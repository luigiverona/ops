package installer

import (
	"crypto/sha256"
	"encoding/hex"
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

func releaseScriptPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("../../script/prepare-release.sh")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPOSIXSyntax(t *testing.T) {
	cmd := exec.Command("sh", "-n", scriptPath(t), releaseScriptPath(t))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v: %s", err, output)
	}
}

func TestReleaseHelperRequiresCleanExactTagBeforeBuilding(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	runGit := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=ops test", "-c", "user.email=ops@example.invalid"}, args...)...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	runGit("init", "-q")
	_ = os.WriteFile(filepath.Join(dir, "source"), []byte("test\n"), 0o600)
	runGit("add", "source")
	runGit("commit", "-q", "-m", "test")

	cmd := exec.Command("sh", releaseScriptPath(t), "1.0.0")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "exact intended tag v1.0.0 does not exist") {
		t.Fatalf("missing-tag output=%s err=%v", output, err)
	}
	runGit("tag", "v1.0.0")
	_ = os.WriteFile(filepath.Join(dir, "dirty"), []byte("test\n"), 0o600)
	cmd = exec.Command("sh", releaseScriptPath(t), "1.0.0")
	cmd.Dir = dir
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "repository is not clean") {
		t.Fatalf("dirty-tree output=%s err=%v", output, err)
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
	cmd.Env = append(os.Environ(), "PATH="+fakePlatformCommands(t)+":"+os.Getenv("PATH"))
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
	dir := t.TempDir()
	osRelease := filepath.Join(dir, "os-release")
	_ = os.WriteFile(osRelease, []byte("ID=arch\n"), 0o600)
	rendered := strings.Replace(string(data), "/etc/os-release", osRelease, 2)
	rendered = strings.Replace(rendered, `[ -r /dev/tty ] && [ -w /dev/tty ] || fail 'interactive installation requires a usable terminal'`, `:`, 1)
	path := filepath.Join(dir, "install.sh")
	_ = os.WriteFile(path, []byte(rendered), 0o700)
	cmd := exec.Command("sh", path)
	cmd.Env = append(os.Environ(), "PATH="+fakePlatformCommands(t)+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "release signing trust is not configured") {
		t.Fatalf("output=%s err=%v", output, err)
	}
}

func fakePlatformCommands(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write("id", "printf '1000\\n'")
	write("uname", "case \"$1\" in -s) printf 'Linux\\n';; -m) printf 'x86_64\\n';; esac")
	// The trust test must pass prerequisite discovery without ever invoking sudo.
	write("sudo", "exit 1")
	return dir
}

func TestInstallerSignatureStatusFailsClosed(t *testing.T) {
	fingerprint := strings.Repeat("A", 40)
	other := strings.Repeat("B", 40)
	tests := []struct {
		name, status string
		exit         string
		wantSuccess  bool
	}{
		{"valid current signing subkey", "[GNUPG:] NEWSIG\n[GNUPG:] GOODSIG test\n[GNUPG:] VALIDSIG " + fingerprint + " 0 0 0 0 0 0 0 0 0\n", "0", true},
		{"wrong signer", "[GNUPG:] VALIDSIG " + other + " 0 0 0 0 0 0 0 0 0\n", "0", false},
		{"invalid signature", "[GNUPG:] BADSIG test\n", "1", false},
		{"revoked signing key", "[GNUPG:] REVKEYSIG test\n[GNUPG:] VALIDSIG " + fingerprint + " 0 0 0 0 0 0 0 0 0\n", "0", false},
		{"expired signing key", "[GNUPG:] EXPKEYSIG test\n[GNUPG:] VALIDSIG " + fingerprint + " 0 0 0 0 0 0 0 0 0\n", "0", false},
		{"expired signature", "[GNUPG:] EXPSIG test\n[GNUPG:] VALIDSIG " + fingerprint + " 0 0 0 0 0 0 0 0 0\n", "0", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd, target, _ := installerCommand(t, fingerprint, test.status, test.exit)
			output, err := cmd.CombinedOutput()
			if test.wantSuccess && err != nil {
				t.Fatalf("installer failed: %v\n%s", err, output)
			}
			if !test.wantSuccess && err == nil {
				t.Fatalf("installer accepted unsafe signature state:\n%s", output)
			}
			if test.wantSuccess {
				if _, err := os.Stat(target); err != nil {
					t.Fatalf("verified binary was not installed: %v", err)
				}
			}
		})
	}
}

func TestInstallerRejectsSymlinkedOpsConfigDirectory(t *testing.T) {
	fingerprint := strings.Repeat("A", 40)
	status := "[GNUPG:] VALIDSIG " + fingerprint + " 0 0 0 0 0 0 0 0 0\n"
	cmd, _, home := installerCommand(t, fingerprint, status, "0")
	outside := t.TempDir()
	_ = os.Mkdir(filepath.Join(home, ".config"), 0o700)
	_ = os.Symlink(outside, filepath.Join(home, ".config", "ops"))
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "configuration directory ~/.config/ops is a symlink") {
		t.Fatalf("output=%s err=%v", output, err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "apps.toml")); !os.IsNotExist(err) {
		t.Fatal("installer created configuration through the symlink")
	}
}

func TestInstallerPreservesExistingConfigAndRejectsUnsafeConfigFile(t *testing.T) {
	fingerprint := strings.Repeat("A", 40)
	status := "[GNUPG:] VALIDSIG " + fingerprint + " 0 0 0 0 0 0 0 0 0\n"
	t.Run("preserves regular file", func(t *testing.T) {
		cmd, _, home := installerCommand(t, fingerprint, status, "0")
		dir := filepath.Join(home, ".config", "ops")
		_ = os.MkdirAll(dir, 0o700)
		path := filepath.Join(dir, "apps.toml")
		_ = os.WriteFile(path, []byte("user configuration\n"), 0o600)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("installer failed: %v\n%s", err, output)
		}
		data, _ := os.ReadFile(path)
		if string(data) != "user configuration\n" {
			t.Fatal("existing configuration was overwritten")
		}
	})
	t.Run("rejects file symlink", func(t *testing.T) {
		cmd, _, home := installerCommand(t, fingerprint, status, "0")
		dir := filepath.Join(home, ".config", "ops")
		_ = os.MkdirAll(dir, 0o700)
		outside := filepath.Join(home, "outside")
		_ = os.WriteFile(outside, []byte("keep\n"), 0o600)
		_ = os.Symlink(outside, filepath.Join(dir, "apps.toml"))
		output, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "apps.toml is a symlink") {
			t.Fatalf("output=%s err=%v", output, err)
		}
		data, _ := os.ReadFile(outside)
		if string(data) != "keep\n" {
			t.Fatal("configuration symlink target was modified")
		}
	})
}

func installerCommand(t *testing.T, fingerprint, status, gpgExit string) (*exec.Cmd, string, string) {
	t.Helper()
	data, err := os.ReadFile(scriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	artifacts := filepath.Join(dir, "artifacts")
	binDir := filepath.Join(dir, "bin")
	for _, path := range []string{home, artifacts, binDir} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	osRelease := filepath.Join(dir, "os-release")
	_ = os.WriteFile(osRelease, []byte("ID=arch\n"), 0o600)
	version := "1.2.3"
	binary := []byte("#!/bin/sh\nprintf 'ops " + version + "\\n'\n")
	_ = os.WriteFile(filepath.Join(artifacts, "ops-linux-x86_64"), binary, 0o700)
	sum := sha256.Sum256(binary)
	_ = os.WriteFile(filepath.Join(artifacts, "checksums.txt"), []byte(hex.EncodeToString(sum[:])+"  ops-linux-x86_64\n"), 0o600)
	_ = os.WriteFile(filepath.Join(artifacts, "checksums.txt.sig"), []byte("test signature"), 0o600)
	_ = os.WriteFile(filepath.Join(artifacts, "latest"), []byte(version+"\n"), 0o600)

	writeFake := func(name, body string) {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFake("id", "printf '1000\\n'")
	writeFake("uname", "case \"$1\" in -s) printf 'Linux\\n';; -m) printf 'x86_64\\n';; esac")
	writeFake("curl", `
out=
url=
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o) out=$2; shift 2 ;;
        -*) shift ;;
        *) url=$1; shift ;;
    esac
done
source=$OPS_TEST_ARTIFACTS/${url##*/}
if [ -n "$out" ]; then cp "$source" "$out"; else cat "$source"; fi`)
	writeFake("gpg", `
case " $* " in
    *" --show-keys "*)
        printf 'pub:u:255:22:PRIMARY:0:0:::::c:::::ed25519::::0:\n'
        printf 'fpr:::::::::CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC:\n'
        printf 'sub:u:255:22:SUBKEY:0:0:::::s:::::ed25519::::0:\n'
        printf 'fpr:::::::::%s:\n' "$OPS_TEST_FINGERPRINT"
        ;;
    *" --import "*) exit 0 ;;
    *" --verify "*) printf '%s' "$OPS_TEST_GPG_STATUS"; exit "$OPS_TEST_GPG_EXIT" ;;
    *) exit 1 ;;
esac`)
	writeFake("install", `
while [ "$#" -gt 0 ]; do
    if [ "$1" = -- ]; then
        shift
        cp "$1" "$2"
        chmod 0755 "$2"
        exit 0
    fi
    shift
done
exit 1`)
	writeFake("sudo", `
case "${1:-}" in
    -v) exit 0 ;;
    -n) shift ;;
esac
exec "$@"`)

	target := filepath.Join(dir, "installed-ops")
	rendered := strings.ReplaceAll(string(data), "/etc/os-release", osRelease)
	rendered = strings.Replace(rendered, "target=/usr/local/bin/ops", "target="+target, 1)
	rendered = strings.Replace(rendered, "fingerprint='@OPS_SIGNING_FINGERPRINT@'", "fingerprint='"+fingerprint+"'", 1)
	rendered = strings.Replace(rendered, "@OPS_SIGNING_PUBLIC_KEY@", "test public key", 1)
	rendered = strings.Replace(rendered, `[ -r /dev/tty ] && [ -w /dev/tty ] || fail 'interactive installation requires a usable terminal'`, `:`, 1)
	prompt := `printf 'Install this verified release? [Y/n] ' > /dev/tty
IFS= read -r answer < /dev/tty || fail 'could not read confirmation'`
	rendered = strings.Replace(rendered, prompt, "answer=yes", 1)
	path := filepath.Join(dir, "install.sh")
	_ = os.WriteFile(path, []byte(rendered), 0o700)
	cmd := exec.Command("sh", path)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"OPS_TEST_ARTIFACTS="+artifacts,
		"OPS_TEST_FINGERPRINT="+fingerprint,
		"OPS_TEST_GPG_STATUS="+status,
		"OPS_TEST_GPG_EXIT="+gpgExit,
	)
	return cmd, target, home
}
