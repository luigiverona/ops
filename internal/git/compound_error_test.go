package git

import (
	"context"
	"errors"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
	"testing"
)

type cancelFailureRunner struct {
	cancel context.CancelFunc
	err    error
	calls  int
}

func (r *cancelFailureRunner) Run(context.Context, run.Spec) (run.Result, error) {
	r.calls++
	if r.cancel != nil {
		r.cancel()
	}
	return run.Result{}, r.err
}
func TestMissingIdentityPreservesCompoundFailures(t *testing.T) {
	for _, tc := range testpkg.ExpectedExitCases(testpkg.ExitStatus(1)) {
		r := &cancelFailureRunner{err: tc.Err}
		_, err := (Manager{Runner: r}).Inspect(context.Background())
		if tc.Cause == nil {
			if err != nil || r.calls != 2 {
				t.Fatalf("ordinary missing: %v", err)
			}
		} else if !errors.Is(err, tc.Cause) || r.calls != 1 {
			t.Fatalf("lost failure: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cause := &run.OwnershipError{Err: errors.New("cleanup")}
	r := &cancelFailureRunner{cancel: cancel, err: cause}
	_, err := (Manager{Runner: r}).Inspect(ctx)
	if !errors.Is(err, cause) || !errors.Is(err, context.Canceled) || r.calls != 1 {
		t.Fatalf("context replaced ownership: %v", err)
	}
}
