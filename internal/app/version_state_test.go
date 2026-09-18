package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
	"github.com/luigiverona/ops/internal/ui"
)

type versionFlowRunner struct {
	*doctorRunner
	version                                string
	prevent, forged, forgeAfter, unknown   bool
	upgrades, reinstalls, inspectionsAfter int
}

func (r *versionFlowRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if s.Name == "vercmp" {
		return (run.Exec{}).Run(ctx, s)
	}
	if s.Name == "sudo" {
		args := strings.Join(s.Args, " ")
		if strings.Contains(args, "pacman -Syu") {
			r.upgrades++
			if !r.prevent {
				r.version = "2-1"
				r.forged = r.forgeAfter
			}
			return run.Result{}, nil
		}
		if strings.Contains(args, "pacman -S ") {
			r.reinstalls++
			return run.Result{}, fmt.Errorf("unexpected provenance reinstall")
		}
		if args == "-v" || args == "-n -v" || args == "-n true" {
			return run.Result{}, nil
		}
		return run.Result{}, fmt.Errorf("unexpected mutation: %s", args)
	}
	result, err := r.doctorRunner.Run(ctx, s)
	if s.Name == "pacman" && s.Args[0] == "-Qi" && s.Args[len(s.Args)-1] == "git" {
		result.Stdout = strings.Replace(result.Stdout, "Version : 1-1", "Version : "+r.version, 1)
	}
	return result, err
}
func (r *versionFlowRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	result, err := r.Run(ctx, run.Spec{Name: "pacman", Args: args})
	if args[0] == "-Si" && args[len(args)-1] == "extra/git" {
		result.Stdout = strings.Replace(result.Stdout, "Version : 1-1", "Version : 2-1", 1)
	}
	return result, err
}
func (r *versionFlowRunner) OfficialInstalledVersion(_ context.Context, target, version string) (bool, error) {
	if r.upgrades > 0 {
		r.inspectionsAfter++
	}
	if target == "extra/git" {
		if version == "1-1" && r.unknown {
			return false, archtrust.ErrExactVersionUnavailable
		}
		return !r.forged, nil
	}
	return true, nil
}

func TestVersionDriftDoctorReadOnly(t *testing.T) {
	for _, mode := range []string{"verified", "inconclusive", "forged"} {
		t.Run(mode, func(t *testing.T) {
			a, out, base := noActionPrepareRuntime(t, false)
			r := &versionFlowRunner{doctorRunner: base, version: "1-1", unknown: mode == "inconclusive", forged: mode == "forged"}
			a.Runner = r
			code := a.Doctor(context.Background())
			if code != Issues || r.upgrades != 0 || r.reinstalls != 0 {
				t.Fatalf("%d %s", code, out)
			}
			if mode == "forged" {
				if !strings.Contains(out.String(), "content requires repair") {
					t.Fatal(out.String())
				}
				return
			}
			if !strings.Contains(out.String(), "update available") || strings.Contains(out.String(), "repair") {
				t.Fatal(out.String())
			}
			if mode == "inconclusive" && !strings.Contains(out.String(), "authenticity could not be established") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestFullUpgradeReinspectsVersionAndSkipsObsoleteRepair(t *testing.T) {
	for _, mode := range []string{"updated", "prevented", "forged after", "repair resolved"} {
		t.Run(mode, func(t *testing.T) {
			a, out, base := noActionPrepareRuntime(t, false)
			r := &versionFlowRunner{doctorRunner: base, version: "1-1", prevent: mode == "prevented", forgeAfter: mode == "forged after", forged: mode == "repair resolved"}
			a.Runner = r
			state, err := a.inspectState(context.Background(), config.Config{})
			if err != nil {
				t.Fatal(err)
			}
			p := plan.Build(config.Config{}, state, nil)
			// The forged control gates git configuration discovery. This test is about
			// package reconciliation, so retain its already configured user identity.
			p.ConfigureGit = false
			p.GitStatus = "ready"
			if !p.FullUpgrade {
				t.Fatal("update not planned")
			}
			code := a.preparePlan(context.Background(), config.Config{}, p, ui.UI{In: strings.NewReader("y\n"), Out: out})
			if r.upgrades != 1 || r.reinstalls != 0 || r.inspectionsAfter == 0 {
				t.Fatalf("missing reinspection or redundant repair: %+v %s", r, out)
			}
			if mode == "updated" || mode == "repair resolved" {
				if code != Success {
					t.Fatalf("%d %s", code, out)
				}
				state, err = a.inspectState(context.Background(), config.Config{})
				if err != nil {
					t.Fatal(err)
				}
				again := plan.Build(config.Config{}, state, nil)
				if again.FullUpgrade || len(again.CorePackages) != 0 || !state.OfficialStates["git"].Ready() {
					t.Fatalf("not idempotent %+v", again)
				}
			} else {
				if code == Success {
					t.Fatal("bad final state accepted")
				}
				if mode == "prevented" && (!strings.Contains(out.String(), "update available") || strings.Contains(out.String(), "content requires repair")) {
					t.Fatal(out.String())
				}
				if mode == "forged after" && !strings.Contains(out.String(), "authenticated official") {
					t.Fatal(out.String())
				}
			}
		})
	}
}

func TestNewerCoreDoesNotAuthorizeDowngrade(t *testing.T) {
	a, out, _ := noActionPrepareRuntime(t, false)
	p := plan.Plan{Core: readyCore(), CoreOfficial: map[string]archrepo.InstalledState{"git": {Target: "extra/git", Authenticity: archrepo.VerifiedOfficial, Currency: archrepo.NewerThanCurrent}}, GitStatus: "ready", SSHStatus: "ready", GitHubStatus: "ready"}
	if code := a.executeForTest(context.Background(), p, ui.UI{}); code != Issues || !strings.Contains(out.String(), "no automatic downgrade") {
		t.Fatalf("%d %s", code, out)
	}
}

func TestFinalRetainedAURBindingReportsVersionDrift(t *testing.T) {
	f := testpkg.NewPacmanFixture(t)
	f.Sync(t, "custom")
	f.Sync(t, "core")
	old := testpkg.FixturePackage{Name: "builder", Version: "1-1", Packager: "Official", Payload: "old", Provides: "compiler=1", Depends: "library"}
	library := testpkg.FixturePackage{Name: "library", Version: "1-1", Packager: "Official", Payload: "library"}
	f.Sync(t, "extra", old, library)
	f.Local(t, old)
	f.Local(t, library)
	next := library
	next.Version = "2-1"
	next.Payload = "next"
	f.Sync(t, "extra", old, next)
	declaration := config.Application{Source: config.AUR, Identifier: "example"}
	cfg := config.Config{Applications: []config.Application{declaration}}
	approved := plan.Plan{Applications: []plan.Application{{Declaration: declaration, AURDependencies: []plan.OfficialDependency{{Requirement: "compiler>=1", Provider: "extra/builder", Packages: []string{"extra/builder", "extra/library"}, Satisfied: true}}}}}
	observed := plan.State{Installed: map[string]bool{"example": true}, Foreign: map[string]bool{"example": true}}
	err := (Runtime{Runner: f}).verifyFinalAURBindings(context.Background(), cfg, observed, approved)
	if err == nil || !strings.Contains(err.Error(), "update available") || strings.Contains(err.Error(), "repair") {
		t.Fatalf("retained version drift: %v", err)
	}
}
