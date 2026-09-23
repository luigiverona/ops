package inspect

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

type serviceCorrectionRunner struct {
	stateRunner
	operation string
	output    string
	failure   error
	failed    bool
	later     int
}

func (r *serviceCorrectionRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if r.failed {
		r.later++
	}
	if s.Name == "systemctl" {
		if s.Args[0] == r.operation {
			r.failed = r.failure != nil
			return run.Result{Stdout: r.output}, r.failure
		}
		return run.Result{Stdout: "enabled\n"}, nil
	}
	if s.Name == "pacman" && s.Args[0] == "-Qqm" && r.operation == "foreign" {
		r.failed = r.failure != nil
		return run.Result{}, r.failure
	}
	result, err := r.stateRunner.Run(ctx, s)
	if s.Name == "pacman" && s.Args[0] == "-Sl" {
		result.Stdout += "extra mullvad-vpn 1-1\n"
	}
	if s.Name == "pacman" && (s.Args[0] == "-Qq" || s.Args[0] == "-Qeq") {
		result.Stdout += "mullvad-vpn\n"
	}
	return result, err
}
func (r *serviceCorrectionRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args})
}
func (r *serviceCorrectionRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}

func TestExpectedInspectionExitPreservesFailures(t *testing.T) {
	original := &run.OwnershipError{Err: errors.New("service cleanup failed")}
	for _, op := range []struct {
		name, output string
		code         int
	}{
		{"is-active", "inactive\n", 3}, {"is-active", "unknown\n", 4},
		{"is-enabled", "disabled\n", 1}, {"is-enabled", "not-found\n", 4}, {"foreign", "", 1},
	} {
		for _, tc := range []struct {
			name       string
			extra      error
			unexpected bool
		}{
			{"expected", nil, false}, {"ownership", original, false}, {"cancel", context.Canceled, false},
			{"deadline", context.DeadlineExceeded, false}, {"joined-unexpected", errors.New("inspection incomplete"), false},
			{"unexpected-exit", nil, true},
		} {
			t.Run(fmt.Sprintf("%s-%d/%s", op.name, op.code, tc.name), func(t *testing.T) {
				var cause error = gitStateExit(op.code)
				if tc.unexpected {
					cause = gitStateExit(9)
				}
				if tc.extra != nil {
					cause = errors.Join(cause, tc.extra)
				}
				failure := &run.Error{Name: "systemctl", Err: fmt.Errorf("wrapped: %w", cause)}
				r := &serviceCorrectionRunner{operation: op.name, output: op.output, failure: failure}
				w := Workstation{Runner: r, Home: t.TempDir(), PacmanConf: testPacmanConf(t), SkipAgent: true, Applications: []config.Application{{Source: config.Pacman, Identifier: "mullvad-vpn"}}}
				state, err := w.State(context.Background())
				if tc.extra == nil && !tc.unexpected {
					if err != nil || !r.failed || state.Services["mullvad-daemon.service"] {
						t.Fatalf("expected inactive: %v %+v", err, state.Services)
					}
				} else {
					if !errors.Is(err, failure) || tc.extra != nil && !errors.Is(err, tc.extra) || r.later != 0 {
						t.Fatalf("lost failure: %v later=%d", err, r.later)
					}
					if tc.extra == original && !run.OwnershipFailed(err) {
						t.Fatal("lost ownership classification", err)
					}
				}
			})
		}
	}
}
