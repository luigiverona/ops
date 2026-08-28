package system

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type fakeRunner struct{ arch string }

func (f fakeRunner) Run(context.Context, run.Spec) (run.Result, error) {
	return run.Result{Stdout: f.arch + "\n"}, nil
}

func TestDetectSupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(path, []byte("NAME=Arch Linux\nID=arch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := (Detector{OSRelease: path, EUID: func() int { return 1000 }, Runner: fakeRunner{"x86_64"}}).Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

func TestDetectRejectsRootDerivativeAndArchitecture(t *testing.T) {
	dir := t.TempDir()
	arch := filepath.Join(dir, "arch")
	derivative := filepath.Join(dir, "derived")
	_ = os.WriteFile(arch, []byte("ID=arch\n"), 0o600)
	_ = os.WriteFile(derivative, []byte("ID=manjaro\nID_LIKE=arch\n"), 0o600)
	tests := []Detector{
		{OSRelease: arch, EUID: func() int { return 0 }, Runner: fakeRunner{"x86_64"}},
		{OSRelease: derivative, EUID: func() int { return 1000 }, Runner: fakeRunner{"x86_64"}},
		{OSRelease: arch, EUID: func() int { return 1000 }, Runner: fakeRunner{"aarch64"}},
	}
	for i, detector := range tests {
		if err := detector.Detect(context.Background()); err == nil {
			t.Fatalf("case %d unexpectedly supported", i)
		}
	}
}
