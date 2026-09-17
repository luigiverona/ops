package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
	"github.com/luigiverona/ops/internal/ui"
)

const bootstrapCommit = "0123456789012345678901234567890123456789"
const bootstrapSRCINFO = "pkgbase = paru\n\tpkgver = 2.1.0\n\tpkgrel = 2\n\tmakedepends = cargo\n\npkgname = paru\n"

type aurOrderRunner struct {
	srcinfo                          string
	calls                            []run.Spec
	events                           []string
	output                           *bytes.Buffer
	dependenciesInstalled            bool
	failDependencies                 bool
	providerChanged                  bool
	transactionChanged               bool
	artifactInstalled                bool
	reviewVisibleAtDependencyInstall bool
	buildDir                         string
	stageDir                         string
	staged                           map[string][]byte
}

func (f *aurOrderRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	{
		if result, ok := testpkg.OfficialStage(spec); ok {
			return result, nil
		}
	}
	spec = testpkg.TransactionSpec(spec)

	f.calls = append(f.calls, spec)
	if spec.Name == "pacman" && spec.Args[0] == "-Qq" {
		return run.Result{}, nil
	}
	if spec.Name == "pacman-conf" || spec.Name == "pacman" && (spec.Args[0] == "-Qi" || spec.Args[0] == "-Si" || spec.Args[0] == "-Sl") {
		result, _ := testpkg.Query(spec)
		return result, nil
	}
	if spec.Name == "sudo" {
		args := strings.Join(spec.Args, " ")
		switch {
		case args == "-v":
			f.events = append(f.events, "sudo-v")
			return run.Result{}, nil
		case strings.HasPrefix(args, "-n pacman -Syu"):
			f.events = append(f.events, "upgrade")
			return run.Result{}, nil
		case strings.HasPrefix(args, "-n pacman -S --noconfirm --asdeps -- "):
			f.events = append(f.events, "dependencies")
			f.reviewVisibleAtDependencyInstall = strings.Contains(f.output.String(), "Build and install paru?")
			if f.failDependencies {
				return run.Result{}, errors.New("sudo timestamp unavailable")
			}
			f.dependenciesInstalled = true
			return run.Result{}, nil
		case strings.HasPrefix(args, "-n stat --format=%u\t%f\t%h -- "):
			path := spec.Args[len(spec.Args)-1]
			switch {
			case path == "/var/tmp":
				return run.Result{Stdout: "0\t43ff\t2\n"}, nil
			case path == f.stageDir:
				return run.Result{Stdout: "0\t41c0\t2\n"}, nil
			default:
				return run.Result{Stdout: "0\t8180\t1\n"}, nil
			}
		case strings.HasPrefix(args, "-n mktemp --directory --tmpdir=/var/tmp "):
			f.stageDir = "/var/tmp/ops-paru-APPTEST12"
			f.staged = make(map[string][]byte)
			return run.Result{Stdout: f.stageDir + "\n"}, nil
		case strings.HasPrefix(args, "-n install --mode=0600 -- /dev/stdin "):
			data, err := io.ReadAll(spec.Stdin)
			if err != nil {
				return run.Result{}, err
			}
			f.staged[spec.Args[len(spec.Args)-1]] = append([]byte(nil), data...)
			return run.Result{}, nil
		case strings.HasPrefix(args, "-n pacman -Qpq -- "):
			name := string(f.staged[spec.Args[len(spec.Args)-1]])
			return run.Result{Stdout: name + "\n"}, nil
		case strings.HasPrefix(args, "-n pacman -U --needed --noconfirm --asdeps -- "):
			f.events = append(f.events, "artifact")
			if strings.Contains(args, "paru-debug") || strings.Contains(args, f.buildDir) || !strings.Contains(args, f.stageDir+"/artifact-000.pkg.tar") || string(f.staged[spec.Args[len(spec.Args)-1]]) != "paru" {
				return run.Result{}, errors.New("unplanned artifact installation")
			}
			f.artifactInstalled = true
			return run.Result{}, nil
		case strings.HasPrefix(args, "-n pacman -D --asexplicit -- "):
			return run.Result{}, nil
		case strings.HasPrefix(args, "-n rm -f -- "):
			for _, path := range spec.Args[4:] {
				delete(f.staged, path)
			}
			return run.Result{}, nil
		case args == "-n rmdir -- "+f.stageDir:
			return run.Result{}, nil
		default:
			return run.Result{}, errors.New("unexpected sudo command: " + args)
		}
	}
	if spec.Name == "pacman" {
		if len(spec.Args) == 2 && (spec.Args[0] == "-Qe" || spec.Args[0] == "-Qm") && spec.Args[1] == "paru" {
			return run.Result{}, nil
		}
		if len(spec.Args) > 0 && spec.Args[0] == "-T" {
			requirement := spec.Args[len(spec.Args)-1]
			if f.dependenciesInstalled {
				return run.Result{}, nil
			}
			return run.Result{Stdout: requirement + "\n"}, &run.Error{Name: "pacman", Err: diagnosticExit(127)}
		}
		if len(spec.Args) > 0 && spec.Args[0] == "-Sp" {
			if len(spec.Args) > 3 && spec.Args[3] == "%r/%n" {
				separator := 0
				for i, arg := range spec.Args {
					if arg == "--" {
						separator = i
						break
					}
				}
				transaction := append([]string(nil), spec.Args[separator+1:]...)
				if f.transactionChanged {
					transaction = append(transaction, "extra/surprise-package")
				}
				return run.Result{Stdout: strings.Join(transaction, "\n") + "\n"}, nil
			}
			switch spec.Args[len(spec.Args)-1] {
			case "base-devel":
				return run.Result{Stdout: "extra/base-devel\t\n"}, nil
			case "cargo":
				if f.providerChanged {
					return run.Result{Stdout: "extra/rustup\tcargo\n"}, nil
				}
				return run.Result{Stdout: "extra/rust\tcargo rustfmt\nextra/llvm-libs\t\n"}, nil
			}
		}
		if len(spec.Args) > 0 && spec.Args[0] == "-Q" {
			return run.Result{}, nil
		}
		return run.Result{}, errors.New("unexpected pacman command")
	}
	if spec.Name == "git" {
		switch {
		case len(spec.Args) >= 2 && spec.Args[0] == "init":
			return run.Result{}, os.MkdirAll(spec.Args[len(spec.Args)-1], 0o700)
		case len(spec.Args) >= 3 && spec.Args[0] == "-C" && spec.Args[2] == "checkout":
			repo := spec.Args[1]
			metadata := f.srcinfo
			if metadata == "" {
				metadata = bootstrapSRCINFO
			}
			if err := os.WriteFile(filepath.Join(repo, ".SRCINFO"), []byte(metadata), 0o600); err != nil {
				return run.Result{}, err
			}
			return run.Result{}, os.WriteFile(filepath.Join(repo, "PKGBUILD"), []byte("pkgname=paru\n"), 0o600)
		case len(spec.Args) >= 3 && spec.Args[0] == "-C" && spec.Args[2] == "rev-parse":
			return run.Result{Stdout: bootstrapCommit + "\n"}, nil
		case len(spec.Args) >= 3 && spec.Args[0] == "-C" && spec.Args[2] == "ls-files":
			return run.Result{Stdout: ".SRCINFO\x00PKGBUILD\x00"}, nil
		default:
			return run.Result{}, nil
		}
	}
	if spec.Name == "makepkg" && len(spec.Args) == 0 {
		f.events = append(f.events, "makepkg")
		f.buildDir = spec.Dir
		for name, contents := range map[string]string{"paru-2.1.0-2-x86_64.pkg.tar.zst": "paru", "paru-debug-2.1.0-2-x86_64.pkg.tar.zst": "paru-debug"} {
			if err := os.WriteFile(filepath.Join(spec.Dir, name), []byte(contents), 0o600); err != nil {
				return run.Result{}, err
			}
		}
		return run.Result{}, nil
	}
	if spec.Name == "makepkg" && strings.Join(spec.Args, " ") == "--packagelist" {
		return run.Result{Stdout: filepath.Join(spec.Dir, "paru-2.1.0-2-x86_64.pkg.tar.zst") + "\n" + filepath.Join(spec.Dir, "paru-debug-2.1.0-2-x86_64.pkg.tar.zst") + "\n"}, nil
	}
	if spec.Name == "paru" && strings.Join(spec.Args, " ") == "--version" {
		if !f.artifactInstalled {
			return run.Result{}, errors.New("paru not installed")
		}
		return run.Result{Stdout: "paru v2\n"}, nil
	}
	if spec.Name == "flatpak" && len(spec.Args) > 0 && spec.Args[0] == "remotes" {
		return testpkg.FlatpakRemotes(testpkg.Flathub), nil
	}
	return run.Result{}, errors.New("unexpected command: " + spec.Name + " " + strings.Join(spec.Args, " "))
}

func declaredParuPlan(t *testing.T) plan.Plan {
	t.Helper()
	metadata, err := aurmeta.Parse([]byte(bootstrapSRCINFO))
	if err != nil {
		t.Fatal(err)
	}
	source := plan.AURSource{Commit: bootstrapCommit, Metadata: metadata}
	state := readyExecutionState()
	state.Installed["base-devel"] = false
	state.Explicit["base-devel"] = false
	p := resolveAndPlan(context.Background(), config.Config{Version: 2, Applications: []config.Application{{Source: "aur", Identifier: "paru"}}}, state, outputResolver{
		aur: map[string]plan.Package{"paru": {Name: "paru", PackageBase: "paru"}}, source: &source,
		deps: map[string]plan.OfficialDependency{
			"base-devel": {Requirement: "base-devel", Provider: "extra/base-devel", Packages: []string{"extra/base-devel"}},
			"cargo":      {Requirement: "cargo", Provider: "extra/rust", Packages: []string{"extra/llvm-libs", "extra/rust"}},
		},
	})
	return p
}

func TestDeclaredAURReviewDependencyBuildOrder(t *testing.T) {
	p := declaredParuPlan(t)
	var output bytes.Buffer
	runner := &aurOrderRunner{output: &output}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).executeForTest(context.Background(), p, ui.UI{In: strings.NewReader("y\n\ny\n"), Out: &output})
	if code != Success {
		t.Fatalf("code=%d\n%s", code, output.String())
	}
	if strings.Join(runner.events, ",") != "sudo-v,upgrade,dependencies,makepkg,artifact" {
		t.Fatalf("events=%v", runner.events)
	}
	if !runner.reviewVisibleAtDependencyInstall {
		t.Fatal("bootstrap dependencies were installed before AUR review acceptance")
	}
	interactiveSudo := 0
	for _, call := range runner.calls {
		if call.Name == "sudo" {
			if strings.Join(call.Args, " ") == "-v" && call.Interactive {
				interactiveSudo++
				continue
			}
			if len(call.Args) == 0 || call.Args[0] != "-n" {
				t.Fatalf("bootstrap sudo could reprompt: %#v", call)
			}
			if len(call.Args) > 2 && (call.Args[2] == "-S" || call.Args[2] == "-U") && call.Interactive {
				t.Fatalf("approved bootstrap transaction became interactive: %#v", call)
			}
		}
		if call.Name == "makepkg" && (call.Interactive || call.StreamOutput) {
			t.Fatalf("makepkg leaked an unnecessary interactive stream: %#v", call)
		}
	}
	if interactiveSudo != 1 {
		t.Fatalf("interactive sudo authorizations=%d", interactiveSudo)
	}
	for _, call := range runner.calls {
		if call.Name == "sudo" && len(call.Args) > 2 && call.Args[2] == "-S" {
			args := strings.Join(call.Args, " ")
			if strings.Contains(args, " cargo") || !strings.Contains(args, " extra/base-devel") || !strings.Contains(args, " extra/llvm-libs") || !strings.Contains(args, " extra/rust") {
				t.Fatalf("unresolved or incomplete dependency transaction reached mutation: %s", args)
			}
		}
	}
	wantProgress := []string{"Updating system...", "Reviewing paru...", "Installing build dependencies...", "Building paru...", "Installing paru..."}
	if got := progressRecords(output.String()); strings.Join(got, "\n") != strings.Join(wantProgress, "\n") {
		t.Fatalf("progress=%v, want=%v\n%s", got, wantProgress, output.String())
	}
	assertConciseOutput(t, output.String())
	if strings.Count(output.String(), "Build and install paru? [y/N]") != 1 || strings.Count(output.String(), "Continue? [Y/n]") != 1 {
		t.Fatalf("unexpected approval boundaries: %s", &output)
	}
}

func TestPreparePlanDeclinedTopLevelParuPlanMutatesNothing(t *testing.T) {
	p := declaredParuPlan(t)
	var output bytes.Buffer
	runner := &aurOrderRunner{output: &output}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).executeForTest(context.Background(), p, ui.UI{In: strings.NewReader("n\n"), Out: &output})
	if code != Success || len(runner.calls) != 0 {
		t.Fatalf("code=%d calls=%#v\n%s", code, runner.calls, output.String())
	}
}

func TestPreparePlanDeclinedParuReviewDoesNotMutateBuildPackages(t *testing.T) {
	p := declaredParuPlan(t)
	var output bytes.Buffer
	runner := &aurOrderRunner{output: &output}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).executeForTest(context.Background(), p, ui.UI{In: strings.NewReader("y\n\nn\n"), Out: &output})
	if code != Issues {
		t.Fatalf("code=%d\n%s", code, output.String())
	}
	for _, event := range runner.events {
		if event == "dependencies" || event == "makepkg" || event == "artifact" {
			t.Fatalf("declined review allowed bootstrap mutation: %v", runner.events)
		}
	}
}

func TestSigningKeyPreparationRequiresBuildApproval(t *testing.T) {
	const fingerprint = "0123456789ABCDEF0123456789ABCDEF01234567"
	t.Setenv("GNUPGHOME", filepath.Join(t.TempDir(), "gnupg"))
	for _, answer := range []string{"q\n", "\n\n", "\nn\n", "\ny\n"} {
		p := declaredParuPlan(t)
		metadata := strings.Replace(bootstrapSRCINFO, "\npkgname", "\nvalidpgpkeys = "+fingerprint+"\npkgname", 1)
		p.Applications[0].AURSource.Metadata = paruSigningMetadata(t, metadata)
		p.Applications[0].AURSigningKeys = []string{fingerprint}
		var out bytes.Buffer
		runner := &aurOrderRunner{srcinfo: metadata, output: &out}
		code := (Runtime{Runner: runner, Out: &out, Err: &out}).executeForTest(context.Background(), p, ui.UI{In: strings.NewReader("y\n" + answer), Out: &out})
		if code != Issues {
			t.Fatalf("code=%d output=%s", code, &out)
		}
		keyPreparation := false
		for _, call := range runner.calls {
			keyPreparation = keyPreparation || call.Name == "gpg"
		}
		if keyPreparation != (answer == "\ny\n") {
			t.Fatalf("unapproved key preparation: answer=%q calls=%v", answer, runner.calls)
		}
		if answer != "q\n" {
			keyAt, approvalAt := strings.Index(out.String(), fingerprint), strings.Index(out.String(), "Build and install paru? [y/N]")
			if keyAt < 0 || approvalAt <= keyAt {
				t.Fatalf("key not disclosed before approval: %s", &out)
			}
		}
		for _, event := range runner.events {
			if event == "dependencies" || event == "makepkg" || event == "artifact" {
				t.Fatalf("failed/unapproved key allowed build: %v", runner.events)
			}
		}
	}
}

func paruSigningMetadata(t *testing.T, text string) aurmeta.Metadata {
	t.Helper()
	metadata, err := aurmeta.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return metadata
}

func TestCancelledAURSourceViewDoesNotAuthorizeBuild(t *testing.T) {
	var output bytes.Buffer
	runner := &aurOrderRunner{output: &output}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).executeForTest(context.Background(), declaredParuPlan(t), ui.UI{In: strings.NewReader("y\nq\ny\n"), Out: &output})
	if code != Issues || strings.Contains(output.String(), "Build and install paru?") {
		t.Fatalf("code=%d output=%s", code, &output)
	}
	for _, event := range runner.events {
		if event == "dependencies" || event == "makepkg" || event == "artifact" {
			t.Fatalf("cancelled review authorized %s", event)
		}
	}
}

func TestAUREOFStopsBeforeLaterApplicationsAndPrompts(t *testing.T) {
	for _, answer := range []string{"", "\n"} {
		var output bytes.Buffer
		runner := &aurOrderRunner{output: &output}
		p := declaredParuPlan(t)
		p.ConfigureGit = true
		p.Applications = append(p.Applications, plan.Application{Declaration: config.Application{Source: config.Flatpak, Identifier: "org.example.Later"}, State: plan.Install})
		code := (Runtime{Runner: runner, Out: &output, Err: &output}).executeForTest(context.Background(), p, ui.UI{In: strings.NewReader("y\n" + answer), Out: &output})
		if code != Fatal || strings.Contains(output.String(), "Git name:") || strings.Contains(output.String(), "Installing org.example.Later") {
			t.Fatalf("EOF continued work: code=%d output=%s", code, &output)
		}
		if strings.Join(runner.events, ",") != "sudo-v,upgrade" {
			t.Fatalf("EOF allowed AUR mutation: %v", runner.events)
		}
		for _, call := range runner.calls {
			if call.Name == "flatpak" || call.Name == "makepkg" || call.Name == "gpg" || call.Name == "git" && len(call.Args) > 0 && call.Args[0] == "config" {
				t.Fatalf("EOF allowed later command: %#v", call)
			}
		}
		if strings.Contains(output.String(), "Interrupted.") || !strings.Contains(output.String(), "EOF") {
			t.Fatalf("EOF misreported: %s", &output)
		}
	}
}

type skippedAURRunner struct {
	*aurOrderRunner
	workstation *lifecycleRunner
}

func (r skippedAURRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if s.Name == "git" && len(s.Args) > 0 && (s.Args[0] == "init" || s.Args[0] == "-C") {
		return r.aurOrderRunner.Run(ctx, s)
	}
	return r.workstation.Run(ctx, s)
}

func TestIntentionalAURSkipContinuesAndReinspects(t *testing.T) {
	for _, answer := range []string{"q\n", "\nn\n", "\n\n"} {
		t.Run(fmt.Sprintf("%q", answer), func(t *testing.T) {
			a, workstation, out := minimalRuntime(t)
			ctx := context.Background()
			cfg := config.Config{Version: 2}
			if code := a.preparePlan(ctx, cfg, plan.Build(cfg, plan.State{}, nil), ui.UI{In: strings.NewReader("y\nUser\nuser@example.com\n"), Out: out}); code != Success {
				t.Fatalf("fixture=%d: %s", code, out)
			}
			p := declaredParuPlan(t)
			// A separate declared pacman app must still be installed after skip.
			p.Applications = append(p.Applications, plan.Application{Package: plan.Package{Name: "firefox", Repository: "extra"}, Declaration: config.Application{Source: config.Pacman, Identifier: "firefox"}, State: plan.Install})
			for _, app := range p.Applications {
				cfg.Applications = append(cfg.Applications, app.Declaration)
			}
			ar := &aurOrderRunner{output: out}
			a.Runner = skippedAURRunner{aurOrderRunner: ar, workstation: workstation}
			out.Reset()
			workstation.events = nil
			code := a.preparePlan(ctx, cfg, p, ui.UI{In: strings.NewReader("y\n" + answer), Out: out})
			if code != Issues || !workstation.installed["firefox"] || !strings.Contains(strings.Join(workstation.events, "\n"), "pacman -Qq") {
				t.Fatalf("code=%d events=%v\n%s", code, workstation.events, out)
			}
			if strings.Count(out.String(), "Skipped paru.") != 1 || !strings.Contains(out.String(), "After setup: the declared application is not installed") || !strings.Contains(out.String(), "Workstation setup incomplete.") {
				t.Fatalf("missing skip or established reinspection conclusion: %s", out)
			}
			if strings.Contains(out.String(), "\nFailed\n") {
				t.Fatalf("skip became an operation failure: %s", out)
			}
			for _, call := range ar.calls {
				if call.Name == "makepkg" || call.Name == "gpg" || call.Name == "sudo" {
					t.Fatalf("unapproved AUR mutation: %#v", call)
				}
			}
		})
	}
}

type cancelOnOutput struct {
	io.Writer
	cancel context.CancelFunc
	marker string
}

func (w cancelOnOutput) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if strings.Contains(string(p), w.marker) {
		w.cancel()
	}
	return n, err
}

func TestInterruptedAURReviewStopsBeforeLaterPromptsOrBuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	runner := &aurOrderRunner{output: &output}
	p := declaredParuPlan(t)
	p.ConfigureGit = true
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).executeForTest(ctx, p, ui.UI{
		In: strings.NewReader("y\n"), Out: cancelOnOutput{Writer: &output, cancel: cancel, marker: "AUR source review"},
	})
	if code != Fatal || strings.Contains(output.String(), "Git name:") || strings.Contains(output.String(), "Build and install paru?") {
		t.Fatalf("code=%d output=%s", code, &output)
	}
	if strings.Join(runner.events, ",") != "sudo-v,upgrade" || !strings.Contains(output.String(), "Earlier changes may remain") {
		t.Fatalf("events=%v output=%s", runner.events, &output)
	}
}

func TestPreparePlanParuProviderDriftFailsBeforeBuildDependencyMutation(t *testing.T) {
	p := declaredParuPlan(t)
	var output bytes.Buffer
	runner := &aurOrderRunner{output: &output, providerChanged: true}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).executeForTest(context.Background(), p, ui.UI{In: strings.NewReader("y\n\ny\n"), Out: &output})
	if code != Issues || !strings.Contains(output.String(), "provider or repository changed after planning") {
		t.Fatalf("code=%d events=%v\n%s", code, runner.events, output.String())
	}
	for _, event := range runner.events {
		if event == "dependencies" || event == "makepkg" || event == "artifact" {
			t.Fatalf("provider drift allowed bootstrap mutation: %v", runner.events)
		}
	}
}

func TestPreparePlanParuTransactionDriftFailsBeforeBuildDependencyMutation(t *testing.T) {
	p := declaredParuPlan(t)
	var output bytes.Buffer
	runner := &aurOrderRunner{output: &output, transactionChanged: true}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).executeForTest(context.Background(), p, ui.UI{In: strings.NewReader("y\n\ny\n"), Out: &output})
	if code != Issues || !strings.Contains(output.String(), "transaction changed after planning") {
		t.Fatalf("code=%d events=%v\n%s", code, runner.events, output.String())
	}
	for _, event := range runner.events {
		if event == "dependencies" || event == "makepkg" || event == "artifact" {
			t.Fatalf("transaction drift allowed bootstrap mutation: %v", runner.events)
		}
	}
}

func TestPreparePlanFailedNoninteractiveSudoDoesNotRetry(t *testing.T) {
	p := declaredParuPlan(t)
	var output bytes.Buffer
	runner := &aurOrderRunner{output: &output, failDependencies: true}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).executeForTest(context.Background(), p, ui.UI{In: strings.NewReader("y\n\ny\n"), Out: &output})
	if code != Issues {
		t.Fatalf("code=%d\n%s", code, output.String())
	}
	interactiveAuthorizations := 0
	for _, call := range runner.calls {
		if call.Name == "sudo" && strings.Join(call.Args, " ") == "-v" {
			interactiveAuthorizations++
		}
	}
	if interactiveAuthorizations != 1 || strings.Contains(strings.Join(runner.events, ","), "makepkg") || strings.Contains(strings.Join(runner.events, ","), "artifact") {
		t.Fatalf("sudo failure retried or continued: authorizations=%d events=%v", interactiveAuthorizations, runner.events)
	}
}
