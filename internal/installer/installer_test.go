package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luigiverona/ops/internal/config"
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
			cmd, target, home := installerCommand(t, fingerprint, test.status, test.exit)
			output, err := cmd.CombinedOutput()
			if test.wantSuccess && err != nil {
				t.Fatalf("installer failed: %v\n%s", err, output)
			}
			if !test.wantSuccess && err == nil {
				t.Fatalf("installer accepted unsafe signature state:\n%s", output)
			}
			if test.wantSuccess {
				want := "ops 1.2.3 verified.\n\nInstalled ops 1.2.3.\nCreated ~/.config/ops/apps.toml.\n\nOptionally add applications; the file explains names and sources.\nRun ops to review workstation setup, including Git, SSH, and GitHub.\n"
				if string(output) != want {
					t.Fatalf("installer output=%q, want=%q", output, want)
				}
				if _, err := os.Stat(target); err != nil {
					t.Fatalf("verified binary was not installed: %v", err)
				}
				data, err := os.ReadFile(config.Path(home))
				if err != nil || string(data) != config.Default {
					t.Fatalf("created config = %q, %v", data, err)
				}
				for path, mode := range map[string]os.FileMode{config.Path(home): 0o600, filepath.Dir(config.Path(home)): 0o700} {
					info, err := os.Stat(path)
					if err != nil || info.Mode().Perm() != mode {
						t.Fatalf("private permissions for %s: %v, %v", path, info, err)
					}
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

func TestInstallerDeclineLeavesBinaryAndConfigurationAbsent(t *testing.T) {
	fingerprint := strings.Repeat("A", 40)
	status := "[GNUPG:] VALIDSIG " + fingerprint + " 0 0 0 0 0 0 0 0 0\n"
	cmd, target, home := installerCommand(t, fingerprint, status, "0")
	cmd.Env = append(cmd.Env, "OPS_TEST_INSTALL_ANSWER=no")
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "ops 1.2.3 verified.\nNo changes made.\n" {
		t.Fatalf("err=%v output=%s", err, output)
	}
	for _, path := range []string{target, filepath.Join(home, ".config")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("decline created %s: %v", path, err)
		}
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
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		output, err := cmd.CombinedOutput()
		want := "ops 1.2.3 verified.\n\nInstalled ops 1.2.3.\nPreserved existing ~/.config/ops/apps.toml.\n\nRun ops to review workstation setup, including Git, SSH, and GitHub.\n"
		if err != nil || string(output) != want {
			t.Fatalf("installer output=%q, err=%v, want=%q", output, err, want)
		}
		data, _ := os.ReadFile(path)
		if string(data) != "user configuration\n" {
			t.Fatal("existing configuration was overwritten")
		}
		after, err := os.Stat(path)
		if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
			t.Fatalf("existing configuration metadata changed: %v", err)
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
	prompt := `printf 'Install to %s? [Y/n] ' "$target" > /dev/tty
IFS= read -r answer < /dev/tty || fail 'could not read confirmation'`
	if strings.Count(rendered, prompt) != 1 {
		t.Fatal("installer must ask once, through the controlling terminal, for its exact target")
	}
	rendered = strings.Replace(rendered, prompt, `answer=${OPS_TEST_INSTALL_ANSWER:-yes}`, 1)
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

// Faults are inserted only into the temporary test copy, at checked boundaries.
// Production does not expose environment variables that bypass filesystem checks.
func installerHook(t *testing.T, cmd *exec.Cmd, before, hook string) {
	t.Helper()
	path := cmd.Args[1]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), before) != 1 {
		t.Fatalf("expected one installer boundary %q", before)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), before, hook+"\n"+before, 1)), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestInstallerConfigPreflightPreservesBinary(t *testing.T) {
	for _, kind := range []string{"parent file", "ops file", "ops symlink", "config directory", "config symlink", "dangling config symlink"} {
		t.Run(kind, func(t *testing.T) {
			fingerprint := strings.Repeat("A", 40)
			cmd, target, home := installerCommand(t, fingerprint, "[GNUPG:] VALIDSIG "+fingerprint+" 0 0 0 0 0 0 0 0 0\n", "0")
			if err := os.WriteFile(target, []byte("original binary"), 0o700); err != nil {
				t.Fatal(err)
			}
			path := config.Path(home)
			if kind == "parent file" {
				if err := os.WriteFile(filepath.Join(home, ".config"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "ops file", "ops symlink":
					if err := os.Remove(filepath.Dir(path)); err != nil {
						t.Fatal(err)
					}
					if kind == "ops file" {
						if err := os.WriteFile(filepath.Dir(path), []byte("keep"), 0o600); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Symlink(t.TempDir(), filepath.Dir(path)); err != nil {
						t.Fatal(err)
					}
				case "config directory":
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				default:
					outside := filepath.Join(home, "outside")
					if kind == "config symlink" {
						if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Symlink(outside, path); err != nil {
						t.Fatal(err)
					}
				}
			}
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "configuration") || strings.Contains(string(output), "Installed ops") {
				t.Fatalf("output=%s, err=%v", output, err)
			}
			if data, err := os.ReadFile(target); err != nil || string(data) != "original binary" {
				t.Fatalf("preflight replaced binary: %q, %v", data, err)
			}
		})
	}
}

func TestInstallerConfigurationFailuresAndRaces(t *testing.T) {
	tests := []struct {
		name, boundary, hook, want string
		preserved                  bool
	}{
		{"parent creation failure", "binary_installed=yes", `mkdir() { return 1; }`, "could not create configuration parent", false},
		{"directory creation failure", "binary_installed=yes", `mkdir() { if [ "$*" = "$config_dir" ]; then return 1; fi; command mkdir "$@"; }`, "could not create configuration directory", false},
		{"exclusive creation failure", "    config_stage=$(", `rmdir "$config_dir"`, "could not create", false},
		{"write failure", "        if ! cat > ./apps.toml", `cat() { printf 'partial'; return 1; }`, "could not write", false},
		{"simulated close failure", "        if ! cat > ./apps.toml", `cat() { command cat; return 1; }`, "could not write", false},
		{"failed writer with concurrent config", "        if ! cat > ./apps.toml", `cat() { printf 'replacement config' > /proc/$$/cwd/apps.toml; printf 'partial'; return 1; }`, "could not write", false},
		{"raced regular file", "        ln -T -- ./apps.toml", `printf 'raced config' > "$config"`, "Preserved existing ~/.config/ops/apps.toml.", true},
		{"raced symlink", "        ln -T -- ./apps.toml", `printf 'keep' > "$HOME/outside"; ln -s "$HOME/outside" "$config"`, "apps.toml is a symlink", false},
		{"raced dangling symlink", "        ln -T -- ./apps.toml", `ln -s "$HOME/outside" "$config"`, "apps.toml is a symlink", false},
		{"raced config directory", "        ln -T -- ./apps.toml", `mkdir "$config"`, "apps.toml is not a regular file", false},
		{"ops directory changed after preflight", "binary_installed=yes", `mkdir -p "$config_parent"; ln -s "$HOME" "$config_dir"`, "configuration directory ~/.config/ops is a symlink", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fingerprint := strings.Repeat("A", 40)
			cmd, target, home := installerCommand(t, fingerprint, "[GNUPG:] VALIDSIG "+fingerprint+" 0 0 0 0 0 0 0 0 0\n", "0")
			installerHook(t, cmd, test.boundary, test.hook)
			output, err := cmd.CombinedOutput()
			if (err == nil) != test.preserved || !strings.Contains(string(output), test.want) {
				t.Fatalf("output=%s, err=%v", output, err)
			}
			binaryOutput, binaryErr := exec.Command(target, "--version").CombinedOutput()
			if binaryErr != nil || string(binaryOutput) != "ops 1.2.3\n" {
				t.Fatalf("binary did not remain installed: %q, %v", binaryOutput, binaryErr)
			}
			if !test.preserved {
				want := "Installed ops 1.2.3, but configuration setup failed for " + config.Path(home) + ".\nThe binary remains installed."
				if !strings.Contains(string(output), want) || strings.Contains(string(output), "Preserved existing") || strings.Contains(string(output), "Created ~/.config") || strings.Contains(string(output), "restored") {
					t.Fatalf("incorrect partial installation report: %s", output)
				}
			}
			stages, stageErr := filepath.Glob(filepath.Join(home, ".config", "ops", ".ops-config.*"))
			if stageErr != nil || len(stages) != 0 {
				t.Fatalf("staging files remain: %v, %v", stages, stageErr)
			}
			switch test.name {
			case "write failure", "simulated close failure":
				if _, err := os.Lstat(config.Path(home)); !os.IsNotExist(err) {
					t.Fatalf("failed writer published config: %v", err)
				}
			case "raced regular file", "failed writer with concurrent config":
				want := "raced config"
				if test.name == "failed writer with concurrent config" {
					want = "replacement config"
				}
				if data, err := os.ReadFile(config.Path(home)); err != nil || string(data) != want {
					t.Fatalf("unexpected config contents: %q, %v", data, err)
				}
			case "raced symlink":
				if data, err := os.ReadFile(filepath.Join(home, "outside")); err != nil || string(data) != "keep" {
					t.Fatalf("symlink target changed: %q, %v", data, err)
				}
			case "raced dangling symlink":
				if _, err := os.Lstat(filepath.Join(home, "outside")); !os.IsNotExist(err) {
					t.Fatalf("created dangling symlink target: %v", err)
				}
			}
		})
	}
}

func TestInstallerConfigWriteCannotFollowReplacedDirectory(t *testing.T) {
	for _, boundary := range []string{"config_physical=$(", "    config_stage=$(", "        ln -T -- ./apps.toml"} {
		for _, replacement := range []string{`ln -s "$HOME/outside" "$config_dir"`, `mkdir "$config_dir"`} {
			t.Run(boundary+replacement, func(t *testing.T) {
				fingerprint := strings.Repeat("A", 40)
				cmd, _, home := installerCommand(t, fingerprint, "[GNUPG:] VALIDSIG "+fingerprint+" 0 0 0 0 0 0 0 0 0\n", "0")
				installerHook(t, cmd, boundary, `mkdir "$HOME/outside"; mv "$config_dir" "$HOME/original-ops"; `+replacement)
				output, err := cmd.CombinedOutput()
				// A replacement real directory before pinning is safe to adopt.
				adopted := boundary == "config_physical=$(" && replacement == `mkdir "$config_dir"`
				if (err == nil) != adopted {
					t.Fatalf("directory race: %v\n%s", err, output)
				}
				if _, err := os.Lstat(filepath.Join(home, "outside", "apps.toml")); !os.IsNotExist(err) {
					t.Fatalf("write escaped through replaced directory: %v\n%s", err, output)
				}
				if !adopted && replacement == `mkdir "$config_dir"` {
					if _, err := os.Lstat(config.Path(home)); !os.IsNotExist(err) {
						t.Fatalf("write followed replacement directory: %v", err)
					}
				}
			})
		}
	}
}

func TestInstallerConfigDirectoryCreationRace(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprint(symlink), func(t *testing.T) {
			fingerprint := strings.Repeat("A", 40)
			cmd, _, home := installerCommand(t, fingerprint, "[GNUPG:] VALIDSIG "+fingerprint+" 0 0 0 0 0 0 0 0 0\n", "0")
			create := `command mkdir "$config_dir"`
			if symlink {
				create = `ln -s "$HOME/outside" "$config_dir"`
			}
			installerHook(t, cmd, "binary_installed=yes", `command mkdir "$HOME/outside"; mkdir() { if [ "$*" = "$config_dir" ]; then `+create+`; return 1; fi; command mkdir "$@"; }`)
			output, err := cmd.CombinedOutput()
			if (err != nil) != symlink {
				t.Fatalf("directory creation race: %v\n%s", err, output)
			}
			if !symlink {
				if data, err := os.ReadFile(config.Path(home)); err != nil || string(data) != config.Default {
					t.Fatalf("default not created: %q, %v", data, err)
				}
			}
			if _, err := os.Lstat(filepath.Join(home, "outside", "apps.toml")); !os.IsNotExist(err) {
				t.Fatalf("write escaped through directory creation race: %v", err)
			}
		})
	}
}

func TestInstallerAllowsSymlinkedConfigParent(t *testing.T) {
	fingerprint := strings.Repeat("A", 40)
	cmd, _, home := installerCommand(t, fingerprint, "[GNUPG:] VALIDSIG "+fingerprint+" 0 0 0 0 0 0 0 0 0\n", "0")
	parent := t.TempDir()
	if err := os.Symlink(parent, filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("installer rejected user-owned parent symlink: %v\n%s", err, output)
	}
	if data, err := os.ReadFile(filepath.Join(parent, "ops", "apps.toml")); err != nil || string(data) != config.Default {
		t.Fatalf("default not created: %q, %v", data, err)
	}
}

func TestInstallerCleansRelativeTemporaryDirectory(t *testing.T) {
	fingerprint := strings.Repeat("A", 40)
	cmd, _, _ := installerCommand(t, fingerprint, "[GNUPG:] VALIDSIG "+fingerprint+" 0 0 0 0 0 0 0 0 0\n", "0")
	cmd.Dir = filepath.Dir(cmd.Args[1])
	tmpDir := filepath.Join(cmd.Dir, "relative-tmp")
	if err := os.Mkdir(tmpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(cmd.Env, "TMPDIR=relative-tmp")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("installer failed with relative TMPDIR: %v\n%s", err, output)
	}
	if entries, err := os.ReadDir(tmpDir); err != nil || len(entries) != 0 {
		t.Fatalf("installer did not clean temporary downloads: %v, %v", entries, err)
	}
}

func TestInstallerConfigPublicationRejectsRacedNonregularTargets(t *testing.T) {
	for _, hook := range []string{`ln -s /dev/null "$config"`, `mkfifo "$config"`} {
		t.Run(hook, func(t *testing.T) {
			fingerprint := strings.Repeat("A", 40)
			cmd, _, home := installerCommand(t, fingerprint, "[GNUPG:] VALIDSIG "+fingerprint+" 0 0 0 0 0 0 0 0 0\n", "0")
			installerHook(t, cmd, "        ln -T -- ./apps.toml", hook)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			bounded := exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
			bounded.Env = cmd.Env
			bounded.WaitDelay = time.Second
			output, err := bounded.CombinedOutput()
			if err == nil || ctx.Err() != nil || strings.Contains(string(output), "Created ~/.config") || strings.Contains(string(output), "could not write") {
				t.Fatalf("unsafe publication: %v, %v\n%s", err, ctx.Err(), output)
			}
			info, err := os.Lstat(config.Path(home))
			if err != nil || info.Mode().IsRegular() {
				t.Fatalf("raced target changed: %v, %v", info, err)
			}
		})
	}
}
