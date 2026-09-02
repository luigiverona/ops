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

	"github.com/luigiverona/ops/internal/arch"
	"github.com/luigiverona/ops/internal/aur"
	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
)

const applicationAURCommit = "0123456789012345678901234567890123456789"
const applicationAURSRCINFO = "pkgbase = browser-bin\n\tpkgver = 1\n\tpkgrel = 1\n\tdepends = runtime\n\tmakedepends = builder\n\npkgname = browser-bin\n"

type applicationAURRunner struct {
	calls                 []run.Spec
	dependenciesInstalled bool
	stageDir              string
	staged                map[string][]byte
	artifact              string
	debugArtifact         string
	revisionChecks        int
}

func (r *applicationAURRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	r.calls = append(r.calls, spec)
	if spec.Name == "paru" {
		return run.Result{}, errors.New("direct paru application installation is forbidden")
	}
	if spec.Name == "sudo" {
		args := strings.Join(spec.Args, " ")
		switch {
		case strings.HasPrefix(args, "-n pacman -S --needed --noconfirm --asdeps -- "):
			r.dependenciesInstalled = true
			return run.Result{}, nil
		case strings.HasPrefix(args, "-n stat --format=%u\t%f\t%h -- "):
			path := spec.Args[len(spec.Args)-1]
			if path == "/var/tmp" {
				return run.Result{Stdout: "0\t43ff\t1\n"}, nil
			}
			if path == r.stageDir {
				return run.Result{Stdout: "0\t41c0\t1\n"}, nil
			}
			return run.Result{Stdout: "0\t8180\t1\n"}, nil
		case strings.HasPrefix(args, "-n mktemp --directory --tmpdir=/var/tmp "):
			r.stageDir = "/var/tmp/ops-paru-AURAPP123"
			r.staged = make(map[string][]byte)
			return run.Result{Stdout: r.stageDir + "\n"}, nil
		case strings.HasPrefix(args, "-n install --mode=0600 -- /dev/stdin "):
			data, err := io.ReadAll(spec.Stdin)
			if err != nil {
				return run.Result{}, err
			}
			r.staged[spec.Args[len(spec.Args)-1]] = data
			return run.Result{}, nil
		case strings.HasPrefix(args, "-n pacman -Qpq -- "):
			return run.Result{Stdout: string(r.staged[spec.Args[len(spec.Args)-1]]) + "\n"}, nil
		case strings.HasPrefix(args, "-n pacman -U --needed --noconfirm -- "):
			if len(spec.Args) != 7 || string(r.staged[spec.Args[6]]) != "browser-bin" {
				return run.Result{}, errors.New("unexpected AUR artifact transaction")
			}
			return run.Result{}, nil
		case strings.HasPrefix(args, "-n rm -f -- "), strings.HasPrefix(args, "-n rmdir -- "):
			return run.Result{}, nil
		default:
			return run.Result{}, errors.New("unexpected sudo command: " + args)
		}
	}
	if spec.Name == "pacman" {
		if len(spec.Args) > 0 && spec.Args[0] == "-T" {
			requirement := spec.Args[len(spec.Args)-1]
			if requirement == "base-devel" || r.dependenciesInstalled {
				return run.Result{}, nil
			}
			return run.Result{Stdout: requirement + "\n"}, errors.New("missing dependency")
		}
		if len(spec.Args) > 0 && spec.Args[0] == "-Sp" {
			format := ""
			for i, arg := range spec.Args {
				if arg == "--print-format" && i+1 < len(spec.Args) {
					format = spec.Args[i+1]
				}
			}
			if format == "%n\t%P" {
				return run.Result{Stdout: spec.Args[len(spec.Args)-1] + "\t\n"}, nil
			}
			for i, arg := range spec.Args {
				if arg == "--" {
					return run.Result{Stdout: strings.Join(spec.Args[i+1:], "\n") + "\n"}, nil
				}
			}
		}
	}
	if spec.Name == "git" {
		switch {
		case len(spec.Args) >= 2 && spec.Args[0] == "init":
			return run.Result{}, os.MkdirAll(spec.Args[len(spec.Args)-1], 0o700)
		case len(spec.Args) >= 3 && spec.Args[0] == "-C" && spec.Args[2] == "checkout":
			repo := spec.Args[1]
			if err := os.WriteFile(filepath.Join(repo, ".SRCINFO"), []byte(applicationAURSRCINFO), 0o600); err != nil {
				return run.Result{}, err
			}
			return run.Result{}, os.WriteFile(filepath.Join(repo, "PKGBUILD"), []byte("pkgname=browser-bin\n"), 0o600)
		case len(spec.Args) >= 3 && spec.Args[0] == "-C" && spec.Args[2] == "rev-parse":
			r.revisionChecks++
			return run.Result{Stdout: applicationAURCommit + "\n"}, nil
		case len(spec.Args) >= 3 && spec.Args[0] == "-C" && spec.Args[2] == "ls-files":
			return run.Result{Stdout: ".SRCINFO\x00PKGBUILD\x00"}, nil
		default:
			return run.Result{}, nil
		}
	}
	if spec.Name == "makepkg" && len(spec.Args) == 0 {
		r.artifact = filepath.Join(spec.Dir, "browser-bin-1-1-x86_64.pkg.tar.zst")
		r.debugArtifact = filepath.Join(spec.Dir, "browser-bin-debug-1-1-x86_64.pkg.tar.zst")
		if err := os.WriteFile(r.artifact, []byte("browser-bin"), 0o600); err != nil {
			return run.Result{}, err
		}
		return run.Result{}, os.WriteFile(r.debugArtifact, []byte("browser-bin-debug"), 0o600)
	}
	if spec.Name == "makepkg" && strings.Join(spec.Args, " ") == "--packagelist" {
		return run.Result{Stdout: r.artifact + "\n" + r.debugArtifact + "\n"}, nil
	}
	return run.Result{}, errors.New("unexpected command: " + spec.Name + " " + strings.Join(spec.Args, " "))
}

func TestAURApplicationBuildIsPinnedNoninteractiveAndInstallsOnlySelectedOutput(t *testing.T) {
	metadata, err := aurmeta.Parse([]byte(applicationAURSRCINFO))
	if err != nil {
		t.Fatal(err)
	}
	app := plan.Application{
		Declaration:     config.Application{Identifier: "browser-bin", Source: "aur"},
		State:           "install",
		AURSource:       plan.AURSource{Commit: applicationAURCommit, Metadata: metadata},
		AUROutputs:      []string{"browser-bin"},
		AURDependencies: []plan.OfficialDependency{{Requirement: "base-devel", Satisfied: true}, {Requirement: "builder", Provider: "builder", Packages: []string{"builder"}}, {Requirement: "runtime", Provider: "runtime", Packages: []string{"runtime"}}},
		AURPackages:     []plan.BootstrapPackage{{Name: "builder", Purposes: []string{"build"}}, {Name: "runtime", Purposes: []string{"runtime"}}},
	}
	runner := &applicationAURRunner{}
	var output bytes.Buffer
	runtime := Runtime{Runner: runner, Out: &output, Err: &output}
	manager := aur.Manager{Runner: runner, Review: func(name string, files map[string]string) error {
		if name != "browser-bin" || files[".SRCINFO"] != applicationAURSRCINFO {
			t.Fatalf("review did not receive exact pinned source: name=%q files=%#v", name, files)
		}
		return nil
	}}
	if err := runtime.installAURApplication(context.Background(), arch.Manager{Runner: runner}, manager, app); err != nil {
		t.Fatal(err)
	}
	makepkgCalls := 0
	fetchedPinnedSource := false
	for _, call := range runner.calls {
		if call.Name == "paru" {
			t.Fatalf("direct paru application install ran: %#v", call)
		}
		if call.Name != "makepkg" {
			if call.Name == "git" && len(call.Args) >= 8 && call.Args[2] == "fetch" &&
				call.Args[6] == "https://aur.archlinux.org/browser-bin.git" && call.Args[7] == applicationAURCommit {
				fetchedPinnedSource = true
			}
			continue
		}
		makepkgCalls++
		if call.Interactive || call.Stdin == nil {
			t.Fatalf("makepkg was interactive or inherited stdin: %#v", call)
		}
		input, readErr := io.ReadAll(call.Stdin)
		if readErr != nil || len(input) != 0 {
			t.Fatalf("makepkg stdin=%q err=%v", input, readErr)
		}
	}
	if makepkgCalls != 2 {
		t.Fatalf("makepkg calls=%d", makepkgCalls)
	}
	if runner.revisionChecks != 2 {
		t.Fatalf("pinned source revision was not revalidated before build: checks=%d", runner.revisionChecks)
	}
	if !fetchedPinnedSource {
		t.Fatalf("application did not fetch its exact pinned AUR source: %#v", runner.calls)
	}
}

func TestAURApplicationPlanShowsOfficialDependenciesBeforeReviewInstall(t *testing.T) {
	p := plan.Plan{Applications: []plan.Application{{
		Declaration: config.Application{Identifier: "browser-bin", Source: "aur"}, State: "install",
		AURPackages: []plan.BootstrapPackage{{Name: "builder", Purposes: []string{"build"}}, {Name: "runtime", Purposes: []string{"runtime"}}},
	}}}
	var output bytes.Buffer
	(Runtime{Out: &output}).showPlan(p)
	got := output.String()
	for _, row := range []string{
		"browser-bin -> builder  install  pacman; build dependency",
		"browser-bin -> runtime  install  pacman; runtime dependency",
		"browser-bin             install  aur; review required",
	} {
		if !strings.Contains(got, row) {
			t.Fatalf("missing planned AUR work %q:\n%s", row, got)
		}
	}
}

func TestAURApplicationFailureContinuesWithUnrelatedApplications(t *testing.T) {
	p := plan.Plan{Core: readyCore(), Applications: []plan.Application{
		{Declaration: config.Application{Identifier: "broken-bin", Source: "aur"}, State: "install"},
		{Declaration: config.Application{Identifier: "org.example.Working", Source: "flatpak"}, State: "install"},
	}, GitStatus: "ready", SSHStatus: "ready", GitHubStatus: "ready"}
	var output bytes.Buffer
	runner := &prepareRunner{}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &output})
	if code != Issues || !strings.Contains(output.String(), "broken-bin") || !strings.Contains(output.String(), "org.example.Working  install  flatpak") {
		t.Fatalf("code=%d\n%s", code, output.String())
	}
	if got := strings.Join(mutationOrder(runner.calls), ","); got != "application" {
		t.Fatalf("unrelated application did not continue safely: %s", got)
	}
}
