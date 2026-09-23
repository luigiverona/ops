package run

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

func placeCommand(cmd *exec.Cmd, fd int, pidfd *int) {
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd, PidFD: pidfd}
}

// execute keeps os/exec responsible for reaping and I/O. A pidfd independently
// observes direct-child exit even while Wait is draining inherited descriptors.
// Neither process-group nor terminal/session attributes are changed.
func (e Exec) execute(ctx context.Context, cmd *exec.Cmd, ephemeralHelpers bool) (err error, waited bool) {
	if err := ctx.Err(); err != nil {
		return err, true
	}
	if e.Owner == nil {
		return unownedRun(ctx, cmd), true
	}
	pidfd := -1
	// Before approval, retain direct-child cancellation and its context cause.
	canceled := make(chan error, 1)
	cmd.Cancel = func() error {
		err := cmd.Process.Kill()
		if err == nil {
			canceled <- ctx.Err()
		}
		return err
	}
	cmd.WaitDelay = ownershipWaitDelay
	group, err := e.Owner.start(ctx, cmd, &pidfd)
	if err != nil {
		return err, true
	}
	if group == nil {
		err := cmd.Wait()
		select {
		case cause := <-canceled:
			return compose(err, cause), true
		default:
			return err, true
		}
	}
	if pidfd >= 0 {
		defer syscall.Close(pidfd)
	}
	done := make(chan error, 1)
	var failureBeforeCancel atomic.Bool
	go func() {
		waitErr := cmd.Wait()
		// Publish independent completion even if cleanup is currently observing
		// population. Cancellation after this point must not replace the failure.
		if waitErr != nil && ctx.Err() == nil {
			failureBeforeCancel.Store(true)
		}
		done <- waitErr
	}()
	epfd, epErr := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if epErr == nil {
		defer syscall.Close(epfd)
		epErr = syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, pidfd, &syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(pidfd)})
	}
	var cause, waitErr error
	waitReceived, natural := false, false
	if epErr == nil {
		ticker := time.NewTicker(ownershipPoll)
		defer ticker.Stop()
		waitErr, cause, natural, waitReceived, epErr = awaitCommand(ctx, done, ticker.C, func() (bool, error) {
			events := make([]syscall.EpollEvent, 1)
			n, err := syscall.EpollWait(epfd, events, 0)
			if err == syscall.EINTR {
				return false, nil
			}
			return n > 0, err
		})
	}
	if epErr != nil {
		cause = e.Owner.fail(fmt.Errorf("observe command exit: %w", epErr))
	}
	return e.finishOwnedCommand(ctx, cmd, group, done, waitErr, cause, natural, waitReceived, epErr, ephemeralHelpers, &failureBeforeCancel)
}

// Direct completion is only one phase of the owned lifetime. This boundary also
// allows deterministic tests to publish Wait before any population observation.
func (e Exec) finishOwnedCommand(ctx context.Context, cmd *exec.Cmd, group commandGroup, done <-chan error, waitErr, cause error, natural, waitReceived bool, epErr error, ephemeralHelpers bool, failureBeforeCancel *atomic.Bool) (result error, waited bool) {
	// An independent failure published before cancellation wins, even if Wait
	// publishes while cleanup is already in progress. Lifecycle failures remain
	// joined with cancellation: they do not complete the unresolved lifetime.
	// Sample after final
	// removal/close, including exceptional reap/drain returns.
	priorFailure := waitReceived && waitErr != nil
	defer func() {
		if !priorFailure && (failureBeforeCancel == nil || !failureBeforeCancel.Load()) {
			if err := ctx.Err(); err != nil && !errors.Is(result, err) {
				result = compose(result, err)
			}
		}
	}()
	// cgroup population accounting may briefly outlive a reaped direct task.
	// Give that transition a small bound before classifying live descendants.
	settle := time.Duration(0)
	if natural {
		settle = ownershipExitSettle
	}
	empty, populationErr := waitEmpty(group, settle)
	if populationErr != nil {
		cause = compose(cause, e.Owner.fail(populationErr))
	}
	if natural && !empty && populationErr == nil && !ephemeralHelpers {
		cause = compose(cause, errors.New("command exited with live background descendants"))
	}
	if !natural && !empty {
		// This can fail for sudo/root. cgroup.kill, not signal permission, is the
		// authoritative force-cleanup mechanism after the grace phase.
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	cleanupErr := e.Owner.emptyGroup(group, !natural && epErr == nil && populationErr == nil)
	if !waitReceived {
		timer := time.NewTimer(ownershipWaitDelay)
		defer timer.Stop()
		select {
		case waitErr = <-done:
			waitReceived = true
		case <-timer.C:
			// Uninterruptible kernel tasks and caller-supplied blocked I/O cannot be
			// repaired by Go. Do not read buffers still owned by Wait's I/O goroutines.
			// Preserve this group's directory for diagnosis when reaping/draining
			// cannot be completed, even if live population has already reached zero.
			closeErr := group.Close()
			return compose(cause, cleanupErr, e.Owner.fail(compose(errors.New("command reap/pipe cleanup did not complete"), closeErr))), false
		}
	}
	return compose(waitErr, cause, cleanupErr, e.Owner.finishGroup(group)), true
}

// Completion and cancellation are arbitrated at this boundary. Completion already
// published by Wait wins a simultaneously ready context. Otherwise cancellation
// is latched before graceful shutdown; subsequent exit status cannot erase it.
func awaitCommand(ctx context.Context, done <-chan error, pulse <-chan time.Time, exited func() (bool, error)) (waitErr, cause error, natural, received bool, observeErr error) {
	for {
		select {
		case err := <-done:
			return err, nil, true, true, nil
		default:
		}
		select {
		case err := <-done:
			return err, nil, true, true, nil
		case <-ctx.Done():
			select {
			case err := <-done:
				return err, nil, true, true, nil
			default:
			}
			return nil, ctx.Err(), false, false, nil
		case <-pulse:
			exited, err := exited()
			if err != nil {
				return nil, nil, false, false, err
			}
			if exited {
				return nil, nil, true, false, nil
			}
		}
	}
}

// Pre-approval/read-only execution retains its narrower direct-child contract.
// It does not claim cgroup ownership, but pipe waiting and context identity are
// still bounded/preserved. Mutation cannot use this branch after activation.
func unownedRun(ctx context.Context, cmd *exec.Cmd) error {
	canceled := make(chan error, 1)
	cmd.WaitDelay = ownershipWaitDelay
	cmd.Cancel = func() error {
		err := cmd.Process.Kill()
		if err == nil {
			canceled <- ctx.Err()
		}
		return err
	}
	err := cmd.Run()
	select {
	case cause := <-canceled:
		return compose(err, cause)
	default:
		return err
	}
}
