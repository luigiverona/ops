//go:build ownership_integration

package release

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	if _, _, err := syscall.Syscall6(syscall.SYS_PRCTL, 36, 1, 0, 0, 0, 0); err != 0 {
		t.Fatal(err)
	}
	owner := run.NewOwner()
	if err := owner.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "verified")
	program := fmt.Sprintf("#!/usr/bin/python3\nimport os,pathlib,signal,time\nsignal.alarm(20)\np=os.fork()\nif p==0: os.setsid()\npathlib.Path(%q+str(os.getpid())+'.pid').write_text(pathlib.Path('/proc/self/cgroup').read_text())\nwhile True: time.sleep(.01)\n", dir+"/")
	if err := os.WriteFile(binary, []byte(program), 0700); err != nil {
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
	for len(records) < 2 {
		records, _ = filepath.Glob(filepath.Join(dir, "*.pid"))
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
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "0::") {
				cgroup = strings.TrimPrefix(line, "0::")
			}
		}
		if !strings.HasPrefix(filepath.Base(cgroup), "command-") {
			t.Fatal("updater ran outside ownership", cgroup)
		}
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
		pid, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(record), ".pid"))
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, 0, nil)
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("updater helper survived", pid, err)
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
