//go:build ownership_integration

package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func cleanupNativeScope(t *testing.T, scope *cgroupScope) {
	t.Helper()
	// scope is the exact handle returned by this test's activation, never a scan
	// of user/system cgroups. All workload directories beneath it are disposable.
	entries, err := os.ReadDir("/sys/fs/cgroup" + scope.path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Error(err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "command-") && name != "nested-fixture" {
			t.Errorf("unexpected fixture directory %s", name)
			continue
		}
		events, err := scope.root.Open(name + "/cgroup.events")
		if err != nil {
			t.Error(err)
			continue
		}
		kill, err := scope.root.OpenFile(name+"/cgroup.kill", os.O_WRONLY, 0)
		if err != nil {
			events.Close()
			t.Error(err)
			continue
		}
		g := &cgroupCommand{scope: scope, name: name, events: events, kill: kill}
		// Failure cleanup must also thaw frozen placement fixtures.
		_ = os.WriteFile("/sys/fs/cgroup"+scope.path+"/"+name+"/cgroup.freeze", []byte("0"), 0)
		var records []processRecord
		_ = filepath.WalkDir("/sys/fs/cgroup"+scope.path+"/"+name, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr == nil && entry.Name() == "cgroup.procs" {
				b, _ := os.ReadFile(path)
				for _, value := range strings.Fields(string(b)) {
					pid, err := strconv.Atoi(value)
					if err == nil {
						records = append(records, processRecord{PID: pid})
					}
				}
			}
			return walkErr
		})
		err = g.Kill()
		empty, observeErr := waitEmpty(g, 8*time.Second)
		if observeErr != nil || !empty {
			t.Errorf("fixture cleanup: kill=%v empty=%v observation=%v", err, empty, observeErr)
		} else if err := g.Remove(); err != nil {
			t.Error(err)
		}
		reapRecorded(t, records)
		if err := g.Close(); err != nil {
			t.Error(err)
		}
	}
}

func TestOwnershipPTYFailureCleanup(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "testdata/ownership_pty.py", binary)
	cmd.Env = append(os.Environ(), "OPS_OWNERSHIP_PTY_HELPER=1", "OPS_OWNERSHIP_PTY_FAIL=1")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "OWNER-ONLY CLEANUP LEFT LIVE DESCENDANTS") || !strings.Contains(string(out), "injected PTY failure") || !strings.Contains(string(out), "PTY FIXTURE CLEANUP PASS") || !strings.Contains(string(out), "PTY WORKSPACE CLEANUP PASS") {
		t.Fatalf("failure cleanup did not complete: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if path, ok := strings.CutPrefix(line, "PTY WORKSPACE CLEANUP PASS "); ok {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("PTY workspace remains", path, err)
			}
		}
	}
	t.Log(string(out))
}

// Exercise the same post-Wait boundary against real cgroup.events. The direct
// command is synchronously reaped before the first observation triggers cancel.
func TestNativeCleanupCancellation(t *testing.T) {
	o := nativeOwner(t)
	for _, mode := range []string{"cancel", "deadline", "prior-failure", "prior-lifecycle-failure"} {
		t.Run(mode, func(t *testing.T) {
			g, err := o.scope.New(fmt.Sprintf("command-cleanup-%s", mode))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(context.Background(), time.Second)
				defer stop()
				cancel = func() {
					if ctx.Err() != nil {
						t.Error("deadline expired before population observation")
					}
					<-ctx.Done()
				}
			}
			// Closed output FDs ensure actual Wait finishes while descendant lives.
			childPID := filepath.Join(t.TempDir(), "child")
			code := "import os,signal,time,pathlib; os.register_at_fork(after_in_child=lambda: signal.alarm(10)); p=os.fork(); os._exit(7 if os.environ.get('OPS_PRIOR_FAILURE') else 0) if p else None; pathlib.Path(os.environ['OPS_CHILD_PID']).write_text(str(os.getpid())); os.close(1); os.close(2); time.sleep(8)"
			cmd := exec.Command("python3", "-c", code)
			cmd.Env = append(os.Environ(), "OPS_CHILD_PID="+childPID)
			if mode == "prior-failure" {
				cmd.Env = append(cmd.Env, "OPS_PRIOR_FAILURE=1")
			}
			pidfd := -1
			placeCommand(cmd, g.FD(), &pidfd)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitErr := cmd.Wait()
			awaitNative(t, func() bool { b, _ := os.ReadFile(childPID); return len(b) > 0 })
			pidBytes, _ := os.ReadFile(childPID)
			child, err := strconv.Atoi(string(pidBytes))
			if err != nil {
				t.Fatal(err)
			}
			if pidfd >= 0 {
				defer syscall.Close(pidfd)
			}
			observation := &cancelObservation{commandGroup: g, cancel: cancel, lifecycle: mode == "prior-lifecycle-failure"}
			// Use a separate Owner for intentional poison while retaining the real group.
			err, waited := (Exec{Owner: NewOwner()}).finishOwnedCommand(ctx, cmd, observation, nil, waitErr, nil, true, true, nil, false, nil)
			if mode == "prior-failure" {
				if !Exited(err, 7) || errors.Is(err, context.Canceled) {
					t.Fatalf("prior failure: %v", err)
				}
			} else {
				want := context.Canceled
				if mode == "deadline" {
					want = context.DeadlineExceeded
				}
				if !errors.Is(err, want) {
					t.Fatalf("cleanup context: %v", err)
				}
			}
			reapRecorded(t, []processRecord{{PID: child}})
			if !waited || !observation.sawPopulation {
				t.Fatalf("real live population was not observed: %v", err)
			}
			if mode == "prior-lifecycle-failure" && !OwnershipFailed(err) {
				t.Fatal("lost lifecycle failure", err)
			}
			if _, err := os.Stat("/sys/fs/cgroup" + o.scope.(*cgroupScope).path + "/command-cleanup-" + mode); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("group remains", err)
			}
		})
	}
}

type cancelObservation struct {
	commandGroup
	cancel                   func()
	sawPopulation, lifecycle bool
	reads                    int
}

func (g *cancelObservation) Empty() (bool, error) {
	empty, err := g.commandGroup.Empty()
	g.reads++
	if g.reads == 1 {
		g.sawPopulation = !empty && err == nil
		if g.lifecycle {
			return false, errors.New("injected lifecycle failure before cancellation")
		}
	}
	if g.reads == 1 || g.lifecycle && g.reads == 2 {
		g.cancel()
	}
	return empty, err
}

func cleanupCrashedOwner(t *testing.T, cmd *exec.Cmd, dir string) {
	t.Helper()
	if cmd.ProcessState == nil {
		_ = cmd.Process.Kill()
		reaped := make(chan struct{})
		go func() { _ = cmd.Wait(); close(reaped) }()
		select {
		case <-reaped:
		case <-time.After(8 * time.Second):
			t.Error("fixture owner reap timeout; still attempting scope cleanup")
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "scope"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Error(err)
		return
	}
	path := string(b)
	prefix := fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice/", os.Getuid())
	unitPrefix := fmt.Sprintf("ops-%d-", cmd.Process.Pid)
	if !strings.HasPrefix(path, prefix) || !strings.HasPrefix(filepath.Base(path), unitPrefix) || !strings.HasSuffix(path, ".scope") || filepath.Clean(path) != path {
		t.Error("invalid recorded fixture scope", path)
		return
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := os.WriteFile(path+"/cgroup.kill", []byte("1"), 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Error(err)
	}
	reapRecorded(t, fixtureRecords(t, dir))
	awaitNative(t, func() bool { _, err := os.Stat(path); return errors.Is(err, os.ErrNotExist) })
}
