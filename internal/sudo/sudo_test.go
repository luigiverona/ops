package sudo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/luigiverona/ops/internal/run"
)

type keeperRunner struct {
	started chan struct{}
	failure error
}

func (r keeperRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if len(s.Args) == 1 {
		return run.Result{}, nil
	}
	close(r.started)
	<-ctx.Done()
	return run.Result{}, errors.Join(ctx.Err(), r.failure)
}
func TestKeeperCloseWaitsForOwnedRefreshAndRetainsCleanupFailure(t *testing.T) {
	for _, failure := range []error{nil, &run.OwnershipError{Err: errors.New("cleanup incomplete")}} {
		started := make(chan struct{})
		keeper, err := acquire(context.Background(), keeperRunner{started, failure}, time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("refresh did not start")
		}
		if err := keeper.Close(); run.OwnershipFailed(err) != (failure != nil) {
			t.Fatal("cleanup error lost", err)
		}
		if err := keeper.Close(); run.OwnershipFailed(err) != (failure != nil) {
			t.Fatal("repeated Close changed result", err)
		}
	}
}
