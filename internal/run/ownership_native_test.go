//go:build ownership_integration

package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type processRecord struct {
	PID        int    `json:"pid"`
	UID        int    `json:"uid"`
	Role       string `json:"role"`
	SID        int    `json:"sid"`
	PGID       int    `json:"pgid"`
	Foreground int    `json:"foreground"`
	Cgroup     string `json:"cgroup"`
}

func nativeOwner(t *testing.T) *Owner {
	t.Helper()
	// Test-only orphan reaping. Production cgroup.events excludes dead zombies;
	// their ordinary init/subreaper handles reaping independently of ops.
	if _, _, err := syscall.Syscall6(syscall.SYS_PRCTL, 36, 1, 0, 0, 0, 0); err != 0 {
		t.Fatal(err)
	}
	o := NewOwner()
	if err := o.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return o
}
func awaitNative(t *testing.T, fn func() bool) {
	t.Helper()
	until := time.Now().Add(8 * time.Second)
	for !fn() {
		if time.Now().After(until) {
			t.Fatal("native synchronization deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func fixtureRecords(t *testing.T, dir string) []processRecord {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var records []processRecord
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var r processRecord
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	return records
}
func nativeSpec(t *testing.T, dir, mode string, privileged bool) Spec {
	t.Helper()
	script, err := filepath.Abs("testdata/ownership.py")
	if err != nil {
		t.Fatal(err)
	}
	s := Spec{Name: "/usr/bin/python3", Args: []string{script, mode, dir}}
	if privileged {
		s.Args = append([]string{"-n", "--", s.Name}, s.Args...)
		s.Name = "sudo"
	}
	return s
}
func checkContained(t *testing.T, o *Owner, records []processRecord, privileged bool) string {
	t.Helper()
	want := o.scope.(*cgroupScope).path + "/command-"
	var path string
	for _, r := range records {
		var cg string
		for _, line := range strings.Split(r.Cgroup, "\n") {
			if strings.HasPrefix(line, "0::") {
				cg = strings.TrimPrefix(line, "0::")
			}
		}
		if !strings.HasPrefix(cg, want) {
			t.Fatalf("escaped: %+v", r)
		}
		// cgroup.events on the command root also covers nested descendants.
		cg = want + strings.SplitN(strings.TrimPrefix(cg, want), "/", 2)[0]
		if path != "" && path != cg {
			t.Fatal("one command used multiple groups")
		}
		path = cg
		if privileged && r.UID != 0 {
			t.Fatalf("helper not root: %+v", r)
		}
	}
	return path
}
func reapRecorded(t *testing.T, records []processRecord) {
	t.Helper()
	awaitNative(t, func() bool {
		// Wait has already reaped the direct child. Reap only recorded test orphans.
		all := true
		for _, r := range records {
			var status syscall.WaitStatus
			_, _ = syscall.Wait4(r.PID, &status, syscall.WNOHANG, nil)
			if _, err := os.Stat(fmt.Sprintf("/proc/%d", r.PID)); !errors.Is(err, os.ErrNotExist) {
				all = false
			}
		}
		return all
	})
}
func nativeCase(t *testing.T, o *Owner, mode string, privileged bool) {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := Exec{Owner: o, Out: io.Discard, Err: io.Discard}
	done := make(chan error, 1)
	go func() { _, err := e.Run(ctx, nativeSpec(t, dir, mode, privileged)); done <- err }()
	awaitNative(t, func() bool { _, err := os.Stat(filepath.Join(dir, "ready")); return err == nil })
	records := fixtureRecords(t, dir)
	cg := checkContained(t, o, records, privileged)
	if len(records) < 4 {
		t.Fatal("fixture incomplete")
	}
	for _, r := range records {
		if r.Role == "leaf-1" && r.SID != r.PID {
			t.Fatal("setsid was not exercised")
		}
	}
	natural := mode == "natural" || mode == "stdout" || mode == "stderr"
	if !natural {
		cancel()
	}
	select {
	case err := <-done:
		if natural {
			if err == nil || !strings.Contains(err.Error(), "background descendants") {
				t.Fatalf("natural leak accepted: %v", err)
			}
		} else if !errors.Is(err, context.Canceled) {
			t.Fatalf("context identity lost: %v", err)
		}
		if OwnershipFailed(err) {
			t.Fatalf("cleanup failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run blocked after cancellation")
	}
	records = fixtureRecords(t, dir)
	checkContained(t, o, records, privileged)
	reapRecorded(t, records)
	if _, err := os.Stat("/sys/fs/cgroup" + cg); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("command cgroup remains", err)
	}
	if err := o.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeOwnership(t *testing.T) {
	if os.Getenv("OPS_OWNERSHIP_PTY_HELPER") != "" {
		return
	}
	o := nativeOwner(t)
	before := runtime.NumGoroutine()
	for _, mode := range []string{"tree", "natural", "stdout", "stderr", "fork", "nested", "tree", "tree"} {
		t.Run(mode, func(t *testing.T) { nativeCase(t, o, mode, false) })
	}
	t.Run("concurrent", func(t *testing.T) {
		var wg sync.WaitGroup
		for range 3 {
			wg.Go(func() { nativeCase(t, o, "tree", false) })
		}
		wg.Wait()
	})
	t.Run("frozen-placement", func(t *testing.T) {
		base := o.scope
		created := make(chan string, 1)
		o.scope = &freezingScope{ownedScope: base, created: created}
		defer func() { o.scope = base }()
		marker := filepath.Join(t.TempDir(), "executed")
		done := make(chan error, 1)
		go func() {
			_, err := (Exec{Owner: o}).Run(context.Background(), Spec{Name: "/usr/bin/python3", Args: []string{"-c", "import pathlib,sys; pathlib.Path(sys.argv[1]).write_text(pathlib.Path('/proc/self/cgroup').read_text())", marker}})
			done <- err
		}()
		cg := <-created
		defer os.WriteFile(cg+"/cgroup.freeze", []byte("0"), 0)
		awaitNative(t, func() bool { b, _ := os.ReadFile(cg + "/cgroup.procs"); return len(strings.Fields(string(b))) == 1 })
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("userspace ran before thaw")
		}
		pids, _ := os.ReadFile(cg + "/cgroup.procs")
		placed, err := os.ReadFile("/proc/" + strings.TrimSpace(string(pids)) + "/cgroup")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(placed), "0::"+strings.TrimPrefix(cg, "/sys/fs/cgroup")+"\n") {
			t.Fatal("not atomically placed")
		}
		if err := os.WriteFile(cg+"/cgroup.freeze", []byte("0"), 0); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(marker)
		if err != nil || string(after) != string(placed) {
			t.Fatal("first instruction membership changed", err)
		}
	})
	t.Run("empty-nested-removal", func(t *testing.T) {
		g, err := o.scope.New("nested-fixture")
		if err != nil {
			t.Fatal(err)
		}
		cg := g.(*cgroupCommand)
		if err := cg.scope.root.Mkdir(cg.name+"/nested", 0700); err != nil {
			t.Fatal(err)
		}
		if err := o.clean(g, false); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("manager-owned-service", func(t *testing.T) {
		unit := fmt.Sprintf("ops-service-fixture-%d-%d.service", os.Getpid(), time.Now().UnixNano())
		e := Exec{Owner: o}
		defer e.Run(context.Background(), Spec{Name: "systemctl", Args: []string{"--user", "stop", unit}})
		if _, err := e.Run(context.Background(), Spec{Name: "systemd-run", Args: []string{"--user", "--quiet", "--collect", "--unit=" + unit, "--", "/usr/bin/sleep", "20"}}); err != nil {
			t.Fatal(err)
		}
		result, err := e.Run(context.Background(), Spec{Name: "systemctl", Args: []string{"--user", "show", "--property=MainPID", "--value", unit}})
		if err != nil {
			t.Fatal(err)
		}
		pid := strings.TrimSpace(result.Stdout)
		if pid == "0" || pid == "" {
			t.Fatal("service did not survive initiating Run")
		}
		membership, err := os.ReadFile("/proc/" + pid + "/cgroup")
		if err != nil || strings.Contains(string(membership), o.scope.(*cgroupScope).path+"/") {
			t.Fatal("manager service confused with descendants", err)
		}
		if _, err := e.Run(context.Background(), Spec{Name: "systemctl", Args: []string{"--user", "stop", unit}}); err != nil {
			t.Fatal(err)
		}
		awaitNative(t, func() bool { _, err := os.Stat("/proc/" + pid); return errors.Is(err, os.ErrNotExist) })
	})
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := (Exec{Owner: o}).Run(ctx, Spec{Name: "/usr/bin/sleep", Args: []string{"20"}})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	})
	t.Run("expected-helpers", func(t *testing.T) {
		dir := t.TempDir()
		spec := nativeSpec(t, dir, "natural", false)
		spec.EphemeralHelpers = true
		if _, err := (Exec{Owner: o}).Run(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
		records := fixtureRecords(t, dir)
		cg := checkContained(t, o, records, false)
		reapRecorded(t, records)
		if _, err := os.Stat("/sys/fs/cgroup" + cg); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("declared helper survived")
		}
	})
	t.Run("start-failure", func(t *testing.T) {
		_, err := (Exec{Owner: o}).Run(context.Background(), Spec{Name: "/not-present-ops-ownership"})
		if !errors.Is(err, os.ErrNotExist) || OwnershipFailed(err) {
			t.Fatal(err)
		}
	})
	t.Run("bwrap", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		spec := Spec{Name: "/usr/bin/python3", Args: []string{"-c", "import os,signal,time; signal.alarm(15); p=os.fork(); print('ready',flush=True); time.sleep(12)"}, ReadOnlyFilesystem: true}
		go func() { _, err := (Exec{Owner: o}).Run(ctx, spec); done <- err }()
		path := "/sys/fs/cgroup" + o.scope.(*cgroupScope).path
		// Inspect only our own command cgroup in this test. Two python PIDs prove
		// the namespace launcher reached the fork before cancellation is requested.
		awaitNative(t, func() bool {
			entries, err := os.ReadDir(path)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, entry := range entries {
				if entry.IsDir() {
					b, _ := os.ReadFile(filepath.Join(path, entry.Name(), "cgroup.procs"))
					for _, pid := range strings.Fields(string(b)) {
						comm, _ := os.ReadFile("/proc/" + pid + "/comm")
						if strings.TrimSpace(string(comm)) == "python3" {
							count++
						}
					}
				}
			}
			return count >= 2
		})
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) || OwnershipFailed(err) {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("bwrap cancellation blocked")
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				t.Fatal("bwrap command cgroup remains", entry.Name())
			}
		}
		t.Setenv("PATH", t.TempDir())
		if _, err := (Exec{Owner: o}).Run(context.Background(), Spec{Name: "/usr/bin/true", ReadOnlyFilesystem: true}); err == nil {
			t.Fatal("owned inventory fell back without bwrap")
		}
	})
	t.Run("exit-and-cleanup-errors", func(t *testing.T) {
		failure := errors.New("injected removal failure")
		base := o.scope
		o.scope = removalFailureScope{ownedScope: base, err: failure}
		_, err := (Exec{Owner: o}).Run(context.Background(), Spec{Name: "/usr/bin/sh", Args: []string{"-c", "exit 7"}})
		if !OwnershipFailed(err) || !errors.Is(err, failure) || !Exited(err, 7) {
			t.Fatal("exit or cleanup failure lost", err)
		}
		if !OwnershipFailed(o.Check()) {
			t.Fatal("cleanup error did not poison admission")
		}
	})
	awaitNative(t, func() bool { runtime.GC(); return runtime.NumGoroutine() <= before+2 })
}

// Dedicated invocation only; never skipped by ordinary go test ./.... CI runs
// this before revoking its existing cloud-init grant, not on developer machines.
func TestPrivilegedOwnership(t *testing.T) {
	if os.Getenv("OPS_OWNERSHIP_PRIVILEGED") != "1" {
		t.Fatal("explicit privileged CI fixture authorization required")
	}
	if os.Getuid() != 1000 {
		t.Fatal("controller must be ordinary UID 1000")
	}
	o := nativeOwner(t)
	for _, mode := range []string{"tree", "natural", "stdout", "stderr", "fork", "fork", "fork"} {
		t.Run(mode, func(t *testing.T) { nativeCase(t, o, mode, true) })
	}
}

func TestOwnershipPTY(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "testdata/ownership_pty.py", binary)
	cmd.Env = append(os.Environ(), "OPS_OWNERSHIP_PTY_HELPER=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PTY: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
func TestOwnershipPTYHelper(t *testing.T) {
	if os.Getenv("OPS_OWNERSHIP_PTY_HELPER") != "1" {
		return
	}
	o := nativeOwner(t)
	privileged := os.Getenv("OPS_OWNERSHIP_PRIVILEGED") == "1"
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dir := t.TempDir()
	spec := nativeSpec(t, dir, "tty", privileged)
	spec.Interactive = true
	spec.Interaction = "ownership PTY fixture"
	done := make(chan error, 1)
	e := Exec{Owner: o, In: os.Stdin, Out: os.Stdout, Err: os.Stderr}
	go func() { _, err := e.Run(ctx, spec); done <- err }()
	awaitNative(t, func() bool { _, err := os.Stat(filepath.Join(dir, "ready")); return err == nil })
	records := fixtureRecords(t, dir)
	cg := checkContained(t, o, records, privileged)
	if !privileged {
		for _, r := range records {
			if r.Role == "direct" && (r.PGID != syscall.Getpgrp() || r.Foreground != syscall.Getpgrp()) {
				t.Fatalf("foreground changed: %+v", r)
			}
		}
	}
	// Include every sudo monitor/helper actually present, without assuming shape.
	b, err := os.ReadFile("/sys/fs/cgroup" + cg + "/cgroup.procs")
	if err != nil {
		t.Fatal(err)
	}
	pids := strings.Fields(string(b))
	if len(pids) < 4 {
		t.Fatal("incomplete subtree")
	}
	for _, pid := range pids {
		member, err := os.ReadFile("/proc/" + pid + "/cgroup")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(member), "0::"+cg+"\n") {
			t.Fatal("sudo/PTY process escaped")
		}
		comm, _ := os.ReadFile("/proc/" + pid + "/comm")
		fmt.Printf("OWNED PID %s %s", pid, comm)
	}
	fmt.Println("NATIVE TREE READY")
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || OwnershipFailed(err) {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("PTY cleanup blocked")
	}
	reapRecorded(t, fixtureRecords(t, dir))
	for _, pid := range pids {
		n, _ := strconv.Atoi(pid)
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(n, &status, syscall.WNOHANG, nil)
	}
	awaitNative(t, func() bool {
		for _, pid := range pids {
			if _, err := os.Stat("/proc/" + pid); !errors.Is(err, os.ErrNotExist) {
				return false
			}
		}
		return true
	})
	if _, err := os.Stat("/sys/fs/cgroup" + cg); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("PTY cgroup remains")
	}
	fmt.Println("NATIVE CLEAN PASS")
	_, err = e.Run(context.Background(), Spec{Name: "/usr/bin/python3", Args: []string{"-c", "print('NATIVE SECOND INPUT',flush=True); assert input()=='terminal-usable'"}, Interactive: true, Interaction: "terminal reuse fixture"})
	if err != nil {
		t.Fatal(err)
	}
}

type freezingScope struct {
	ownedScope
	created chan string
}

func (s *freezingScope) New(name string) (commandGroup, error) {
	g, err := s.ownedScope.New(name)
	if err != nil {
		return nil, err
	}
	cg := g.(*cgroupCommand)
	path := "/sys/fs/cgroup" + cg.scope.path + "/" + name
	if err := os.WriteFile(path+"/cgroup.freeze", []byte("1"), 0); err != nil {
		g.Remove()
		g.Close()
		return nil, err
	}
	s.created <- path
	return g, nil
}

// This demonstrates the explicit crash limitation without introducing a
// production supervisor. The surviving test parent cleans only its fixture.
func TestNativeCrashScopeLifetime(t *testing.T) {
	_ = nativeOwner(t)
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestOwnershipCrashHelper$")
	cmd.Env = append(os.Environ(), "OPS_OWNERSHIP_CRASH_HELPER="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	awaitNative(t, func() bool { _, err := os.Stat(filepath.Join(dir, "ready")); return err == nil })
	records := fixtureRecords(t, dir)
	var path string
	for _, line := range strings.Split(records[0].Cgroup, "\n") {
		if strings.HasPrefix(line, "0::") {
			path = strings.TrimPrefix(line, "0::")
		}
	}
	if !strings.HasPrefix(path, "/user.slice/user-1000.slice/") || !strings.HasPrefix(filepath.Base(path), "command-") {
		t.Fatal("unexpected fixture membership", path)
	}
	root, err := os.OpenRoot("/sys/fs/cgroup" + filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	events, err := os.Open("/sys/fs/cgroup" + path + "/cgroup.events")
	if err != nil {
		t.Fatal(err)
	}
	kill, err := os.OpenFile("/sys/fs/cgroup"+path+"/cgroup.kill", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	g := &cgroupCommand{scope: &cgroupScope{root: root}, name: filepath.Base(path), events: events, kill: kill}
	defer func() { _ = g.Kill(); _ = g.Close() }()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("owner was not killed")
	}
	if empty, err := g.Empty(); err != nil || empty {
		t.Fatal("unexpected automatic crash cleanup", empty, err)
	}
	// With the owner absent, systemd can collect the whole scope as soon
	// as this test kills the remaining population. Removed kernfs descriptors
	// then report ENODEV; scope disappearance is the expected crash cleanup.
	if err := g.Kill(); err != nil {
		t.Fatal(err)
	}
	reapRecorded(t, records)
	awaitNative(t, func() bool {
		_, err := os.Stat("/sys/fs/cgroup" + filepath.Dir(path))
		return errors.Is(err, os.ErrNotExist)
	})
}
func TestOwnershipCrashHelper(t *testing.T) {
	dir := os.Getenv("OPS_OWNERSHIP_CRASH_HELPER")
	if dir == "" {
		return
	}
	o := nativeOwner(t)
	_, err := (Exec{Owner: o}).Run(context.Background(), nativeSpec(t, dir, "tree", false))
	t.Fatal("fixture owner should have been killed", err)
}

type removalFailureScope struct {
	ownedScope
	err error
}

func (s removalFailureScope) New(name string) (commandGroup, error) {
	g, err := s.ownedScope.New(name)
	if err != nil {
		return nil, err
	}
	return removalFailureGroup{g, s.err}, nil
}

type removalFailureGroup struct {
	commandGroup
	err error
}

func (g removalFailureGroup) Remove() error { return compose(g.commandGroup.Remove(), g.err) }
