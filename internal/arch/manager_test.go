package arch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

type managerRunner struct{ calls []run.Spec }

func (f *managerRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, spec)
	if result, ok := testpkg.Query(spec); ok {
		return result, nil
	}
	args := spec.Args
	if spec.Name == "sudo" && len(args) > 0 && args[0] == "-n" {
		args = args[1:]
	}
	if spec.Name == "sudo" && len(args) > 0 {
		switch args[0] {
		case "install":
			data, err := os.ReadFile(args[len(args)-2])
			if err == nil {
				err = os.WriteFile(args[len(args)-1], data, 0o644)
			}
			return run.Result{}, err
		case "mv":
			return run.Result{}, os.Rename(args[len(args)-2], args[len(args)-1])
		case "rm":
			for _, path := range args[4:] {
				_ = os.Remove(path)
			}
		}
	}
	return run.Result{}, nil
}

func TestPacmanCommandsNeverCreatePartialUpgrade(t *testing.T) {
	f := &managerRunner{}
	m := Manager{Runner: f}
	if err := m.FullUpgrade(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(context.Background(), []string{"extra/firefox"}, false); err != nil {
		t.Fatal(err)
	}
	first := strings.Join(f.calls[1].Args, " ")
	second := strings.Join(f.calls[4].Args, " ")
	if f.calls[1].Name != "sudo" || !f.calls[1].Interactive || first != "-n pacman -Syu" {
		t.Fatalf("full upgrade changed from its interactive command shape: %#v", f.calls[1])
	}
	if strings.Contains(first, "--noconfirm") || strings.Contains(first, "pacman -Sy ") {
		t.Fatalf("unsafe upgrade: %s", first)
	}
	if !strings.Contains(second, "pacman -S --noconfirm") || strings.Contains(second, " -Sy") {
		t.Fatalf("unsafe install: %s", second)
	}
	if !f.calls[4].StreamOutput || f.calls[4].Interactive || f.calls[4].Stdin != nil {
		t.Fatalf("approved transaction must stream output without consuming input: %#v", f.calls[1])
	}
}

func TestManagerEnablesFixtureAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pacman.conf")
	_ = os.WriteFile(path, []byte("[core]\nInclude = /mirror\n#[multilib]\n#Include = /mirror\n"), 0o644)
	f := &managerRunner{}
	m := Manager{Runner: f, PacmanConf: path}
	if err := m.EnableMultilib(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	enabled, err := MultilibEnabled(data)
	if err != nil || !enabled {
		t.Fatalf("enabled=%v err=%v data=%s", enabled, err, data)
	}
	for _, call := range f.calls {
		if call.Name == "sudo" && len(call.Args) > 0 && call.Args[0] != "-n" {
			t.Fatalf("sudo could reprompt: %#v", call.Args)
		}
	}
}

func TestBootstrapPackageCommandsAreExactNoninteractiveSudoTransactions(t *testing.T) {
	runner := &managerRunner{}
	manager := Manager{Runner: runner}
	if err := manager.Install(context.Background(), []string{"extra/llvm-libs", "extra/rust"}, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkExplicit(context.Background(), []string{"rust"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-n pacman -S --noconfirm --asdeps -- extra/llvm-libs extra/rust",
		"-n pacman -D --asexplicit -- rust",
	}
	var mutations []run.Spec
	for _, call := range runner.calls {
		if call.Name == "sudo" {
			mutations = append(mutations, call)
		}
	}
	for i, call := range mutations {
		if call.Name != "sudo" || call.Interactive || strings.Join(call.Args, " ") != want[i] {
			t.Fatalf("call[%d]=%#v want=%q", i, call, want[i])
		}
	}
}

type artifactStageRunner struct {
	calls              []run.Spec
	stageDir           string
	staged             map[string][]byte
	copyNumber         int
	failCopy           bool
	failValidation     bool
	failInstall        bool
	failExplicitVerify bool
	onCopy             func()
	onValidation       func()
	cleaned            bool
	installed          []string
	installedBytes     [][]byte
	installReasons     []string
}

func (f *artifactStageRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, spec)
	if spec.Name == "pacman" && len(spec.Args) == 2 && spec.Args[0] == "-Qe" {
		if f.failExplicitVerify {
			return run.Result{}, errors.New("package is not explicit")
		}
		return run.Result{}, nil
	}
	if spec.Name != "sudo" || len(spec.Args) < 2 || spec.Args[0] != "-n" {
		if result, ok := testpkg.Query(spec); ok {
			return result, nil
		}
		return run.Result{}, errors.New("unexpected non-privileged staging command")
	}
	args := spec.Args[1:]
	switch args[0] {
	case "stat":
		path := args[len(args)-1]
		switch {
		case path == artifactStageParent:
			return run.Result{Stdout: "0\t43ff\t2\n"}, nil // root, sticky 01777 directory
		case path == f.stageDir:
			return run.Result{Stdout: "0\t41c0\t2\n"}, nil // root 0700 directory
		case strings.HasPrefix(path, f.stageDir+string(os.PathSeparator)):
			return run.Result{Stdout: "0\t8180\t1\n"}, nil // root 0600 regular file
		}
	case "mktemp":
		return run.Result{Stdout: f.stageDir + "\n"}, nil
	case "install":
		if len(args) != 5 || args[3] != "/dev/stdin" || spec.Stdin == nil {
			return run.Result{}, errors.New("unsafe staging copy command")
		}
		f.copyNumber++
		if f.onCopy != nil {
			f.onCopy()
			f.onCopy = nil
		}
		data, err := io.ReadAll(spec.Stdin)
		if err != nil {
			return run.Result{}, err
		}
		if f.failCopy {
			f.staged[args[4]] = append([]byte(nil), data[:len(data)/2]...)
			return run.Result{}, errors.New("copy failed")
		}
		f.staged[args[4]] = append([]byte(nil), data...)
		return run.Result{}, nil
	case "pacman":
		if len(args) >= 2 && args[1] == "-Qpq" {
			if f.failValidation {
				return run.Result{}, errors.New("invalid archive")
			}
			name, ok := f.staged[args[len(args)-1]]
			if !ok {
				return run.Result{}, errors.New("unknown staged archive")
			}
			if f.onValidation != nil {
				f.onValidation()
				f.onValidation = nil
			}
			return run.Result{Stdout: string(name) + "\n"}, nil
		}
		if len(args) >= 2 && args[1] == "-U" {
			separator := -1
			for i, arg := range args {
				if arg == "--" {
					separator = i
					break
				}
			}
			if separator < 0 {
				return run.Result{}, errors.New("missing artifact transaction separator")
			}
			paths := args[separator+1:]
			reason := ""
			if strings.Contains(strings.Join(args, " "), "--asdeps") {
				reason = "dependency"
			}
			if strings.Contains(strings.Join(args, " "), "--asexplicit") {
				reason = "explicit"
			}
			if reason == "" {
				return run.Result{}, errors.New("artifact transaction did not declare an install reason")
			}
			f.installed = append(f.installed, paths...)
			for _, path := range paths {
				f.installedBytes = append(f.installedBytes, append([]byte(nil), f.staged[path]...))
				f.installReasons = append(f.installReasons, reason)
			}
			if f.failInstall {
				return run.Result{}, errors.New("pacman failed")
			}
			return run.Result{}, nil
		}
		if len(args) >= 2 && args[1] == "-D" {
			return run.Result{}, nil
		}
	case "rm":
		for _, path := range args[3:] {
			delete(f.staged, path)
		}
		return run.Result{}, nil
	case "rmdir":
		f.cleaned = true
		return run.Result{}, nil
	}
	return run.Result{}, errors.New("unexpected protected staging command: " + strings.Join(args, " "))
}

func newArtifactStageRunner() *artifactStageRunner {
	return &artifactStageRunner{stageDir: "/var/tmp/ops-paru-ABCDEFGH", staged: make(map[string][]byte)}
}

type protectedStatRunner struct{ output string }

func (r protectedStatRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	if spec.Name != "sudo" || strings.Join(spec.Args, " ") != "-n stat --format=%u\t%f\t%h -- /protected/path" {
		return run.Result{}, errors.New("unexpected protected stat command")
	}
	return run.Result{Stdout: r.output}, nil
}

func TestValidateProtectedPathDirectoryAndFileInvariants(t *testing.T) {
	for _, test := range []struct {
		name             string
		directory        bool
		uid, mode, links uint64
		wantError        bool
	}{
		{"directory two links", true, 0, syscall.S_IFDIR | 0o700, 2, false},
		{"directory five links", true, 0, syscall.S_IFDIR | 0o755, 5, false},
		{"directory unsafe owner", true, 1000, syscall.S_IFDIR | 0o700, 2, true},
		{"directory group writable", true, 0, syscall.S_IFDIR | 0o720, 2, true},
		{"directory other writable", true, 0, syscall.S_IFDIR | 0o702, 2, true},
		{"directory sticky writable", true, 0, syscall.S_IFDIR | syscall.S_ISVTX | 0o777, 2, true},
		{"directory is regular file", true, 0, syscall.S_IFREG | 0o600, 1, true},
		{"directory is symlink", true, 0, syscall.S_IFLNK | 0o700, 1, true},
		{"file single link", false, 0, syscall.S_IFREG | 0o600, 1, false},
		{"file multiple links", false, 0, syscall.S_IFREG | 0o600, 2, true},
		{"file no links", false, 0, syscall.S_IFREG | 0o600, 0, true},
		{"file unsafe owner", false, 1000, syscall.S_IFREG | 0o600, 1, true},
		{"file group writable", false, 0, syscall.S_IFREG | 0o620, 1, true},
		{"file other writable", false, 0, syscall.S_IFREG | 0o602, 1, true},
		{"file is directory", false, 0, syscall.S_IFDIR | 0o700, 2, true},
		{"file is symlink", false, 0, syscall.S_IFLNK | 0o700, 1, true},
		{"file is fifo", false, 0, syscall.S_IFIFO | 0o600, 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := protectedStatRunner{output: fmt.Sprintf("%d\t%x\t%d\n", test.uid, test.mode, test.links)}
			err := (Manager{Runner: runner}).validateProtectedPath(context.Background(), "/protected/path", test.directory)
			if (err != nil) != test.wantError {
				t.Fatalf("stat=%q err=%v wantError=%v", runner.output, err, test.wantError)
			}
		})
	}
	for _, directory := range []bool{true, false} {
		for _, output := range []string{"", "0\t41c0\tinvalid\n", "0\t41c0\t2\n0\t41c0\t2\n"} {
			if err := (Manager{Runner: protectedStatRunner{output: output}}).validateProtectedPath(context.Background(), "/protected/path", directory); err == nil {
				t.Fatalf("accepted malformed stat: directory=%v output=%q", directory, output)
			}
		}
	}
}

func TestInstallArtifactsBindsStagedBytesAndExcludesDebug(t *testing.T) {
	dir := t.TempDir()
	paru := filepath.Join(dir, "paru.pkg.tar.zst")
	debug := filepath.Join(dir, "paru-debug.pkg.tar.zst")
	if err := os.WriteFile(paru, []byte("paru"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(debug, []byte("paru-debug"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := newArtifactStageRunner()
	replace := func() {
		replacement := filepath.Join(dir, "replacement")
		if err := os.WriteFile(replacement, []byte("paru-debug"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, paru); err != nil {
			t.Fatal(err)
		}
	}
	runner.onCopy = replace       // replacement after source descriptors were opened
	runner.onValidation = replace // replacement after staged identity inspection
	err := (Manager{Runner: runner}).InstallArtifacts(context.Background(), dir, []string{paru, debug}, []string{"paru"}, []string{"paru"})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.installed) != 1 || runner.installed[0] == paru || !strings.HasPrefix(runner.installed[0], runner.stageDir+"/") || string(runner.installedBytes[0]) != "paru" {
		t.Fatalf("installed paths=%v bytes=%q", runner.installed, runner.installedBytes)
	}
	if !runner.cleaned || len(runner.staged) != 0 {
		t.Fatalf("protected stage was not cleaned: %#v", runner)
	}
	for _, call := range runner.calls {
		if call.Name == "sudo" && (len(call.Args) == 0 || call.Args[0] != "-n") {
			t.Fatalf("interactive sudo: %#v", call)
		}
		transaction := call.Name == "sudo" && len(call.Args) > 2 && call.Args[1] == "pacman" && call.Args[2] == "-U"
		if call.StreamOutput != transaction || call.Interactive {
			t.Fatalf("only the approved artifact transaction may stream output: %#v", call)
		}
	}
}

func TestInstallArtifactsSkipsMissingPredictions(t *testing.T) {
	for _, predicted := range []string{"ops-artifact-probe-debug-1-1-any.pkg.tar.zst", "ops-artifact-probe-docs-1-1-any.pkg.tar.zst"} {
		t.Run(predicted, func(t *testing.T) {
			dir := t.TempDir()
			artifact := filepath.Join(dir, "ops-artifact-probe-1-1-any.pkg.tar.zst")
			if err := os.WriteFile(artifact, []byte("ops-artifact-probe"), 0o600); err != nil {
				t.Fatal(err)
			}
			runner := newArtifactStageRunner()
			// Exercise both relative and absolute packagelist paths.
			if err := (Manager{Runner: runner}).InstallArtifacts(context.Background(), dir, []string{predicted, artifact}, []string{"ops-artifact-probe"}, []string{"ops-artifact-probe"}); err != nil {
				t.Fatal(err)
			}
			if runner.copyNumber != 1 || len(runner.installed) != 1 || !strings.HasPrefix(runner.installed[0], runner.stageDir+"/") || string(runner.installedBytes[0]) != "ops-artifact-probe" {
				t.Fatalf("copies=%d installed=%v bytes=%q", runner.copyNumber, runner.installed, runner.installedBytes)
			}
			if !runner.cleaned || len(runner.staged) != 0 {
				t.Fatalf("protected stage was not cleaned: %#v", runner)
			}
		})
	}
}

func TestInstallArtifactsRejectsMissingPlannedOutputBeforeTransaction(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "all missing"
		if partial {
			name = "partially produced"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			artifacts := []string{filepath.Join(dir, "suite-libs.pkg.tar.zst")}
			targets := []string{"suite-libs"}
			if partial {
				cli := filepath.Join(dir, "suite-cli.pkg.tar.zst")
				if err := os.WriteFile(cli, []byte("suite-cli"), 0o600); err != nil {
					t.Fatal(err)
				}
				artifacts = append([]string{cli}, artifacts...)
				targets = append([]string{"suite-cli"}, targets...)
			}
			runner := newArtifactStageRunner()
			err := (Manager{Runner: runner}).InstallArtifacts(context.Background(), dir, artifacts, targets, targets)
			if err == nil || !strings.Contains(err.Error(), `no protected artifact matches planned package "suite-libs"`) {
				t.Fatalf("expected missing planned identity, got %v", err)
			}
			for _, call := range runner.calls {
				if call.Name == "sudo" && len(call.Args) >= 3 && call.Args[1] == "pacman" && (call.Args[2] == "-U" || call.Args[2] == "-D") {
					t.Fatalf("package mutation despite missing planned output: %#v", call)
				}
			}
			if !runner.cleaned || len(runner.staged) != 0 || len(runner.installed) != 0 {
				t.Fatalf("unexpected installation or uncleaned stage: %#v", runner)
			}
		})
	}
}

func TestInstallArtifactsRejectsDuplicateMissingPredictionsBeforePrivilege(t *testing.T) {
	dir := t.TempDir()
	runner := newArtifactStageRunner()
	err := (Manager{Runner: runner}).InstallArtifacts(context.Background(), dir, []string{"missing.pkg.tar.zst", filepath.Join(dir, "missing.pkg.tar.zst")}, []string{"suite"}, []string{"suite"})
	if err == nil || !strings.Contains(err.Error(), "unsafe or duplicate package artifact path") || len(runner.calls) != 0 {
		t.Fatalf("duplicate prediction accepted or privileged staging started: err=%v calls=%#v", err, runner.calls)
	}
}

func TestInstallArtifactsSelectsOnlyExactSplitOutputs(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "suite-cli.pkg.tar.zst")
	libs := filepath.Join(dir, "suite-libs.pkg.tar.zst")
	docs := filepath.Join(dir, "suite-docs.pkg.tar.zst")
	for path, identity := range map[string]string{cli: "suite-cli", libs: "suite-libs", docs: "suite-docs"} {
		if err := os.WriteFile(path, []byte(identity), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runner := newArtifactStageRunner()
	if err := (Manager{Runner: runner}).InstallArtifacts(context.Background(), dir, []string{docs, libs, cli}, []string{"suite-cli", "suite-libs"}, []string{"suite-cli"}); err != nil {
		t.Fatal(err)
	}
	if len(runner.installed) != 2 {
		t.Fatalf("installed=%v bytes=%q", runner.installed, runner.installedBytes)
	}
	if strings.Join(runner.installReasons, ",") != "dependency,dependency" || string(runner.installedBytes[0]) != "suite-cli" || string(runner.installedBytes[1]) != "suite-libs" {
		t.Fatalf("split output reasons=%v bytes=%q", runner.installReasons, runner.installedBytes)
	}
	markedExplicit := false
	for _, call := range runner.calls {
		markedExplicit = markedExplicit || call.Name == "sudo" && strings.Join(call.Args, " ") == "-n pacman -D --asexplicit -- suite-cli"
	}
	if !markedExplicit {
		t.Fatalf("declared split output was not made explicit: %#v", runner.calls)
	}
}

func TestInstallArtifactsFailsWhenExplicitReasonCannotBeVerified(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "suite.pkg.tar.zst")
	if err := os.WriteFile(artifact, []byte("suite"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := newArtifactStageRunner()
	runner.failExplicitVerify = true
	err := (Manager{Runner: runner}).InstallArtifacts(context.Background(), dir, []string{artifact}, []string{"suite"}, []string{"suite"})
	if err == nil || !strings.Contains(err.Error(), "verify explicit package artifact") || !runner.cleaned {
		t.Fatalf("err=%v cleaned=%v", err, runner.cleaned)
	}
}

func TestInstallArtifactsRejectsUnsafeSourcesBeforePrivilege(t *testing.T) {
	for _, kind := range []string{"symlink", "broken symlink", "directory", "fifo", "outside", "missing outside"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "artifact")
			switch kind {
			case "symlink":
				if err := os.WriteFile(filepath.Join(dir, "target"), []byte("paru"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(dir, "target"), path); err != nil {
					t.Fatal(err)
				}
			case "broken symlink":
				if err := os.Symlink(filepath.Join(dir, "missing"), path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "outside":
				path = filepath.Join(t.TempDir(), "artifact")
				if err := os.WriteFile(path, []byte("paru"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing outside":
				path = filepath.Join(t.TempDir(), "missing")
			}
			runner := newArtifactStageRunner()
			if err := (Manager{Runner: runner}).InstallArtifacts(context.Background(), dir, []string{path}, []string{"paru"}, []string{"paru"}); err == nil || len(runner.calls) != 0 {
				t.Fatalf("unsafe source accepted or privileged staging started: err=%v calls=%#v", err, runner.calls)
			}
		})
	}
}

func TestInstallArtifactsCopiesHardlinksAndCleansFailures(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "original")
	linked := filepath.Join(dir, "linked")
	if err := os.WriteFile(original, []byte("paru"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, linked); err != nil {
		t.Fatal(err)
	}
	t.Run("independent inode", func(t *testing.T) {
		runner := newArtifactStageRunner()
		runner.onValidation = func() {
			if err := os.WriteFile(original, []byte("paru-debug"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := (Manager{Runner: runner}).InstallArtifacts(context.Background(), dir, []string{linked}, []string{"paru"}, []string{"paru"}); err != nil {
			t.Fatal(err)
		}
		if len(runner.installedBytes) != 1 || string(runner.installedBytes[0]) != "paru" {
			t.Fatalf("hardlinked source leaked into staged artifact: %q", runner.installedBytes)
		}
	})
	for _, failure := range []string{"copy", "validation", "install"} {
		t.Run(failure, func(t *testing.T) {
			if err := os.WriteFile(original, []byte("paru"), 0o600); err != nil {
				t.Fatal(err)
			}
			runner := newArtifactStageRunner()
			runner.failCopy = failure == "copy"
			runner.failValidation = failure == "validation"
			runner.failInstall = failure == "install"
			err := (Manager{Runner: runner}).InstallArtifacts(context.Background(), dir, []string{linked}, []string{"paru"}, []string{"paru"})
			if err == nil || !runner.cleaned || len(runner.staged) != 0 {
				t.Fatalf("err=%v cleaned=%v staged=%#v", err, runner.cleaned, runner.staged)
			}
			if failure != "install" && len(runner.installed) != 0 {
				t.Fatalf("failed stage reached install: %v", runner.installed)
			}
			if failure == "install" && len(runner.installed) != 1 {
				t.Fatalf("install failure was not exercised: %v", runner.installed)
			}
		})
	}
}

type failingSudoRunner struct{ calls []run.Spec }

func (f *failingSudoRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, spec)
	if spec.Name == "pacman" || spec.Name == "pacman-conf" {
		if result, ok := testpkg.Query(spec); ok {
			return result, nil
		}
	}
	return run.Result{}, errors.New("sudo timestamp unavailable")
}

func TestBootstrapSudoFailureNeverRetriesInteractively(t *testing.T) {
	runner := &failingSudoRunner{}
	err := (Manager{Runner: runner}).Install(context.Background(), []string{"extra/rust"}, true)
	if err == nil || len(runner.calls) != 3 || runner.calls[2].Interactive || strings.Join(runner.calls[2].Args, " ") != "-n pacman -S --noconfirm --asdeps -- extra/rust" {
		t.Fatalf("err=%v calls=%#v", err, runner.calls)
	}
}
