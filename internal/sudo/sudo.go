// Package sudo manages one bounded sudo authorization window.
package sudo

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/luigiverona/ops/internal/run"
)

// Keeper refreshes an existing sudo timestamp without additional prompts.
type Keeper struct {
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	err    error
}

func Acquire(ctx context.Context, runner run.Runner) (*Keeper, error) {
	if _, err := runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"-v"}, Interactive: true}); err != nil {
		return nil, err
	}
	keepCtx, cancel := context.WithCancel(ctx)
	k := &Keeper{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(k.done)
		ticker := time.NewTicker(50 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-keepCtx.Done():
				return
			case <-ticker.C:
				if _, err := runner.Run(keepCtx, run.Spec{Name: "sudo", Args: []string{"-n", "-v"}}); err != nil && !errors.Is(err, context.Canceled) {
					k.mu.Lock()
					if k.err == nil {
						k.err = err
					}
					k.mu.Unlock()
					return
				}
			}
		}
	}()
	return k, nil
}

func (k *Keeper) Close() error {
	k.cancel()
	<-k.done
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.err
}
