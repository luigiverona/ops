//go:build ownership_integration

package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/luigiverona/ops/internal/run"
)

// Exercise Replace's staged executable, cancellation and independent recovery
// contexts entirely in temporary files. The fixture substitutes ordinary-user
// install/mv/rm for sudo; the production Owner/Exec still runs every command.
func TestNativeUpdaterOwnership(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"normal", "parent-death"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "python3", "testdata/ownership_supervisor.py", mode, binary)
			cmd.WaitDelay = 2 * time.Second
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("release supervisor: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "RELEASE CLEANUP PASS") {
				t.Fatalf("missing cleanup: %s", out)
			}
			t.Log(string(out))
		})
	}
}

func TestNativeUpdaterOwnershipHelper(t *testing.T) {
	dir := os.Getenv("OPS_RELEASE_FIXTURE_DIR")
	if dir == "" {
		return
	}
	if _, _, err := syscall.Syscall6(syscall.SYS_PRCTL, 36, 1, 0, 0, 0, 0); err != 0 {
		t.Fatal(err)
	}
	owner := run.NewOwner()
	if err := owner.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(membership), "\n") {
		if strings.HasPrefix(line, "0::") {
			if err := os.WriteFile(filepath.Join(dir, "scope"), []byte("/sys/fs/cgroup"+strings.TrimPrefix(line, "0::")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	binary := filepath.Join(dir, "verified")
	fixture, err := os.ReadFile("testdata/ownership.py")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, fixture, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Replace(ctx, unprivilegedUpdater{run.Exec{Owner: owner}}, binary, filepath.Join(dir, "installed"), "9.0.0")
	}()
	deadline := time.Now().Add(8 * time.Second)
	var records []string
	for len(records) < 3 {
		records, _ = filepath.Glob(filepath.Join(dir, "*.json"))
		if time.Now().After(deadline) {
			t.Fatal("updater descendants did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var cgroup string
	for _, record := range records {
		b, err := os.ReadFile(record)
		if err != nil {
			t.Fatal(err)
		}
		var value struct{ Cgroup string }
		var current string
		if err := json.Unmarshal(b, &value); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(value.Cgroup, "\n") {
			if strings.HasPrefix(line, "0::") {
				current = strings.TrimPrefix(line, "0::")
			}
		}
		scope, err := os.ReadFile(filepath.Join(dir, "scope"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(filepath.Base(current), "command-") || "/sys/fs/cgroup"+filepath.Dir(current) != string(scope) || cgroup != "" && current != cgroup {
			t.Fatal("updater ran outside its command group", current)
		}
		cgroup = current
	}
	fmt.Println("RELEASE TREE READY")
	if os.Getenv("OPS_RELEASE_PARENT_DEATH") == "1" {
		// Supervisor kills us here while the command population is still alive.
		time.Sleep(25 * time.Second)
		t.Fatal("supervisor failed to kill fixture owner")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || run.OwnershipFailed(err) {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("updater cleanup blocked")
	}
	for _, record := range records {
		b, err := os.ReadFile(record)
		if err != nil {
			t.Fatal(err)
		}
		var value struct{ PID int }
		if err := json.Unmarshal(b, &value); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			var status syscall.WaitStatus
			_, _ = syscall.Wait4(value.PID, &status, syscall.WNOHANG, nil)
			if _, err := os.Stat(fmt.Sprintf("/proc/%d", value.PID)); errors.Is(err, os.ErrNotExist) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("updater helper survived", value.PID)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if _, err := os.Stat("/sys/fs/cgroup" + cgroup); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("updater command cgroup remains", err)
	}
	if err := owner.Check(); err != nil {
		t.Fatal(err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "installed*")); len(files) != 0 {
		t.Fatal("independent owned cleanup did not remove staging", files)
	}
}

type unprivilegedUpdater struct{ run.Exec }

func (r unprivilegedUpdater) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if s.Name == "sudo" {
		s.Name = s.Args[1]
		s.Args = s.Args[2:]
		if s.Name == "install" {
			s.Args = append([]string{"-m", "0755", "--"}, s.Args[len(s.Args)-2:]...)
		}
	}
	return r.Exec.Run(ctx, s)
}
