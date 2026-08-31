package arch

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type managerRunner struct{ calls []run.Spec }

func (f *managerRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, spec)
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
	if err := m.Install(context.Background(), []string{"firefox"}, false); err != nil {
		t.Fatal(err)
	}
	first := strings.Join(f.calls[0].Args, " ")
	second := strings.Join(f.calls[1].Args, " ")
	if !strings.Contains(first, "pacman -Syu") || strings.Contains(first, "pacman -Sy ") {
		t.Fatalf("unsafe upgrade: %s", first)
	}
	if !strings.Contains(second, "pacman -S --needed") || strings.Contains(second, " -Sy") {
		t.Fatalf("unsafe install: %s", second)
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
	if err := manager.Install(context.Background(), []string{"llvm-libs", "rust"}, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkExplicit(context.Background(), []string{"rust"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-n pacman -S --needed --noconfirm --asdeps -- llvm-libs rust",
		"-n pacman -D --asexplicit -- rust",
	}
	for i, call := range runner.calls {
		if call.Name != "sudo" || call.Interactive || strings.Join(call.Args, " ") != want[i] {
			t.Fatalf("call[%d]=%#v want=%q", i, call, want[i])
		}
	}
}

type artifactStageRunner struct {
	calls          []run.Spec
	stageDir       string
	staged         map[string][]byte
	copyNumber     int
	failCopy       bool
	failValidation bool
	failInstall    bool
	onCopy         func()
	onValidation   func()
	cleaned        bool
	installed      []string
	installedBytes [][]byte
}

func (f *artifactStageRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, spec)
	if spec.Name != "sudo" || len(spec.Args) < 2 || spec.Args[0] != "-n" {
		return run.Result{}, errors.New("unexpected non-privileged staging command")
	}
	args := spec.Args[1:]
	switch args[0] {
	case "stat":
		path := args[len(args)-1]
		switch {
		case path == artifactStageParent:
			return run.Result{Stdout: "0\t43ff\t1\n"}, nil // root, sticky 01777 directory
		case path == f.stageDir:
			return run.Result{Stdout: "0\t41c0\t1\n"}, nil // root 0700 directory
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
			f.installed = append([]string(nil), args[5:]...)
			for _, path := range f.installed {
				f.installedBytes = append(f.installedBytes, append([]byte(nil), f.staged[path]...))
			}
			if f.failInstall {
				return run.Result{}, errors.New("pacman failed")
			}
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
	err := (Manager{Runner: runner}).InstallArtifacts(context.Background(), dir, []string{paru, debug}, []string{"paru"})
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
	}
}

func TestInstallArtifactsRejectsUnsafeSourcesBeforePrivilege(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "fifo", "outside"} {
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
			}
			runner := newArtifactStageRunner()
			if err := (Manager{Runner: runner}).InstallArtifacts(context.Background(), dir, []string{path}, []string{"paru"}); err == nil || len(runner.calls) != 0 {
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
		if err := (Manager{Runner: runner}).InstallArtifacts(context.Background(), dir, []string{linked}, []string{"paru"}); err != nil {
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
			err := (Manager{Runner: runner}).InstallArtifacts(context.Background(), dir, []string{linked}, []string{"paru"})
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
	return run.Result{}, errors.New("sudo timestamp unavailable")
}

func TestBootstrapSudoFailureNeverRetriesInteractively(t *testing.T) {
	runner := &failingSudoRunner{}
	err := (Manager{Runner: runner}).Install(context.Background(), []string{"rust"}, true)
	if err == nil || len(runner.calls) != 1 || runner.calls[0].Interactive || strings.Join(runner.calls[0].Args, " ") != "-n pacman -S --needed --noconfirm --asdeps -- rust" {
		t.Fatalf("err=%v calls=%#v", err, runner.calls)
	}
}
