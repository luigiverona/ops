package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/resolve"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
)

func identityFatalCauses() map[string]error {
	ownership := &run.OwnershipError{Err: errors.New("original identity cleanup failure")}
	return map[string]error{
		"cancel":            context.Canceled,
		"deadline":          context.DeadlineExceeded,
		"wrapped cancel":    fmt.Errorf("agent: %w", context.Canceled),
		"wrapped deadline":  fmt.Errorf("agent: %w", context.DeadlineExceeded),
		"ownership":         ownership,
		"wrapped ownership": fmt.Errorf("agent: %w", ownership),
	}
}

// Deliberately do not poison the owner or cancel the parent. Only the returned
// error can stop orchestration; every later runner admission is counted.
type identityFailureRunner struct {
	base               run.Runner
	match              func(run.Spec) bool
	cause              error
	failed             bool
	later, laterGitHub int
}

func (r *identityFailureRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if r.failed {
		r.later++
		if s.Name == "gh" {
			r.laterGitHub++
		}
	}
	if r.match(s) {
		r.failed = true
		return run.Result{}, r.cause
	}
	return r.base.Run(ctx, s)
}

func agentListing(s run.Spec) bool {
	return s.Name == "ssh-add" && strings.Join(s.Args, " ") == "-L"
}

func assertIdentityStop(t *testing.T, r *identityFailureRunner, ctx context.Context) {
	t.Helper()
	if !r.failed || r.later != 0 || r.laterGitHub != 0 || ctx.Err() != nil {
		t.Fatalf("failed=%v later=%d later-GitHub=%d parent=%v", r.failed, r.later, r.laterGitHub, ctx.Err())
	}
}

func TestSSHAgentFatalStopsReconciliation(t *testing.T) {
	for name, cause := range identityFatalCauses() {
		for _, prior := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/prior=%v", name, prior), func(t *testing.T) {
				a, out, base := noActionPrepareRuntime(t, false)
				before := readDoctorSSHFiles(t, filepath.Join(a.Home, ".ssh"))
				owner := &gateOwner{}
				a.Ownership = owner
				git := &prepareRunner{home: a.Home}
				r := &identityFailureRunner{cause: cause, match: agentListing}
				r.base = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
					if s.Name == "sudo" || s.Interactive {
						t.Fatalf("unexpected mutation: %+v", s)
					}
					if prior && s.Name == "git" {
						return git.Run(ctx, s)
					}
					return base.Run(ctx, s)
				})
				a.Runner = diagnosticRunner(r.Run)
				ctx := context.Background()
				p := plan.Plan{ConfigureGit: prior, ReviewSSHAgent: true, GitStatus: "ready", SSHStatus: "ready", GitHubStatus: "ready"}
				terminal := func() ui.UI { return ui.UI{In: strings.NewReader("y\nUser\nuser@example.com\n"), Out: out} }
				// identity.go retains the cause even while carrying it as an Issue.
				_, _, issues, err := a.configureSSH(ctx, terminal(), p)
				if err != nil || len(issues) != 1 || !errors.Is(issues[0].Err, cause) {
					t.Fatalf("identity cause: issues=%+v err=%v", issues, err)
				}
				assertIdentityStop(t, r, ctx)
				r.failed = false
				result := a.executePlan(ctx, p, terminal())
				if result.status != Fatal || !result.stopInspection || result.applied != prior || len(result.problems) == 0 || !errors.Is(result.problems[len(result.problems)-1].Err, cause) {
					t.Fatalf("lost fatal cause: %+v", result)
				}
				if !resolve.StopsPlanning(result.problems[len(result.problems)-1].Err) {
					t.Fatal("lost interruption classification")
				}
				assertIdentityStop(t, r, ctx)
				r.failed = false
				git.gitName, git.gitEmail = "", ""
				out.Reset()
				code := a.preparePlan(ctx, config.Config{}, p, terminal())
				t.Logf("code=%d later=%d later-GitHub=%d", code, r.later, r.laterGitHub)
				assertIdentityStop(t, r, ctx)
				if code != Fatal || !strings.Contains(out.String(), cause.Error()) || strings.Contains(out.String(), "Earlier changes may remain") != prior {
					t.Fatalf("code=%d output=%s", code, out)
				}
				if prior && (git.gitName != "User" || git.gitEmail != "user@example.com") {
					t.Fatal("earlier successful changes lost")
				}
				if !reflect.DeepEqual(before, readDoctorSSHFiles(t, filepath.Join(a.Home, ".ssh"))) || owner.poison != nil {
					t.Fatal("SSH files changed or test relied on owner poison")
				}
			})
		}
	}
}

func TestSSHAgentFatalStopsPrepareInspection(t *testing.T) {
	for name, cause := range identityFatalCauses() {
		t.Run(name, func(t *testing.T) {
			a, out, base := noActionPrepareRuntime(t, false)
			before := readDoctorSSHFiles(t, filepath.Join(a.Home, ".ssh"))
			owner := &gateOwner{}
			a.Ownership = owner
			r := &identityFailureRunner{cause: cause, match: agentListing}
			r.base = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				if s.Name == "sudo" || s.Interactive {
					t.Fatalf("unexpected mutation: %+v", s)
				}
				// An incomplete effective configuration makes agent inspection relevant.
				if s.Name == "ssh" && s.Args[0] == "-G" {
					return run.Result{}, nil
				}
				return base.Run(ctx, s)
			})
			a.Runner = diagnosticRunner(r.Run)
			ctx := context.Background()
			_, err := a.inspectState(ctx, config.Config{})
			if !errors.Is(err, cause) {
				t.Fatalf("lost cause: %v", err)
			}
			assertIdentityStop(t, r, ctx)
			r.failed = false
			if code := a.Prepare(ctx); code != Fatal || owner.activations != 0 || strings.Contains(out.String(), "Continue?") {
				t.Fatalf("code=%d activations=%d output=%s", code, owner.activations, out)
			}
			assertIdentityStop(t, r, ctx)
			if !reflect.DeepEqual(before, readDoctorSSHFiles(t, filepath.Join(a.Home, ".ssh"))) {
				t.Fatal("SSH files changed")
			}
		})
	}
}

func TestSSHAgentFatalDuringFinalReinspection(t *testing.T) {
	for name, cause := range identityFatalCauses() {
		t.Run(name, func(t *testing.T) {
			a, out, base := noActionPrepareRuntime(t, false)
			git := &prepareRunner{home: a.Home}
			r := &identityFailureRunner{cause: cause, match: agentListing}
			r.base = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				if s.Name == "sudo" || s.Interactive {
					t.Fatalf("unexpected mutation: %+v", s)
				}
				if s.Name == "git" {
					return git.Run(ctx, s)
				}
				if s.Name == "ssh" && s.Args[0] == "-G" {
					return run.Result{}, nil
				}
				return base.Run(ctx, s)
			})
			a.Runner = diagnosticRunner(r.Run)
			ctx := context.Background()
			p := plan.Plan{ConfigureGit: true, SSHStatus: "ready", GitHubStatus: "ready"}
			code := a.preparePlan(ctx, config.Config{}, p, ui.UI{In: strings.NewReader("y\nUser\nuser@example.com\n"), Out: out})
			assertIdentityStop(t, r, ctx)
			if code != Fatal || git.gitName != "User" || git.gitEmail != "user@example.com" || !strings.Contains(out.String(), cause.Error()) || !strings.Contains(out.String(), "Earlier changes may remain") {
				t.Fatalf("code=%d output=%s", code, out)
			}
		})
	}
}

func TestSSHAgentOrdinaryReconciliation(t *testing.T) {
	ordinary := errors.New("ordinary agent protocol failure")
	for _, tc := range []struct {
		name   string
		result run.Result
		err    error
		want   int
	}{
		{"empty", run.Result{Stdout: "The agent has no identities.\n"}, fixtureExit(1), Success},
		{"unavailable", run.Result{}, fixtureExit(2), Success},
		{"ordinary problem", run.Result{}, ordinary, Issues},
		{"malformed", run.Result{Stdout: "invalid\nssh-ed25519 !bad-base64\n"}, nil, Success},
		{"valid", run.Result{Stdout: wirePublic(13) + "\n"}, nil, Success},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, out, base := noActionPrepareRuntime(t, false)
			listed, laterGitHub := false, 0
			a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				if agentListing(s) {
					listed = true
					return tc.result, tc.err
				}
				if listed && s.Name == "gh" {
					laterGitHub++
				}
				if s.Name == "ssh" && len(s.Args) > 0 && s.Args[0] == "-o" {
					return run.Result{Stderr: "successfully authenticated"}, fixtureExit(1)
				}
				if s.Name == "sudo" || s.Interactive {
					t.Fatalf("unexpected mutation: %+v", s)
				}
				return base.Run(ctx, s)
			})
			p := plan.Plan{ReviewSSHAgent: true, GitStatus: "ready", SSHStatus: "ready", GitHubStatus: "ready"}
			result := a.executePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: out})
			if result.stopInspection || result.status == Fatal || !listed {
				t.Fatalf("ordinary state became fatal: %+v", result)
			}
			if tc.want == Issues && (len(result.problems) != 1 || !errors.Is(result.problems[0].Err, ordinary)) {
				t.Fatalf("lost Issue: %+v", result)
			}
			out.Reset()
			code := a.preparePlan(context.Background(), config.Config{}, p, ui.UI{In: strings.NewReader("y\n"), Out: out})
			if code != tc.want || laterGitHub != 2 {
				t.Fatalf("code=%d later-GitHub=%d output=%s", code, laterGitHub, out)
			}
		})
	}
}

func TestDoctorSkipsSSHAgentAndStopsLocalIdentityFailure(t *testing.T) {
	for name, cause := range identityFatalCauses() {
		t.Run(name, func(t *testing.T) {
			a, out, base := noActionPrepareRuntime(t, false)
			owner := &gateOwner{}
			a.Ownership = owner
			before := readDoctorSSHFiles(t, filepath.Join(a.Home, ".ssh"))
			r := &identityFailureRunner{base: base, match: agentListing, cause: cause}
			a.Runner = diagnosticRunner(r.Run)
			ctx := context.Background()
			if code := a.Doctor(ctx); code != Success || r.failed {
				t.Fatalf("Doctor queried agent: code=%d output=%s", code, out)
			}
			r.match = func(s run.Spec) bool { return s.Name == "ssh-keygen" }
			out.Reset()
			if code := a.Doctor(ctx); code != Fatal || owner.activations != 0 || !strings.Contains(out.String(), cause.Error()) {
				t.Fatalf("code=%d output=%s", code, out)
			}
			assertIdentityStop(t, r, ctx)
			for _, s := range base.calls {
				if s.Name == "sudo" || s.Interactive {
					t.Fatalf("unexpected mutation: %+v", s)
				}
			}
			if !reflect.DeepEqual(before, readDoctorSSHFiles(t, filepath.Join(a.Home, ".ssh"))) {
				t.Fatal("Doctor changed files")
			}
		})
	}
}

func TestAdjacentIdentityAggregationStopsFatalCause(t *testing.T) {
	for name, cause := range identityFatalCauses() {
		for _, point := range []string{"git", "SSH discovery", "GitHub authentication", "GitHub keys", "GitHub verification", "verification after SSH"} {
			t.Run(name+"/"+point, func(t *testing.T) {
				a, out, base := noActionPrepareRuntime(t, false)
				p := plan.Plan{ConfigureGit: true, ReviewSSHAgent: true, AuthenticateGitHub: true, GitStatus: "ready", SSHStatus: "ready", GitHubStatus: "ready"}
				if point == "verification after SSH" {
					p.AuthenticateGitHub = false
				}
				r := &identityFailureRunner{cause: cause, match: func(s run.Spec) bool {
					switch point {
					case "git":
						return s.Name == "git"
					case "SSH discovery":
						return s.Name == "ssh-keygen"
					case "GitHub authentication":
						return s.Name == "gh" && s.Args[0] == "auth"
					case "GitHub keys":
						return s.Name == "gh" && s.Args[0] == "api"
					default:
						return s.Name == "ssh" && s.Args[0] == "-o"
					}
				}}
				r.base = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
					if agentListing(s) {
						return run.Result{Stdout: base.managedKey + "\n"}, nil
					}
					if s.Name == "sudo" || s.Interactive {
						t.Fatalf("unexpected mutation: %+v", s)
					}
					return base.Run(ctx, s)
				})
				a.Runner = diagnosticRunner(r.Run)
				ctx := context.Background()
				result := a.executePlan(ctx, p, ui.UI{In: strings.NewReader("y\n"), Out: out})
				if result.status != Fatal || !result.stopInspection || len(result.problems) == 0 || !errors.Is(result.problems[len(result.problems)-1].Err, cause) {
					t.Fatalf("lost fatal cause: %+v", result)
				}
				assertIdentityStop(t, r, ctx)
				r.failed = false
				out.Reset()
				if code := a.preparePlan(ctx, config.Config{}, p, ui.UI{In: strings.NewReader("y\n"), Out: out}); code != Fatal {
					t.Fatalf("code=%d output=%s", code, out)
				}
				assertIdentityStop(t, r, ctx)
			})
		}
	}
}
