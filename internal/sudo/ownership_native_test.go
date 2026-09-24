//go:build ownership_integration

package sudo

import (
	"context"
	"github.com/luigiverona/ops/internal/testproc"
	"io"
	"os"
	"testing"
	"time"

	"github.com/luigiverona/ops/internal/run"
)

func TestPrivilegedOwnershipKeeper(t *testing.T) {
	if testproc.Supervise(t) {
		return
	}
	if os.Getenv("OPS_OWNERSHIP_PRIVILEGED") != "1" || os.Getuid() != 1000 {
		t.Fatal("explicit privileged CI fixture authorization required")
	}
	owner := run.NewOwner()
	if err := owner.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	testproc.RecordScope(t)
	e := run.Exec{Owner: owner, Out: io.Discard, Err: io.Discard}
	observed := make(chan struct{}, 1)
	keeper, err := acquire(context.Background(), observeKeeper{e, observed}, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer keeper.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = e.Run(ctx, run.Spec{Name: "/usr/bin/sleep", Args: []string{"10"}}) }()
	select {
	case <-observed:
	case <-time.After(5 * time.Second):
		t.Fatal("owned refresh did not complete")
	}
	if err := keeper.Close(); err != nil {
		t.Fatal(err)
	}
	<-done
	if err := owner.Check(); err != nil {
		t.Fatal(err)
	}
}

type observeKeeper struct {
	run.Runner
	refreshed chan struct{}
}

func (r observeKeeper) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	result, err := r.Runner.Run(ctx, s)
	if len(s.Args) == 2 && err == nil {
		select {
		case r.refreshed <- struct{}{}:
		default:
		}
	}
	return result, err
}
