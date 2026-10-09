package installer

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

var interruptionSignals = []syscall.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM}

// The foreground helper acknowledges the exact boundary over fd 3 and blocks
// on fd 4. Signal delivery precedes release, so deferred shell traps must run
// before the next command. No scheduling delay is used to choose the boundary.
const signalBlock = `python3 -c 'import os, signal
for sig in (signal.SIGHUP, signal.SIGINT, signal.SIGTERM):
    signal.signal(sig, signal.SIG_IGN)
os.write(3, (str(os.getppid()) + "\n").encode())
os.read(4, 1)'
`

const signalCheckpoint = signalBlock + "printf 'POST-SIGNAL WORK\\n' >&2\n"

func signalRun(t *testing.T, cmd *exec.Cmd, sig syscall.Signal, recipient string) (string, int) {
	t.Helper()
	ready, readyChild, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	defer readyChild.Close()
	gateChild, gate, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer gateChild.Close()
	defer gate.Close()
	output, err := os.CreateTemp(t.TempDir(), "output-")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	cmd.Stdout, cmd.Stderr = output, output
	cmd.ExtraFiles = []*os.File{readyChild, gateChild}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	readyChild.Close()
	gateChild.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	defer func() {
		// This group contains only this fixture, including its blocked helper.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if !waited {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("fixture reap timed out")
			}
		}
	}()
	checkpoint := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(ready).ReadString('\n'); checkpoint <- line }()
	var line string
	select {
	case line = <-checkpoint:
	case <-time.After(10 * time.Second):
		t.Fatal("signal checkpoint timed out")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		data, _ := os.ReadFile(output.Name())
		t.Fatalf("checkpoint %q: %v\n%s", line, err, data)
	}
	switch recipient {
	case "parent":
		pid = cmd.Process.Pid
	case "group":
		pid = -cmd.Process.Pid
	case "phase": // The helper's parent can be a command-substitution subshell.
	default:
		t.Fatalf("unknown recipient %q", recipient)
	}
	if err := syscall.Kill(pid, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		waited = true
	case <-time.After(10 * time.Second):
		t.Fatal("interrupted fixture did not exit")
	}
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(-cmd.Process.Pid, 0); err != syscall.ESRCH {
		t.Fatalf("fixture process group still exists after exit: %v", err)
	}
	return string(data), cmd.ProcessState.ExitCode()
}

func TestInstallerRecoverySignals(t *testing.T) {
	for _, mode := range []string{"restore-pending", "restore-failed", "verified", "restored"} {
		for _, sig := range interruptionSignals {
			t.Run(fmt.Sprintf("%s/%s", mode, sig), func(t *testing.T) {
				fp := publicationFingerprint
				cmd, target, home := installerCommand(t, fp, "[GNUPG:] VALIDSIG "+fp+" 0 0 0 0 0 0 0 0 0\n", "0")
				tmp := t.TempDir()
				cmd.Env = append(cmd.Env, "TMPDIR="+tmp)
				writeTestFile(t, target, "original binary", 0700)
				if mode != "verified" {
					installerHook(t, cmd, `if [ "$("$target" --version`, `printf '#!/bin/sh\nexit 1\n' > "$target"`)
				}
				switch mode {
				case "restore-pending":
					installerHook(t, cmd, `        if ! sudo -n mv -- "$backup"`, signalCheckpoint)
				case "restore-failed":
					installerHook(t, cmd, `        if ! sudo -n mv -- "$backup"`, `sudo() { if [ "$2" = mv ]; then return 1; fi; command sudo "$@"; }`)
					installerHook(t, cmd, `            fail "installation failed and the previous binary`, signalCheckpoint)
				case "verified":
					installerHook(t, cmd, "sudo -n rm -f -- \"$backup\"\n\nbinary_installed=yes", signalCheckpoint)
				case "restored":
					installerHook(t, cmd, `    fail 'installed binary verification failed;`, signalCheckpoint)
				}
				output, code := signalRun(t, cmd, sig, "parent")
				assertInterrupted(t, output, code, 128+int(sig))
				backups, _ := filepath.Glob(target + ".ops-backup-*")
				data, err := os.ReadFile(target)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "restore-pending" || mode == "restore-failed" {
					if len(backups) != 1 || !strings.Contains(output, backups[0]) {
						t.Fatalf("backup not retained/reported: %v\n%s", backups, output)
					}
					old, err := os.ReadFile(backups[0])
					if err != nil || string(old) != "original binary" {
						t.Fatalf("backup=%q %v", old, err)
					}
					if string(data) != "#!/bin/sh\nexit 1\n" {
						t.Fatalf("target=%q", data)
					}
				} else {
					if len(backups) != 0 {
						t.Fatalf("obsolete backup: %v", backups)
					}
					if mode == "restored" && string(data) != "original binary" || mode == "verified" && !strings.Contains(string(data), "ops 1.2.3") {
						t.Fatalf("durable target=%q", data)
					}
				}
				assertNoMatches(t, target+".ops-new-*")
				assertNoMatches(t, filepath.Join(home, ".config"))
				assertNoMatches(t, filepath.Join(tmp, "*"))
			})
		}
	}
}

func TestInstallerConfigurationSignals(t *testing.T) {
	for _, recipient := range []string{"phase", "group", "parent"} {
		for _, sig := range interruptionSignals {
			t.Run(fmt.Sprintf("%s/%s", recipient, sig), func(t *testing.T) {
				fp := publicationFingerprint
				cmd, target, home := installerCommand(t, fp, "[GNUPG:] VALIDSIG "+fp+" 0 0 0 0 0 0 0 0 0\n", "0")
				tmp := t.TempDir()
				cmd.Env = append(cmd.Env, "TMPDIR="+tmp)
				writeTestFile(t, target, "original binary", 0700)
				if recipient == "parent" {
					// The writer is already in flight. Parent-only interruption
					// must suppress publication after it finishes.
					installerHook(t, cmd, `        if ! cat > ./apps.toml`, `cat() { command cat; `+signalBlock+`}`)
					installerHook(t, cmd, `        ln -T -- /proc/$$/fd/8/apps.toml`, `printf 'POST-SIGNAL WORK\n' >&2`)
					installerHook(t, cmd, `        if ! cat > ./apps.toml`, `printf 'concurrent config' > "$config"`)
				} else {
					installerHook(t, cmd, `        trap - EXIT`, `printf 'concurrent config' > "$config"`+"\n"+signalCheckpoint)
				}
				output, code := signalRun(t, cmd, sig, recipient)
				want := 128 + int(sig)
				if recipient == "phase" {
					want = 2
				}
				assertInterrupted(t, output, code, want)
				data, err := os.ReadFile(filepath.Join(home, ".config", "ops", "apps.toml"))
				if err != nil || string(data) != "concurrent config" {
					t.Fatalf("concurrent file=%q %v", data, err)
				}
				data, err = os.ReadFile(target)
				if err != nil || !strings.Contains(string(data), "ops 1.2.3") {
					t.Fatalf("durable target=%q %v", data, err)
				}
				assertNoMatches(t, target+".ops-*")
				assertNoMatches(t, filepath.Join(home, ".config", "ops", ".ops-config.*"))
				assertNoMatches(t, filepath.Join(tmp, "*"))
			})
		}
	}
}

func TestPrepareReleaseSignals(t *testing.T) {
	for _, sig := range interruptionSignals {
		t.Run(sig.String(), func(t *testing.T) {
			f := newPrepareReleaseFixture(t, prepareReleaseFingerprint+"\n", false)
			cmd := f.command(nil)
			installerHook(t, cmd, `binary=$stage/ops-linux-x86_64`, signalCheckpoint)
			runFixtureGit(t, f.dir, "add", ".")
			runFixtureGit(t, f.dir, "commit", "--amend", "--no-edit", "-q")
			runFixtureGit(t, f.dir, "tag", "-f", "v1.0.0")
			output, code := signalRun(t, cmd, sig, "parent")
			assertInterrupted(t, output, code, 128+int(sig))
			assertNoMatches(t, filepath.Join(f.dir, "dist", "*"))
			if log := f.readGPGLog(t); log != "" {
				t.Fatalf("signing began after interruption: %s", log)
			}
		})
	}
}

func TestRenderInstallSignals(t *testing.T) {
	for _, phase := range []string{"gpg-group", "gpg-phase", "gpg-parent", "output"} {
		for _, sig := range interruptionSignals {
			t.Run(fmt.Sprintf("%s/%s", phase, sig), func(t *testing.T) {
				path, err := filepath.Abs("../../script/render-install.sh")
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				dir := t.TempDir()
				path = filepath.Join(dir, "render.sh")
				writeTestFile(t, path, string(data), 0700)
				out := filepath.Join(dir, "install")
				writeTestFile(t, out, "previous installer", 0600)
				cmd := exec.Command("sh", path, out)
				tmp := t.TempDir()
				cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
				recipient := "parent"
				if strings.HasPrefix(phase, "gpg-") {
					recipient = strings.TrimPrefix(phase, "gpg-")
					if recipient == "parent" {
						installerHook(t, cmd, `gpg --homedir "$gpg_home"`, signalBlock)
						installerHook(t, cmd, `printf '%s\n' "$shown"`, `printf 'POST-SIGNAL WORK\n' >&2`)
					} else {
						installerHook(t, cmd, `chmod 0700 "$gpg_home"`, signalCheckpoint)
					}
				} else {
					installerHook(t, cmd, `mv -- "$tmp" "$output"`, signalCheckpoint)
				}
				output, code := signalRun(t, cmd, sig, recipient)
				want := 128 + int(sig)
				if recipient == "phase" {
					want = 1
				}
				assertInterrupted(t, output, code, want)
				data, err = os.ReadFile(out)
				if err != nil || string(data) != "previous installer" {
					t.Fatalf("output published: %q %v", data, err)
				}
				assertNoMatches(t, out+".tmp.*")
				assertNoMatches(t, filepath.Join(tmp, "*"))
			})
		}
	}
}

func TestPublishReleaseSignals(t *testing.T) {
	for _, sig := range interruptionSignals {
		t.Run(sig.String(), func(t *testing.T) {
			f := newPublicationFixture(t, "1.2.3", true)
			tmp := t.TempDir()
			cmd := f.command("1.2.3", map[string]string{"TMPDIR": tmp})
			installerHook(t, cmd, `publish_immutable "$checksums_key"`, signalCheckpoint)
			runFixtureGit(t, f.dir, "add", ".")
			runFixtureGit(t, f.dir, "commit", "--amend", "--no-edit", "-q")
			runFixtureGit(t, f.dir, "tag", "-f", "v1.2.3")
			output, code := signalRun(t, cmd, sig, "parent")
			assertInterrupted(t, output, code, 128+int(sig))
			puts := f.putLog(t)
			if len(puts) != 1 || !strings.Contains(puts[0], "|releases/1.2.3/ops-linux-x86_64|") {
				t.Fatalf("later publication: %v", puts)
			}
			data, err := os.ReadFile(filepath.Join(f.state, "objects", "releases", "1.2.3", "ops-linux-x86_64"))
			if err != nil || !strings.Contains(string(data), "ops 1.2.3") {
				t.Fatalf("durable publication lost: %q %v", data, err)
			}
			assertNoMatches(t, filepath.Join(tmp, "*"))
			lock, err := os.OpenFile(filepath.Join(f.dir, ".git", "ops-publish.lock"), os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatalf("lock leaked: %v", err)
			}
		})
	}
}

func assertInterrupted(t *testing.T, output string, code, want int) {
	t.Helper()
	if code != want || strings.Contains(output, "POST-SIGNAL WORK") ||
		strings.Contains(output, "\nInstalled ops") || strings.Contains(output, "Prepared\n") ||
		strings.Contains(output, "Rendered installer:") || strings.Contains(output, "Published\n") {
		t.Fatalf("signal exit=%d want=%d\n%s", code, want, output)
	}
}

func assertNoMatches(t *testing.T, pattern string) {
	t.Helper()
	paths, err := filepath.Glob(pattern)
	if err != nil || len(paths) != 0 {
		t.Fatalf("unexpected files %s: %v %v", pattern, paths, err)
	}
}

func TestInstallerSignals(t *testing.T) {
	for _, boundary := range []struct {
		name, before     string
		replaced, backup bool
	}{
		{"before-mutation", "sudo -v ||", false, false},
		{"backup-created", `sudo -n mv -- "$staged" "$target"`, false, true},
		{"replacement-unverified", `if [ "$("$target" --version`, true, true},
	} {
		for _, sig := range interruptionSignals {
			t.Run(fmt.Sprintf("%s/%s", boundary.name, sig), func(t *testing.T) {
				fp := publicationFingerprint
				cmd, target, home := installerCommand(t, fp, "[GNUPG:] VALIDSIG "+fp+" 0 0 0 0 0 0 0 0 0\n", "0")
				writeTestFile(t, target, "original binary", 0700)
				tmp := t.TempDir()
				cmd.Env = append(cmd.Env, "TMPDIR="+tmp)
				installerHook(t, cmd, boundary.before, signalCheckpoint)
				output, code := signalRun(t, cmd, sig, "parent")
				backups, _ := filepath.Glob(target + ".ops-backup-*")
				data, _ := os.ReadFile(target)
				_, configErr := os.Stat(filepath.Join(home, ".config", "ops", "apps.toml"))
				t.Logf("status=%d target=%q backups=%v configExists=%v output=%q", code, data, backups, configErr == nil, output)
				assertInterrupted(t, output, code, 128+int(sig))
				if boundary.replaced {
					if !strings.Contains(string(data), "ops 1.2.3") {
						t.Fatalf("unexpected replacement %q", data)
					}
				} else if string(data) != "original binary" {
					t.Fatalf("previous target changed: %q", data)
				}
				if boundary.backup {
					if len(backups) != 1 {
						t.Fatalf("recovery backup missing: %v", backups)
					}
					data, err := os.ReadFile(backups[0])
					if err != nil || string(data) != "original binary" {
						t.Fatalf("backup: %q %v", data, err)
					}
				} else if len(backups) != 0 {
					t.Fatalf("unexpected backup %v", backups)
				}
				assertNoMatches(t, target+".ops-new-*")
				assertNoMatches(t, filepath.Join(tmp, "*"))
				if !os.IsNotExist(configErr) {
					t.Fatalf("configuration started: %v", configErr)
				}
			})
		}
	}
}

// The fake privilege boundary performs the rename, then blocks before it can
// report success. A signal must preserve the backup even if mv returns failure.
func TestInstallerReplacementCommandSignals(t *testing.T) {
	for _, sig := range interruptionSignals {
		t.Run(sig.String(), func(t *testing.T) {
			fp := publicationFingerprint
			cmd, target, home := installerCommand(t, fp, "[GNUPG:] VALIDSIG "+fp+" 0 0 0 0 0 0 0 0 0\n", "0")
			writeTestFile(t, target, "original binary", 0o700)
			tmp := t.TempDir()
			cmd.Env = append(cmd.Env, "TMPDIR="+tmp)
			fake := filepath.Join(filepath.Dir(cmd.Args[1]), "bin", "sudo")
			body, err := os.ReadFile(fake)
			if err != nil {
				t.Fatal(err)
			}
			hook := `if [ "$1" = mv ]; then
    "$@"
` + signalBlock + `    exit 1
fi
exec "$@"`
			writeTestFile(t, fake, strings.Replace(string(body), `exec "$@"`, hook, 1), 0o700)
			installerHook(t, cmd, `if [ "$("$target" --version`, `printf 'POST-SIGNAL WORK\n' >&2`)
			output, code := signalRun(t, cmd, sig, "parent")
			assertInterrupted(t, output, code, 128+int(sig))
			backups, _ := filepath.Glob(target + ".ops-backup-*")
			if len(backups) != 1 || !strings.Contains(output, backups[0]) {
				t.Fatalf("backup=%v output=%s", backups, output)
			}
			data, err := os.ReadFile(backups[0])
			if err != nil || string(data) != "original binary" {
				t.Fatalf("backup=%q %v", data, err)
			}
			data, err = os.ReadFile(target)
			if err != nil || !strings.Contains(string(data), "ops 1.2.3") {
				t.Fatalf("target=%q %v", data, err)
			}
			assertNoMatches(t, target+".ops-new-*")
			assertNoMatches(t, filepath.Join(home, ".config"))
			assertNoMatches(t, filepath.Join(tmp, "*"))
		})
	}
}

func TestInstallerFailedRestoreRetainsBackup(t *testing.T) {
	fp := publicationFingerprint
	cmd, target, _ := installerCommand(t, fp, "[GNUPG:] VALIDSIG "+fp+" 0 0 0 0 0 0 0 0 0\n", "0")
	writeTestFile(t, target, "original binary", 0o700)
	installerHook(t, cmd, `if [ "$("$target" --version`, `printf '#!/bin/sh\nexit 1\n' > "$target"`)
	installerHook(t, cmd, `        if ! sudo -n mv -- "$backup"`, `sudo() { if [ "$2" = mv ]; then return 1; fi; command sudo "$@"; }`)
	output, err := cmd.CombinedOutput()
	backups, _ := filepath.Glob(target + ".ops-backup-*")
	if err == nil || len(backups) != 1 || !strings.Contains(string(output), "previous binary could not be restored; backup retained at "+backups[0]) {
		t.Fatalf("backup=%v err=%v output=%s", backups, err, output)
	}
	data, err := os.ReadFile(backups[0])
	if err != nil || string(data) != "original binary" {
		t.Fatalf("backup=%q %v", data, err)
	}
	assertNoMatches(t, target+".ops-new-*")
}

func TestInstallerPreservesEarlierRecoveryPaths(t *testing.T) {
	for _, suffix := range []string{"ops-backup", "ops-new"} {
		t.Run(suffix, func(t *testing.T) {
			fp := publicationFingerprint
			cmd, target, _ := installerCommand(t, fp, "[GNUPG:] VALIDSIG "+fp+" 0 0 0 0 0 0 0 0 0\n", "0")
			writeTestFile(t, target, "original binary", 0o700)
			installerHook(t, cmd, `suffix=$$`, `printf 'earlier recovery evidence' > "$target.`+suffix+`-$$"`)
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "installation staging path already exists") {
				t.Fatalf("err=%v output=%s", err, output)
			}
			paths, _ := filepath.Glob(target + "." + suffix + "-*")
			if len(paths) != 1 {
				t.Fatalf("earlier evidence lost: %v", paths)
			}
			data, err := os.ReadFile(paths[0])
			if err != nil || string(data) != "earlier recovery evidence" {
				t.Fatalf("evidence=%q %v", data, err)
			}
			data, err = os.ReadFile(target)
			if err != nil || string(data) != "original binary" {
				t.Fatalf("target=%q %v", data, err)
			}
		})
	}
}

func TestInstallerStageDescriptorRejectsReplacedPath(t *testing.T) {
	fp := publicationFingerprint
	cmd, _, home := installerCommand(t, fp, "[GNUPG:] VALIDSIG "+fp+" 0 0 0 0 0 0 0 0 0\n", "0")
	outside := filepath.Join(home, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(outside, "apps.toml"), "unrelated configuration", 0o600)
	installerHook(t, cmd, `    exec 8< "$config_stage"`, `rmdir "$config_stage"; ln -s "$HOME/outside" "$config_stage"`)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "configuration staging directory changed") {
		t.Fatalf("err=%v output=%s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(outside, "apps.toml"))
	if err != nil || string(data) != "unrelated configuration" {
		t.Fatalf("unrelated file=%q %v", data, err)
	}
	assertNoMatches(t, filepath.Join(home, ".config", "ops", "apps.toml"))
}

func TestReleasePreflightSignals(t *testing.T) {
	for _, workflow := range []string{"prepare", "publish"} {
		for _, sig := range interruptionSignals {
			t.Run(workflow+"/"+sig.String(), func(t *testing.T) {
				var cmd *exec.Cmd
				var dir, tag string
				var publication *publicationFixture
				if workflow == "prepare" {
					f := newPrepareReleaseFixture(t, prepareReleaseFingerprint+"\n", false)
					cmd, dir, tag = f.command(nil), f.dir, "v1.0.0"
				} else {
					publication = newPublicationFixture(t, "1.2.3", true)
					cmd, dir, tag = publication.command("1.2.3", nil), publication.dir, "v1.2.3"
				}
				cmd.Env = append(cmd.Env, "TMPDIR="+t.TempDir())
				installerHook(t, cmd, `root=$(git`, signalCheckpoint)
				runFixtureGit(t, dir, "add", ".")
				runFixtureGit(t, dir, "commit", "--amend", "--no-edit", "-q")
				runFixtureGit(t, dir, "tag", "-f", tag)
				output, code := signalRun(t, cmd, sig, "parent")
				t.Logf("status=%d output=%q", code, output)
				assertInterrupted(t, output, code, 128+int(sig))
				if publication != nil {
					assertNoAWS(t, publication)
				} else {
					assertNoMatches(t, filepath.Join(dir, "dist"))
				}
			})
		}
	}
}

func TestCIShellStartupSignals(t *testing.T) {
	for _, name := range []string{"test-minimal-arch.sh", "test-minimal-arch.sh/guest", "test-ci-arch.sh"} {
		for _, sig := range interruptionSignals {
			t.Run(name+"/"+sig.String(), func(t *testing.T) {
				data, err := os.ReadFile(filepath.Join("../../script", strings.TrimSuffix(name, "/guest")))
				if err != nil {
					t.Fatal(err)
				}
				// Execute only the exact startup prologue, before the FIRST runtime
				// guard. No bootstrap, sudo, package command, guest, or container runs.
				prefix, _, ok := strings.Cut(string(data), "\ntest ")
				if strings.HasSuffix(name, "/guest") {
					_, guest, found := strings.Cut(string(data), "runuser -u ops-test -- sh <<'TEST'\n")
					prefix, _, ok = strings.Cut(guest, "\ncd /home/ops-test")
					ok = ok && found
				}
				if !ok {
					t.Fatal("missing startup guard")
				}
				path := filepath.Join(t.TempDir(), "startup.sh")
				writeTestFile(t, path, prefix+"\n"+signalCheckpoint+"exit 0\n", 0o700)
				shell := "sh"
				if name == "test-ci-arch.sh" {
					shell = "bash"
				}
				output, code := signalRun(t, exec.Command(shell, path), sig, "parent")
				t.Logf("status=%d output=%q", code, output)
				assertInterrupted(t, output, code, 128+int(sig))
			})
		}
	}
}

func TestWorkflowShellStartupSignals(t *testing.T) {
	for _, name := range []string{"arch.yml", "ci.yml", "release.yml"} {
		data, err := os.ReadFile(filepath.Join("../../.github/workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		blocks := strings.Split(string(data), "        run: |\n")
		if len(blocks) < 2 {
			t.Fatalf("no shell blocks in %s", name)
		}
		for i, block := range blocks[1:] {
			// Execute only startup trap/comment lines before any workflow work.
			// This cannot boot a VM, use sudo, download, or upload an artifact.
			var prefix strings.Builder
			for _, line := range strings.Split(block, "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "trap ") {
					break
				}
				prefix.WriteString(line + "\n")
			}
			for _, sig := range interruptionSignals {
				t.Run(fmt.Sprintf("%s/step-%d/%s", name, i+1, sig), func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "workflow-startup.sh")
					writeTestFile(t, path, prefix.String()+signalCheckpoint+"exit 0\n", 0o700)
					output, code := signalRun(t, exec.Command("bash", path), sig, "parent")
					assertInterrupted(t, output, code, 128+int(sig))
				})
			}
		}
	}
}
