package run

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func TestCancellationDuringOwnedCleanup(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "prior-command-failure", "prior-lifecycle-failure", "post-kill"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var expire func()
			if mode == "deadline" {
				// Publish deadline identity only once population observation is entered.
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(context.Background(), time.Now().Add(time.Hour))
				defer stop()
				dc := &cleanupDeadlineContext{Context: ctx, done: make(chan struct{})}
				ctx = dc
				expire = func() { close(dc.done) }
			} else {
				expire = cancel
			}
			failure := errors.New("independent exit")
			lifecycle := errors.New("population observation failed")
			var waitErr error
			if mode == "prior-command-failure" {
				waitErr = failure
			}
			g := &fakeGroup{}
			observations := 0
			g.observe = func() (bool, error) {
				observations++
				if mode == "prior-lifecycle-failure" && observations == 1 {
					return false, lifecycle
				}
				if mode == "post-kill" {
					if g.killed > 0 && observations > 3 {
						expire()
					}
				} else {
					expireOnce := observations == 1 || mode == "prior-lifecycle-failure" && observations == 2
					if expireOnce {
						expire()
					}
				}
				return g.killed > 0, nil
			}
			o := NewOwner()
			// Wait has definitively published success/failure before first Empty.
			err, waited := (Exec{Owner: o}).finishOwnedCommand(ctx, exec.Command("true"), g, nil, waitErr, nil, true, true, nil, false, nil)
			want := context.Canceled
			if mode == "deadline" {
				want = context.DeadlineExceeded
			}
			if mode == "prior-command-failure" {
				if !errors.Is(err, failure) || errors.Is(err, context.Canceled) {
					t.Fatalf("earlier failure lost precedence: %v", err)
				}
			} else if !errors.Is(err, want) {
				t.Fatalf("cleanup lost context: %v", err)
			}
			if mode == "prior-lifecycle-failure" && (!errors.Is(err, lifecycle) || !OwnershipFailed(err)) {
				t.Fatalf("lifecycle failure lost: %v", err)
			}
			if !waited || g.killed != 1 || g.removed != 1 || g.closed != 1 {
				t.Fatalf("incomplete cleanup: %+v", g)
			}
		})
	}
}

// Synchronized context implements the deadline contract without wall-clock sleeps.
type cleanupDeadlineContext struct {
	context.Context
	done chan struct{}
}

func (c *cleanupDeadlineContext) Done() <-chan struct{} { return c.done }
func (c *cleanupDeadlineContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func TestFailurePublicationDuringCleanup(t *testing.T) {
	for _, failureFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(failureFirst), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("published independent failure")
			done := make(chan error, 1)
			var published atomic.Bool
			g := &fakeGroup{}
			observed := false
			g.observe = func() (bool, error) {
				if !observed {
					observed = true
					// Synchronize publication while cleanup is active and before its final
					// receive from done. Reverse the events to prove cancellation still wins.
					if !failureFirst {
						cancel()
					}
					if ctx.Err() == nil {
						published.Store(true)
					}
					done <- failure
					if failureFirst {
						cancel()
					}
				}
				return g.killed > 0, nil
			}
			err, waited := (Exec{Owner: NewOwner()}).finishOwnedCommand(ctx, exec.Command("true"), g, done, nil, nil, true, false, nil, false, &published)
			if !waited || !errors.Is(err, failure) || errors.Is(err, context.Canceled) == failureFirst {
				t.Fatalf("publication ordering lost: %v", err)
			}
		})
	}
}
