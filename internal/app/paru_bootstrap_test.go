package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
)

const bootstrapCommit = "0123456789012345678901234567890123456789"
const bootstrapSRCINFO = "pkgbase = paru\n\tpkgver = 2.1.0\n\tpkgrel = 2\n\tmakedepends = cargo\n\npkgname = paru\n"

type paruBootstrapRunner struct {
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

func (f *paruBootstrapRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, spec)
	if spec.Name == "sudo" {
		args := strings.Join(spec.Args, " ")
		switch {
		case args == "-v":
			f.events = append(f.events, "sudo-v")
			return run.Result{}, nil
		case args == "-n pacman -Syu":
			f.events = append(f.events, "upgrade")
			return run.Result{}, nil
		case strings.HasPrefix(args, "-n pacman -S --needed --noconfirm --asdeps -- "):
			f.events = append(f.events, "dependencies")
			f.reviewVisibleAtDependencyInstall = strings.Contains(f.output.String(), "Build and install this reviewed AUR package?")
			if f.failDependencies {
				return run.Result{}, errors.New("sudo timestamp unavailable")
			}
			f.dependenciesInstalled = true
			return run.Result{}, nil
		case strings.HasPrefix(args, "-n stat --format=%u\t%f\t%h -- "):
			path := spec.Args[len(spec.Args)-1]
			switch {
			case path == "/var/tmp":
				return run.Result{Stdout: "0\t43ff\t1\n"}, nil
			case path == f.stageDir:
				return run.Result{Stdout: "0\t41c0\t1\n"}, nil
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
		case strings.HasPrefix(args, "-n pacman -U --needed --noconfirm -- "):
			f.events = append(f.events, "artifact")
			if strings.Contains(args, "paru-debug") || strings.Contains(args, f.buildDir) || !strings.Contains(args, f.stageDir+"/artifact-000.pkg.tar") || string(f.staged[spec.Args[len(spec.Args)-1]]) != "paru" {
				return run.Result{}, errors.New("unplanned artifact installation")
			}
			f.artifactInstalled = true
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
		if len(spec.Args) > 0 && spec.Args[0] == "-T" {
			requirement := spec.Args[len(spec.Args)-1]
			if f.dependenciesInstalled {
				return run.Result{}, nil
			}
			return run.Result{Stdout: requirement + "\n"}, errors.New("exit 127")
		}
		if len(spec.Args) > 0 && spec.Args[0] == "-Sp" {
			if len(spec.Args) > 4 && spec.Args[4] == "%n" {
				separator := 0
				for i, arg := range spec.Args {
					if arg == "--" {
						separator = i
						break
					}
				}
				transaction := append([]string(nil), spec.Args[separator+1:]...)
				if f.transactionChanged {
					transaction = append(transaction, "surprise-package")
				}
				return run.Result{Stdout: strings.Join(transaction, "\n") + "\n"}, nil
			}
			switch spec.Args[len(spec.Args)-1] {
			case "base-devel":
				return run.Result{Stdout: "base-devel\t\n"}, nil
			case "cargo":
				if f.providerChanged {
					return run.Result{Stdout: "rustup\tcargo\n"}, nil
				}
				return run.Result{Stdout: "rust\tcargo rustfmt\nllvm-libs\t\n"}, nil
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
			if err := os.WriteFile(filepath.Join(repo, ".SRCINFO"), []byte(bootstrapSRCINFO), 0o600); err != nil {
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
		return run.Result{Stdout: "flathub\n"}, nil
	}
	return run.Result{}, errors.New("unexpected command: " + spec.Name + " " + strings.Join(spec.Args, " "))
}

func paruBootstrapPlan(t *testing.T) plan.Plan {
	t.Helper()
	metadata, err := aurmeta.Parse([]byte(bootstrapSRCINFO))
	if err != nil {
		t.Fatal(err)
	}
	source := plan.AURSource{Commit: bootstrapCommit, Metadata: metadata}
	state := readyExecutionState()
	state.Paru = false
	state.Installed["base-devel"] = false
	p, err := plan.Build(context.Background(), config.Config{Version: 1}, state, outputResolver{
		source: &source,
		deps: map[string]plan.OfficialDependency{
			"base-devel": {Requirement: "base-devel", Provider: "base-devel", Packages: []string{"base-devel"}},
			"cargo":      {Requirement: "cargo", Provider: "rust", Packages: []string{"llvm-libs", "rust"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPreparePlanDeterministicParuBootstrap(t *testing.T) {
	p := paruBootstrapPlan(t)
	var output bytes.Buffer
	runner := &paruBootstrapRunner{output: &output}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\ny\n"), Out: &output})
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
		if call.Name == "makepkg" && len(call.Args) == 0 && !call.Interactive {
			t.Fatalf("makepkg did not run as the interactive normal-user build: %#v", call)
		}
	}
	if interactiveSudo != 1 {
		t.Fatalf("interactive sudo authorizations=%d", interactiveSudo)
	}
	for _, call := range runner.calls {
		if call.Name == "sudo" && len(call.Args) > 2 && call.Args[2] == "-S" {
			args := strings.Join(call.Args, " ")
			if strings.Contains(args, " cargo") || !strings.Contains(args, " base-devel") || !strings.Contains(args, " llvm-libs") || !strings.Contains(args, " rust") {
				t.Fatalf("unresolved or incomplete dependency transaction reached mutation: %s", args)
			}
		}
	}
	wantProgress := []string{
		"full system upgrade|upgrade|pacman",
		"paru -> base-devel|install|pacman; build dependency",
		"paru -> llvm-libs|install|pacman; build dependency",
		"paru -> rust|install|pacman; provides cargo; build dependency",
		"paru|install|AUR build",
		"paru|install|local package",
	}
	if got := progressRecords(output.String()); strings.Join(got, "\n") != strings.Join(wantProgress, "\n") {
		t.Fatalf("progress=%v, want=%v\n%s", got, wantProgress, output.String())
	}
}

func TestPreparePlanDeclinedTopLevelParuPlanMutatesNothing(t *testing.T) {
	p := paruBootstrapPlan(t)
	var output bytes.Buffer
	runner := &paruBootstrapRunner{output: &output}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("n\n"), Out: &output})
	if code != Success || len(runner.calls) != 0 {
		t.Fatalf("code=%d calls=%#v\n%s", code, runner.calls, output.String())
	}
}

func TestPreparePlanDeclinedParuReviewDoesNotMutateBootstrapPackages(t *testing.T) {
	p := paruBootstrapPlan(t)
	var output bytes.Buffer
	runner := &paruBootstrapRunner{output: &output}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\nn\n"), Out: &output})
	if code != Fatal {
		t.Fatalf("code=%d\n%s", code, output.String())
	}
	for _, event := range runner.events {
		if event == "dependencies" || event == "makepkg" || event == "artifact" {
			t.Fatalf("declined review allowed bootstrap mutation: %v", runner.events)
		}
	}
}

func TestPreparePlanParuProviderDriftFailsBeforeBootstrapMutation(t *testing.T) {
	p := paruBootstrapPlan(t)
	var output bytes.Buffer
	runner := &paruBootstrapRunner{output: &output, providerChanged: true}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\ny\n"), Out: &output})
	if code != Fatal || !strings.Contains(output.String(), "provider changed after planning") {
		t.Fatalf("code=%d events=%v\n%s", code, runner.events, output.String())
	}
	for _, event := range runner.events {
		if event == "dependencies" || event == "makepkg" || event == "artifact" {
			t.Fatalf("provider drift allowed bootstrap mutation: %v", runner.events)
		}
	}
}

func TestPreparePlanParuTransactionDriftFailsBeforeBootstrapMutation(t *testing.T) {
	p := paruBootstrapPlan(t)
	var output bytes.Buffer
	runner := &paruBootstrapRunner{output: &output, transactionChanged: true}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\ny\n"), Out: &output})
	if code != Fatal || !strings.Contains(output.String(), "transaction changed after planning") {
		t.Fatalf("code=%d events=%v\n%s", code, runner.events, output.String())
	}
	for _, event := range runner.events {
		if event == "dependencies" || event == "makepkg" || event == "artifact" {
			t.Fatalf("transaction drift allowed bootstrap mutation: %v", runner.events)
		}
	}
}

func TestPreparePlanFailedNoninteractiveSudoDoesNotRetry(t *testing.T) {
	p := paruBootstrapPlan(t)
	var output bytes.Buffer
	runner := &paruBootstrapRunner{output: &output, failDependencies: true}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\ny\n"), Out: &output})
	if code != Fatal {
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
