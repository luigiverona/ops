package installer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func scriptPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("../../script/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPOSIXSyntax(t *testing.T) {
	cmd := exec.Command("sh", "-n", scriptPath(t))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v: %s", err, output)
	}
}

func TestUnsupportedPlatform(t *testing.T) {
	data, err := os.ReadFile(scriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	osRelease := filepath.Join(dir, "os-release")
	_ = os.WriteFile(osRelease, []byte("ID=manjaro\nID_LIKE=arch\n"), 0o600)
	rendered := strings.Replace(string(data), "/etc/os-release", osRelease, 2)
	path := filepath.Join(dir, "install.sh")
	_ = os.WriteFile(path, []byte(rendered), 0o700)
	cmd := exec.Command("sh", path)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "official Arch Linux") {
		t.Fatalf("output=%s err=%v", output, err)
	}
}

func TestUnconfiguredTrustFailsSafely(t *testing.T) {
	data, err := os.ReadFile(scriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	rendered := strings.Replace(string(data), `[ -r /dev/tty ] && [ -w /dev/tty ] || fail 'interactive installation requires a usable terminal'`, `:`, 1)
	path := filepath.Join(t.TempDir(), "install.sh")
	_ = os.WriteFile(path, []byte(rendered), 0o700)
	cmd := exec.Command("sh", path)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "release signing trust is not configured") {
		t.Fatalf("output=%s err=%v", output, err)
	}
}
