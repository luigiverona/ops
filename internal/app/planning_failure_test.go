package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/resolve"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
)

func TestPlanningFailureStopsDoctorAndPrepare(t *testing.T) {
	for _, mode := range []string{"doctor", "prepare"} {
		for _, cause := range []error{&run.OwnershipError{Err: errors.New("original application cleanup failure")}, context.Canceled, context.DeadlineExceeded} {
			t.Run(mode+"/"+cause.Error(), func(t *testing.T) {
				a, out, base := noActionPrepareRuntime(t, false)
				data := []byte("version=2\npacman=[\"example\",\"later\"]\n")
				if err := os.WriteFile(config.Path(a.Home), data, 0600); err != nil {
					t.Fatal(err)
				}
				owner := &gateOwner{}
				a.Ownership = owner
				calls, failedAt, later := 0, 0, 0
				a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
					calls++
					if failedAt != 0 {
						later++
						return run.Result{}, &run.OwnershipError{Err: errors.New("later poisoned admission")}
					}
					if s.Name == "pacman" && s.Args[0] == "-Si" && s.Args[len(s.Args)-1] == "example" {
						failedAt = calls
						owner.poison = cause
						return run.Result{}, cause
					}
					if s.Name == "sudo" || s.Interactive {
						t.Fatalf("planning requested mutation/approval: %+v", s)
					}
					return base.Run(ctx, s)
				})
				// The resolution boundary must return the original cause, not a stored Problem.
				cfg := config.Config{Applications: []config.Application{{Source: config.Pacman, Identifier: "example"}, {Source: config.Pacman, Identifier: "later"}}}
				facts, err := resolve.Applications(context.Background(), cfg, plan.State{}, resolve.Resolver{Runner: a.Runner})
				if !errors.Is(err, cause) || facts != nil || calls != 1 {
					t.Fatalf("resolution err=%v facts=%v calls=%d", err, facts, calls)
				}
				calls, failedAt, later, owner.poison = 0, 0, 0, nil
				var code int
				if mode == "doctor" {
					code = a.Doctor(context.Background())
				} else {
					code = a.Prepare(context.Background())
				}
				t.Logf("%s calls=%d failure-at=%d later=%d", mode, calls, failedAt, later)
				if code != Fatal || failedAt == 0 || later != 0 || calls != failedAt || owner.activations != 0 {
					t.Fatalf("code=%d calls=%d failure=%d later=%d output=%s", code, calls, failedAt, later, out)
				}
				if !strings.Contains(out.String(), cause.Error()) || strings.Contains(out.String(), "later poisoned admission") || strings.Contains(out.String(), "Continue?") || strings.Contains(out.String(), "Earlier changes") {
					t.Fatal(out.String())
				}
				got, err := os.ReadFile(config.Path(a.Home))
				if err != nil || string(got) != string(data) {
					t.Fatal("declarations changed", err)
				}
			})
		}
	}
}

func TestFinalAURResolutionFailureAfterEarlierMutation(t *testing.T) {
	a, out, base := noActionPrepareRuntime(t, false)
	gitRunner := &prepareRunner{home: a.Home}
	original := &run.OwnershipError{Err: errors.New("original final dependency cleanup failure")}
	owner := &gateOwner{}
	a.Ownership = owner
	failed, later := false, 0
	a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
		if failed {
			later++
			return run.Result{}, original
		}
		if s.Name == "pacman" && s.Args[0] == "-T" {
			failed = true
			owner.poison = original
			return run.Result{}, original
		}
		if s.Name == "sudo" {
			t.Fatalf("unexpected sudo: %+v", s)
		}
		if s.Name == "git" {
			return gitRunner.Run(ctx, s)
		}
		result, err := base.Run(ctx, s)
		if s.Name == "pacman" && (s.Args[0] == "-Qq" || s.Args[0] == "-Qqm" || s.Args[0] == "-Qeq") {
			result.Stdout += "paru\nlater\n"
		}
		return result, err
	})
	cfg := config.Config{Applications: []config.Application{{Source: config.AUR, Identifier: "paru"}, {Source: config.AUR, Identifier: "later"}}}
	p := plan.Plan{ConfigureGit: true, SSHStatus: "ready", GitHubStatus: "ready"}
	for _, d := range cfg.Applications {
		p.Applications = append(p.Applications, plan.Application{Declaration: d, State: plan.Ready, AURDependencies: []plan.OfficialDependency{{Requirement: "base-devel"}, {Requirement: "later"}}})
	}
	// Check the exact final-resolution boundary separately for cause identity.
	_, err := a.inspectState(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	observed := plan.State{Installed: map[string]bool{"paru": true, "later": true}, Foreign: map[string]bool{"paru": true, "later": true}, Explicit: map[string]bool{"paru": true, "later": true}}
	err = a.verifyFinalAURBindings(context.Background(), cfg, observed, p)
	if !errors.Is(err, original) || later != 0 || !failed {
		t.Fatalf("err=%v failed=%v later=%d", err, failed, later)
	}
	failed, later, owner.poison = false, 0, nil
	gitRunner.gitName, gitRunner.gitEmail = "", ""
	code := a.preparePlan(context.Background(), cfg, p, ui.UI{In: strings.NewReader("y\nUser\nuser@example.com\n"), Out: out})
	if code != Fatal || !failed || later != 0 || gitRunner.gitName != "User" || gitRunner.gitEmail != "user@example.com" {
		t.Fatalf("code=%d failed=%v later=%d output=%s", code, failed, later, out)
	}
	if !strings.Contains(out.String(), original.Error()) || !strings.Contains(out.String(), "Earlier changes may remain") {
		t.Fatal(out.String())
	}
}

func TestApplicationReinspectionStopsOnReturnedInterruption(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, &run.OwnershipError{Err: errors.New("original reinspection cleanup failure")}} {
		t.Run(cause.Error(), func(t *testing.T) {
			base := &prepareRunner{}
			failed, later := false, 0
			runner := diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				if failed {
					later++
					return run.Result{}, errors.New("unexpected later command")
				}
				if s.Name == "pacman" && s.Args[0] == "-Qm" {
					failed = true
					return run.Result{}, cause
				}
				return base.Run(ctx, s)
			})
			var out strings.Builder
			a := Runtime{Ownership: testOwnership{}, Runner: runner, Out: &out, Err: &out}
			p := plan.Plan{Applications: []plan.Application{
				{Declaration: config.Application{Source: config.AUR, Identifier: "first"}, State: plan.Configure},
				{Declaration: config.Application{Source: config.Flatpak, Identifier: "org.example.Later"}, State: plan.Install},
			}}
			result := a.executePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &out})
			if result.status != Fatal || !result.stopInspection || !failed || later != 0 || len(result.problems) == 0 || !errors.Is(result.problems[len(result.problems)-1].Err, cause) {
				t.Fatalf("result=%+v failed=%v later=%d output=%s", result, failed, later, &out)
			}
			for _, s := range base.calls {
				if s.Name == "sudo" && len(s.Args) > 1 && s.Args[1] == "pacman" {
					t.Fatal("reinspection failure allowed package mutation", s)
				}
			}
		})
	}
}
