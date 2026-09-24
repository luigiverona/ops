// Package testproc supervises disposable ownership integration fixtures.
package testproc

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Supervise runs a native test in a child. The Python parent owns its temporary
// files and exact scope cleanup even if the Go owner crashes or times out.
func Supervise(t *testing.T) bool { return supervise(t, "normal") }

// SuperviseDeath kills the owner only after a live keyboxd has been observed.
func SuperviseDeath(t *testing.T) bool { return supervise(t, "owner-death") }

func supervise(t *testing.T, mode string) bool {
	t.Helper()
	if os.Getenv("OPS_NATIVE_TEST_CHILD") == t.Name() {
		return false
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("supervisor source unavailable")
	}
	script := filepath.Join(filepath.Dir(source), "native.py")
	cmd := exec.Command("python3", script, binary, t.Name(), "^"+regexp.QuoteMeta(t.Name())+"$", mode)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native supervisor: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "NATIVE SUPERVISOR CLEANUP PASS") {
		t.Fatalf("missing cleanup proof: %s", out)
	}
	t.Log(string(out))
	return true
}

// RecordScope runs immediately after successful activation, before workloads.
func RecordScope(t *testing.T) {
	t.Helper()
	record := os.Getenv("OPS_OWNERSHIP_SCOPE_RECORD")
	if record == "" {
		t.Fatal("native fixture has no independent supervisor")
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			if err := os.WriteFile(record, []byte("/sys/fs/cgroup"+path), 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("native scope membership unavailable")
}
