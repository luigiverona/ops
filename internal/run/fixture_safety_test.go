package run

import (
	"os/exec"
	"testing"
)

func TestFixtureIndependentExpiration(t *testing.T) {
	cmd := exec.Command("python3", "testdata/ownership_safety.py")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture safety: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}

func TestArchitectureFixtureIndependentExpiration(t *testing.T) {
	cmd := exec.Command("python3", "testdata/architecture_safety.py")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("retained architecture safety: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
