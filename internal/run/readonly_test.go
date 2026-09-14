package run

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReadOnlyFilesystemPreventsNativeWrites(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap unavailable")
	}
	path := filepath.Join(t.TempDir(), "must-not-exist")
	_, err := (Exec{}).Run(context.Background(), Spec{Name: "touch", Args: []string{path}, ReadOnlyFilesystem: true})
	if err == nil {
		t.Fatal("write in read-only inventory succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("inventory wrote host state: %v", err)
	}
	// Distinguish functioning isolation from an unavailable sandbox.
	result, err := (Exec{}).Run(context.Background(), Spec{Name: "cat", Args: []string{"/etc/os-release"}, ReadOnlyFilesystem: true})
	if err != nil || result.Stdout == "" {
		t.Fatalf("read-only inspection unavailable: %v", err)
	}
}

func TestReadOnlyFilesystemNeverFallsBack(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := (Exec{}).Run(context.Background(), Spec{Name: "/usr/bin/true", ReadOnlyFilesystem: true})
	if err == nil {
		t.Fatal("missing bubblewrap silently bypassed read-only boundary")
	}
}

func TestReadOnlyFilesystemPreservesRuntimeFiles(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap unavailable")
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		t.Skip("no user runtime directory")
	}
	dir, err := os.MkdirTemp(runtimeDir, "ops-readonly-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "inventory")
	if err := os.WriteFile(path, []byte("existing state"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := (Exec{}).Run(context.Background(), Spec{Name: "cat", Args: []string{path}, ReadOnlyFilesystem: true})
	if err != nil || result.Stdout != "existing state" {
		t.Fatalf("runtime inventory hidden: %q %v", result.Stdout, err)
	}
}
