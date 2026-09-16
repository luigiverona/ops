package app

import (
	"context"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
	"github.com/luigiverona/ops/internal/ui"
)

func TestReviewFinalInspectionRevalidatesAURBindings(t *testing.T) {
	for _, drift := range []string{"", "repository", "addition", "unsatisfied", "payload"} {
		t.Run(drift, func(t *testing.T) {
			a, out, local := noActionPrepareRuntime(t, false)
			p := declaredParuPlan(t)
			cfg := config.Config{Applications: []config.Application{p.Applications[0].Declaration}}
			ar := &aurOrderRunner{output: out}
			final, checked := false, false
			a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				if s.Name == "pacman" {
					switch s.Args[0] {
					case "-Qq", "-Qeq":
						final = true
						result, err := local.Run(ctx, s)
						if ar.artifactInstalled {
							result.Stdout += "paru\n"
						}
						return result, err
					case "-Qqm":
						if ar.artifactInstalled {
							return run.Result{Stdout: "paru\n"}, nil
						}
						return run.Result{}, nil
					case "-T", "-Sp", "-Qm", "-Qe":
						result, err := ar.Run(ctx, s)
						if final {
							checked = true
							if s.Args[0] == "-Sp" && drift == "repository" {
								result.Stdout = strings.ReplaceAll(result.Stdout, "extra/rust", "core/rust")
							}
							if s.Args[0] == "-Sp" && drift == "addition" {
								result.Stdout += "extra/new-member\t\n"
							}
							if s.Args[0] == "-T" && drift == "unsatisfied" {
								return run.Result{Stdout: s.Args[len(s.Args)-1] + "\n"}, &run.Error{Name: "pacman", Err: diagnosticExit(127)}
							}
						}
						return result, err
					}
				}
				if s.Name == "sudo" || s.Name == "makepkg" || s.Name == "git" && (s.Args[0] == "init" || s.Args[0] == "-C") {
					return ar.Run(ctx, s)
				}
				return local.Run(ctx, s)
			})
			if drift == "payload" {
				a.Runner = finalContentRunner{diagnosticRunner: a.Runner.(diagnosticRunner), final: &final}
			}
			code := a.preparePlan(context.Background(), cfg, p, ui.UI{In: strings.NewReader("y\n\ny\n"), Out: out})
			if !ar.artifactInstalled || !final {
				t.Fatalf("fixture did not build and reinspect: %d %s", code, out)
			}
			if !checked {
				t.Error("D-R7: final inspection dropped the approved AUR provider bindings")
			}
			if drift == "" && code != Success {
				t.Fatalf("unchanged binding failed: %d %s", code, out)
			}
			if drift != "" && (code == Success || strings.Contains(out.String(), "Workstation ready.")) {
				t.Errorf("D-R7: final %s drift reported ready: %d %s", drift, code, out)
			}
		})
	}
}

type finalContentRunner struct {
	diagnosticRunner
	final *bool
}

func (r finalContentRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	if *r.final && target == "extra/rust" {
		return false, nil
	}
	return testpkg.FakeContent(ctx, r, target)
}
