package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
)

func TestConfigureGitInspectionFailureDoesNotPromptOrMutate(t *testing.T) {
	for _, field := range []string{"user.name", "user.email"} {
		for _, cause := range []error{errors.New("runner unavailable"), context.Canceled} {
			t.Run(field+"/"+cause.Error(), func(t *testing.T) {
				var output bytes.Buffer
				runner := diagnosticRunner(func(_ context.Context, spec run.Spec) (run.Result, error) {
					if spec.Name != "git" || len(spec.Args) != 4 || spec.Args[2] != "--get" {
						t.Fatalf("inspection failure reached mutation: %#v", spec)
					}
					if spec.Args[3] == field {
						return run.Result{}, cause
					}
					return run.Result{}, diagnosticExit(1)
				})
				a := Runtime{Runner: runner, Out: &output, Err: &output, interruption: &interruption{}}
				status, err := a.configureGit(context.Background(), ui.UI{In: strings.NewReader("Test User\nuser@example.invalid\n"), Out: &output})
				if status != "failed" || !errors.Is(err, cause) || output.Len() != 0 || a.interruption.mutation {
					t.Fatalf("status=%s err=%v mutation=%v output=%s", status, err, a.interruption.mutation, &output)
				}
			})
		}
	}
}

func TestGitInspectionFailureFlowsThroughSetupIssue(t *testing.T) {
	var output bytes.Buffer
	base := &prepareRunner{}
	cause := errors.New("Git read unavailable")
	runner := diagnosticRunner(func(ctx context.Context, spec run.Spec) (run.Result, error) {
		if spec.Name == "git" {
			if spec.Args[2] != "--get" {
				t.Fatalf("unexpected Git write: %#v", spec)
			}
			return run.Result{}, cause
		}
		return base.Run(ctx, spec)
	})
	p := plan.Plan{Core: readyCore(), ConfigureGit: true, SSHStatus: "ready", GitHubStatus: "ready"}
	r := (Runtime{Runner: runner, Out: &output, Err: &output}).executePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &output})
	if r.git != "failed" || len(r.problems) != 1 || !errors.Is(r.problems[0].Err, cause) || strings.Contains(output.String(), "Git name:") {
		t.Fatalf("result=%#v output=%s", r, &output)
	}
}

func TestGitReadFailureStopsDoctorAndInitialSetup(t *testing.T) {
	for _, field := range []string{"user.name", "user.email"} {
		for _, doctor := range []bool{false, true} {
			t.Run(field+map[bool]string{true: "/doctor", false: "/setup"}[doctor], func(t *testing.T) {
				a, out, base := noActionPrepareRuntime(t, false)
				a.Runner = diagnosticRunner(func(ctx context.Context, spec run.Spec) (run.Result, error) {
					if spec.Name == "git" {
						if spec.Args[2] != "--get" {
							t.Fatalf("unexpected Git mutation: %#v", spec)
						}
						if spec.Args[3] == field {
							return run.Result{Stderr: "permission denied\n"}, &run.Error{Name: "git", Err: diagnosticExit(1)}
						}
					}
					return base.Run(ctx, spec)
				})
				var code int
				if doctor {
					code = a.Doctor(context.Background())
				} else {
					code = a.Prepare(context.Background())
				}
				if code != Fatal || !strings.Contains(out.String(), "inspect Git "+field) {
					t.Fatalf("code=%d output=%s", code, out)
				}
				for _, wrong := range []string{"Workstation healthy.", "Git: configuration required", "Git name:", "Git email:", "Continue?"} {
					if strings.Contains(out.String(), wrong) {
						t.Fatalf("failure became missing state: %s", out)
					}
				}
			})
		}
	}
}
