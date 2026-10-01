package run

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	ownershipGrace          = 2 * time.Second
	ownershipPopulationWait = 2 * time.Second
	ownershipWaitDelay      = 5 * time.Second
	ownershipPoll           = 10 * time.Millisecond
	ownershipActivationWait = 10 * time.Second
	ownershipExitSettle     = 50 * time.Millisecond
)

// OwnershipError is fatal to the entire mutation lifecycle, including in-process
// writes. It intentionally contains no command arguments or captured output.
type OwnershipError struct{ Err error }

func (e *OwnershipError) Error() string {
	return "safe subprocess ownership unavailable or compromised: " + e.Err.Error()
}
func (e *OwnershipError) Unwrap() error { return e.Err }
func OwnershipFailed(err error) bool    { var e *OwnershipError; return errors.As(err, &e) }

type commandGroup interface {
	FD() int
	Empty() (bool, error)
	Kill() error
	Remove() error
	Close() error
}
type ownedScope interface {
	Check() error
	New(string) (commandGroup, error)
}

// Owner is shared by all copies of Exec. Activation is lazy and irreversible;
// a failed activation or cleanup permanently closes admission for this process.
// Create one Owner per ops process, before making copies of its Runner.
type Owner struct {
	mu       sync.Mutex
	scope    ownedScope
	poison   error
	next     uint64
	activate func(context.Context) (ownedScope, error)
	probe    func(context.Context, commandGroup) error
}

func NewOwner() *Owner { return &Owner{activate: activateScope, probe: probePlacement} }

func (o *Owner) failLocked(err error) error {
	if err != nil && o.poison == nil {
		o.poison = &OwnershipError{Err: err}
	}
	return o.poison
}
func (o *Owner) fail(err error) error { o.mu.Lock(); defer o.mu.Unlock(); return o.failLocked(err) }

func (o *Owner) Activate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if o == nil {
		return &OwnershipError{errors.New("no ownership controller")}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.poison != nil {
		return o.poison
	}
	if o.scope != nil {
		return o.checkLocked()
	}
	ctx, cancel := context.WithTimeout(ctx, ownershipActivationWait)
	defer cancel()
	scope, err := o.activate(ctx)
	if err != nil {
		return o.failLocked(err)
	}
	o.scope = scope
	if err := o.checkLocked(); err != nil {
		return err
	}
	// Test the actual interfaces and removal, rather than a version or xattr.
	g, err := o.newLocked()
	if err != nil {
		return err
	}
	empty, err := g.Empty()
	if err == nil && !empty {
		err = errors.New("capability probe unexpectedly populated")
	}
	if err == nil {
		err = g.Kill()
	}
	if err == nil {
		err = g.Remove()
	}
	err = errors.Join(err, g.Close())
	if err != nil {
		return o.failLocked(err)
	}
	// Probe CLONE_INTO_CGROUP itself before sudo or mutation, using an inert
	// fixed executable. No fallback if the kernel/LSM/seccomp denies placement.
	g, err = o.newLocked()
	if err != nil {
		return err
	}
	err = o.probe(ctx, g)
	empty, observeErr := waitEmpty(g, ownershipPopulationWait)
	if observeErr == nil && !empty {
		observeErr = errors.New("capability command left a populated cgroup")
	}
	if observeErr == nil {
		observeErr = g.Remove()
	}
	err = compose(err, observeErr, g.Close(), ctx.Err())
	if err != nil {
		return o.failLocked(err)
	}
	return nil
}

func (o *Owner) checkLocked() error {
	if o.poison != nil {
		return o.poison
	}
	if o.scope == nil {
		return &OwnershipError{errors.New("mutation requires active delegated ownership")}
	}
	return o.failLocked(o.scope.Check())
}

// Check is the defense-in-depth gate for in-process mutation.
func (o *Owner) Check() error {
	if o == nil {
		return &OwnershipError{errors.New("mutation requires an ownership controller")}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.checkLocked()
}

func (o *Owner) newLocked() (commandGroup, error) {
	o.next++
	g, err := o.scope.New(fmt.Sprintf("command-%016x", o.next))
	if err != nil {
		return nil, o.failLocked(err)
	}
	return g, nil
}

// start serializes admission with poisoning and activation. Inactive owners are
// the explicit pre-approval/read-only path. There is no fallback after activation.
func (o *Owner) start(ctx context.Context, cmd *exec.Cmd, pidfd *int) (commandGroup, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.poison != nil {
		return nil, o.poison
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o.scope == nil {
		return nil, cmd.Start()
	}
	if err := o.checkLocked(); err != nil {
		return nil, err
	}
	g, err := o.newLocked()
	if err != nil {
		return nil, err
	}
	// Owned cancellation is driven by the lifetime controller, not os/exec.
	cmd.Cancel = nil
	placeCommand(cmd, g.FD(), pidfd)
	if err = ctx.Err(); err == nil {
		err = cmd.Start()
	}
	if err != nil {
		empty, cleanErr := g.Empty()
		if cleanErr == nil && !empty {
			cleanErr = errors.New("failed Start left a populated command cgroup")
		}
		if cleanErr == nil {
			cleanErr = g.Remove()
		}
		cleanErr = errors.Join(cleanErr, g.Close())
		if cleanErr != nil {
			cleanErr = o.failLocked(cleanErr)
		}
		return nil, compose(err, cleanErr)
	}
	return g, nil
}

// Empty is always read from cgroup.events, including nested population. Cleanup
// ignores the cancelled work context and instead has its own finite deadline.
func waitEmpty(g commandGroup, bound time.Duration) (bool, error) {
	deadline := time.Now().Add(bound)
	for {
		empty, err := g.Empty()
		if err != nil || empty {
			return empty, err
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		time.Sleep(ownershipPoll)
	}
}

// emptyGroup never removes an unproven subtree. It may run concurrently with
// Cmd.Wait, so inherited pipe writers are killed before waiting for EOF.
func (o *Owner) emptyGroup(g commandGroup, grace bool) (result error) {
	bound := time.Duration(0)
	if grace {
		bound = ownershipGrace
	}
	empty, err := waitEmpty(g, bound)
	if err != nil {
		result = o.fail(err)
	}
	if !empty {
		if killErr := g.Kill(); killErr != nil {
			if empty, readErr := g.Empty(); readErr != nil || !empty {
				return compose(result, o.fail(compose(killErr, readErr)))
			}
		}
		empty, err = waitEmpty(g, ownershipPopulationWait)
		if err != nil {
			return compose(result, o.fail(err))
		}
		if !empty {
			return compose(result, o.fail(errors.New("cgroup.kill cleanup did not complete: descendants remain; command cgroup preserved")))
		}
	}
	return result
}

func (o *Owner) finishGroup(g commandGroup) error {
	err := g.Remove() // Rechecks populated=0 before removal.
	err = compose(err, g.Close())
	if err != nil {
		return o.fail(err)
	}
	return nil
}
func (o *Owner) clean(g commandGroup, grace bool) error {
	err := o.emptyGroup(g, grace)
	return compose(err, o.finishGroup(g))
}

// Keep single independent exit errors single: existing absence classifiers
// intentionally reject compound errors, even those containing an exit status.
func compose(errs ...error) error {
	var nonnil []error
	for _, err := range errs {
		if err != nil {
			nonnil = append(nonnil, err)
		}
	}
	if len(nonnil) == 1 {
		return nonnil[0]
	}
	return errors.Join(nonnil...)
}

func probePlacement(ctx context.Context, g commandGroup) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/true")
	cmd.WaitDelay = ownershipWaitDelay
	pidfd := -1
	placeCommand(cmd, g.FD(), &pidfd)
	err := cmd.Run()
	if pidfd >= 0 {
		syscall.Close(pidfd)
	}
	if err == nil && pidfd < 0 {
		return errors.New("pidfd exit observation unavailable")
	}
	return err
}
