package archrepo_test

import (
	"context"
	"errors"
	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
	"testing"
)

type unavailableRunner struct{ failure error }

func (r unavailableRunner) Run(_ context.Context, s run.Spec) (run.Result, error) {
	return run.Result{Stdout: testpkg.Info("extra/git")}, nil
}
func (r unavailableRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Args: args})
}
func (r unavailableRunner) OfficialInstalledVersion(context.Context, string, string) (bool, error) {
	return false, r.failure
}
func TestUnavailableEvidencePreservesCompoundFailures(t *testing.T) {
	for _, tc := range testpkg.ExpectedExitCases(archtrust.ErrExactVersionUnavailable) {
		t.Run(tc.Name, func(t *testing.T) {
			state, err := archrepo.InspectInstalled(context.Background(), unavailableRunner{tc.Err}, "extra/git")
			if tc.Err == archtrust.ErrExactVersionUnavailable {
				if err != nil || state.Ready() {
					t.Fatalf("missing evidence: %+v %v", state, err)
				}
			} else if !errors.Is(err, tc.Err) || tc.Cause != nil && !errors.Is(err, tc.Cause) {
				t.Fatalf("lost evidence failure: %v", err)
			}
		})
	}
}
