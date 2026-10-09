package installer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Fail only the named disposal, with an explicit status independent of UID or
// filesystem permissions. All other rm calls still execute the real utility.
// Installed at an acknowledged workflow boundary, never in production sources.
func failCleanupRM(arguments string) string {
	return `rm() {
    if [ "$*" = "` + arguments + `" ]; then
        printf '%s\n' 'INJECTED cleanup rm failure (73)' >&2
        return 73
    fi
    command rm "$@"
}
`
}

func assertCleanupWarning(t *testing.T, output, warning string) {
	t.Helper()
	if strings.Count(output, "INJECTED cleanup rm failure (73)") != 1 || strings.Count(output, warning) != 1 {
		t.Fatalf("expected exactly one failed disposal and warning %q:\n%s", warning, output)
	}
}

func assertOneMatch(t *testing.T, pattern string) string {
	t.Helper()
	paths, err := filepath.Glob(pattern)
	if err != nil || len(paths) != 1 {
		t.Fatalf("expected one retained path %s: %v %v", pattern, paths, err)
	}
	return paths[0]
}

func TestInstallerCleanupFailureSignals(t *testing.T) {
	for _, sig := range interruptionSignals {
		t.Run(sig.String(), func(t *testing.T) {
			fp := publicationFingerprint
			cmd, target, home := installerCommand(t, fp, "[GNUPG:] VALIDSIG "+fp+" 0 0 0 0 0 0 0 0 0\n", "0")
			writeTestFile(t, target, "original binary\n\x00recovery bytes", 0700)
			tmp := t.TempDir()
			cmd.Env = append(cmd.Env, "TMPDIR="+tmp)
			// Replacement has completed. Recreate disposable stage contents to
			// prove its later cleanup actually runs after download cleanup fails.
			installerHook(t, cmd, `if [ "$("$target" --version`, failCleanupRM(`-rf -- $tmp`)+
				`printf disposable > "$staged"`+"\n"+signalCheckpoint)
			output, code := signalRun(t, cmd, sig, "parent")
			assertInterrupted(t, output, code, 128+int(sig))
			assertCleanupWarning(t, output, "ops installer: warning: temporary download cleanup failed")
			backup := assertOneMatch(t, target+".ops-backup-*")
			data, err := os.ReadFile(backup)
			if err != nil || string(data) != "original binary\n\x00recovery bytes" || !strings.Contains(output, "backup retained at "+backup) {
				t.Fatalf("recovery bytes=%q err=%v output=%s", data, err, output)
			}
			data, err = os.ReadFile(target)
			if err != nil || !strings.Contains(string(data), "ops 1.2.3") {
				t.Fatalf("replacement=%q %v", data, err)
			}
			assertOneMatch(t, filepath.Join(tmp, "ops-install.*"))
			assertNoMatches(t, target+".ops-new-*")
			assertNoMatches(t, filepath.Join(home, ".config"))
		})
	}
}

func TestInstallerConfigurationCleanupFailureSignals(t *testing.T) {
	for _, recipient := range []string{"parent", "phase"} {
		for _, sig := range interruptionSignals {
			t.Run(recipient+"/"+sig.String(), func(t *testing.T) {
				fp := publicationFingerprint
				cmd, _, home := installerCommand(t, fp, "[GNUPG:] VALIDSIG "+fp+" 0 0 0 0 0 0 0 0 0\n", "0")
				tmp := t.TempDir()
				cmd.Env = append(cmd.Env, "TMPDIR="+tmp)
				warning := "ops installer: warning: temporary download cleanup failed"
				if recipient == "parent" {
					// Failure of the first parent disposal must not skip the later
					// descriptor-pinned configuration file and directory cleanup.
					installerHook(t, cmd, `    result=0`, failCleanupRM(`-rf -- $tmp`))
					installerHook(t, cmd, `        if ! cat > ./apps.toml`, `cat() { command cat; `+signalBlock+`}`)
				} else {
					warning = "ops installer: warning: private configuration cleanup failed"
					installerHook(t, cmd, `        trap - EXIT`, failCleanupRM(`-f -- ./apps.toml`)+signalCheckpoint)
					// Observe the real writer status after its EXIT trap; the parent
					// must still remove the file the writer could not dispose of.
					installerHook(t, cmd, `    if [ "$result" -eq 0 ]; then`, `printf 'writer status=%s\n' "$result" >&2`)
				}
				installerHook(t, cmd, `        if ! cat > ./apps.toml`, `printf 'unrelated config' > "$config"`)
				installerHook(t, cmd, `        ln -T -- /proc/$$/fd/8/apps.toml`, `printf 'POST-SIGNAL WORK\n' >&2`)
				output, code := signalRun(t, cmd, sig, recipient)
				want := 128 + int(sig)
				if recipient == "phase" {
					want = 2
					if !strings.Contains(output, "writer status=2") {
						t.Fatalf("writer status replaced: %s", output)
					}
					assertNoMatches(t, filepath.Join(tmp, "*"))
				} else {
					assertOneMatch(t, filepath.Join(tmp, "ops-install.*"))
				}
				assertInterrupted(t, output, code, want)
				assertCleanupWarning(t, output, warning)
				data, err := os.ReadFile(filepath.Join(home, ".config", "ops", "apps.toml"))
				if err != nil || string(data) != "unrelated config" {
					t.Fatalf("config=%q %v", data, err)
				}
				assertNoMatches(t, filepath.Join(home, ".config", "ops", ".ops-config.*"))
			})
		}
	}
}

func TestRenderCleanupFailureSignals(t *testing.T) {
	for _, phase := range []string{"output", "gpg-parent", "gpg-group", "gpg-phase"} {
		for _, sig := range interruptionSignals {
			t.Run(phase+"/"+sig.String(), func(t *testing.T) {
				data, err := os.ReadFile("../../script/render-install.sh")
				if err != nil {
					t.Fatal(err)
				}
				dir := t.TempDir()
				path := filepath.Join(dir, "render.sh")
				writeTestFile(t, path, string(data), 0700)
				out := filepath.Join(dir, "install")
				writeTestFile(t, out, "previous installer", 0600)
				tmp := t.TempDir()
				unrelated := filepath.Join(tmp, "pre-existing")
				writeTestFile(t, unrelated, "unrelated bytes", 0600)
				cmd := exec.Command("sh", path, out)
				cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
				recipient := "parent"
				warning := "render-install: warning: temporary output cleanup failed"
				if phase == "output" {
					installerHook(t, cmd, `mv -- "$tmp" "$output"`, failCleanupRM(`-f -- $tmp`)+signalCheckpoint)
				} else {
					recipient = strings.TrimPrefix(phase, "gpg-")
					warning = "render-install: warning: verification keyring cleanup failed"
					block := signalCheckpoint
					if recipient == "parent" {
						block = signalBlock // The unsignalled child may finish its work.
					}
					installerHook(t, cmd, `gpg --homedir "$gpg_home"`, failCleanupRM(`-rf -- $gpg_home`)+block)
					installerHook(t, cmd, `printf '%s\n' "$shown"`, `printf 'POST-SIGNAL WORK\n' >&2`)
					// Preserve the caller's existing failure mapping, while recording
					// the actual command-substitution exit status before it is mapped.
					body, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					old := ") || fail 'invalid signing public key'"
					if strings.Count(string(body), old) != 1 {
						t.Fatal("missing GPG result boundary")
					}
					writeTestFile(t, path, strings.Replace(string(body), old, `) || { printf 'keyring status=%s\n' "$?" >&2; fail 'invalid signing public key'; }`, 1), 0700)
				}
				output, code := signalRun(t, cmd, sig, recipient)
				want := 128 + int(sig)
				if recipient == "phase" {
					want = 1
					if !strings.Contains(output, fmt.Sprintf("keyring status=%d", 128+int(sig))) {
						t.Fatalf("keyring status replaced: %s", output)
					}
				}
				assertInterrupted(t, output, code, want)
				assertCleanupWarning(t, output, warning)
				data, err = os.ReadFile(out)
				if err != nil || string(data) != "previous installer" {
					t.Fatalf("output=%q %v", data, err)
				}
				data, err = os.ReadFile(unrelated)
				if err != nil || string(data) != "unrelated bytes" {
					t.Fatalf("unrelated=%q %v", data, err)
				}
				if phase == "output" {
					assertOneMatch(t, out+".tmp.*")
					assertNoMatches(t, filepath.Join(tmp, "ops-render-gpg.*"))
				} else {
					assertOneMatch(t, filepath.Join(tmp, "ops-render-gpg.*"))
					assertNoMatches(t, out+".tmp.*")
				}
			})
		}
	}
}

func TestReleaseCleanupFailureSignals(t *testing.T) {
	for _, workflow := range []string{"prepare", "publish"} {
		for _, sig := range interruptionSignals {
			t.Run(workflow+"/"+sig.String(), func(t *testing.T) {
				var cmd *exec.Cmd
				var dir, tag, retained, warning string
				var prepare *prepareReleaseFixture
				var publication *publicationFixture
				tmp := t.TempDir()
				if workflow == "prepare" {
					prepare = newPrepareReleaseFixture(t, prepareReleaseFingerprint+"\n", false)
					cmd, dir, tag = prepare.command(nil), prepare.dir, "v1.0.0"
					installerHook(t, cmd, `binary=$stage/ops-linux-x86_64`, failCleanupRM(`-rf -- $stage`)+signalCheckpoint)
					retained = filepath.Join(dir, "dist", ".ops-release.*")
					warning = "prepare-release: warning: release staging cleanup failed"
				} else {
					publication = newPublicationFixture(t, "1.2.3", true)
					cmd, dir, tag = publication.command("1.2.3", nil), publication.dir, "v1.2.3"
					installerHook(t, cmd, `publish_immutable "$checksums_key"`, failCleanupRM(`-rf -- $tmp`)+signalCheckpoint)
					retained = filepath.Join(tmp, "ops-publish.*")
					warning = "publish-release: warning: publication staging cleanup failed"
				}
				cmd.Env = append(cmd.Env, "TMPDIR="+tmp)
				if err := os.MkdirAll(filepath.Join(dir, "dist"), 0700); err != nil {
					t.Fatal(err)
				}
				unrelated := filepath.Join(dir, "dist", "pre-existing")
				writeTestFile(t, unrelated, "unrelated bytes", 0600)
				runFixtureGit(t, dir, "add", ".")
				runFixtureGit(t, dir, "commit", "--amend", "--no-edit", "-q")
				runFixtureGit(t, dir, "tag", "-f", tag)
				output, code := signalRun(t, cmd, sig, "parent")
				assertInterrupted(t, output, code, 128+int(sig))
				assertCleanupWarning(t, output, warning)
				assertOneMatch(t, retained)
				data, err := os.ReadFile(unrelated)
				if err != nil || string(data) != "unrelated bytes" {
					t.Fatalf("unrelated=%q %v", data, err)
				}
				if prepare != nil {
					assertNoMatches(t, filepath.Join(dir, "dist", "release-*"))
					if log := prepare.readGPGLog(t); log != "" {
						t.Fatalf("signing started: %s", log)
					}
				} else {
					puts := publication.putLog(t)
					if len(puts) != 1 || !strings.Contains(puts[0], "|releases/1.2.3/ops-linux-x86_64|") {
						t.Fatalf("later publication: %v", puts)
					}
					data, err := os.ReadFile(filepath.Join(publication.state, "objects", "releases", "1.2.3", "ops-linux-x86_64"))
					if err != nil || !strings.Contains(string(data), "ops 1.2.3") {
						t.Fatalf("durable publication=%q %v", data, err)
					}
					lock, err := os.OpenFile(filepath.Join(dir, ".git", "ops-publish.lock"), os.O_RDWR, 0600)
					if err != nil {
						t.Fatal(err)
					}
					defer lock.Close()
					if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
						t.Fatalf("lock leaked despite process exit: %v", err)
					}
				}
			})
		}
	}
}

// Exercise the exact production EXIT functions on ordinary success/failure as
// well: cleanup must not manufacture success or replace an existing failure.
func TestCleanupPreservesPrimaryStatus(t *testing.T) {
	for _, script := range []string{"install", "prepare-release", "publish-release", "render-install"} {
		for _, status := range []int{0, 7} {
			t.Run(fmt.Sprintf("%s/%d", script, status), func(t *testing.T) {
				body, err := os.ReadFile("../../script/" + script + ".sh")
				if err != nil {
					t.Fatal(err)
				}
				_, tail, ok := strings.Cut(string(body), "\ncleanup() {")
				if !ok {
					t.Fatal("missing cleanup")
				}
				functions, _, ok := strings.Cut(tail, "\ntrap on_exit ")
				if !ok {
					t.Fatal("missing EXIT registration")
				}
				dir := t.TempDir()
				path := filepath.Join(dir, "exit.sh")
				// Only disposable owned paths are set; installer privileged
				// cleanup and recovery paths remain empty in this probe.
				setup := `set -eu
staged=
backup=
backup_required=no
config_stage=
config_stage_open=no
tmp_parent=$1
tmp=$1/ops-install.probe
stage=$tmp
mkdir "$tmp"
` + failCleanupRM(`-rf -- $tmp`)
				if script == "render-install" {
					setup = strings.Replace(setup, "-rf -- $tmp", "-f -- $tmp", 1)
				}
				writeTestFile(t, path, setup+"\ncleanup() {"+functions+fmt.Sprintf("\ntrap on_exit EXIT\nexit %d\n", status), 0700)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "sh", path, dir)
				cmd.WaitDelay = time.Second
				output, _ := cmd.CombinedOutput()
				if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != status {
					t.Fatalf("primary status %d replaced: %v %s", status, cmd.ProcessState, output)
				}
				if strings.Count(string(output), "INJECTED cleanup rm failure (73)") != 1 || !strings.Contains(string(output), "warning:") {
					t.Fatalf("missing or repeated cleanup: %s", output)
				}
			})
		}
	}
}

func TestPublishManualCleanupFailure(t *testing.T) {
	f := newPublicationFixture(t, "1.2.3", true)
	cmd := f.command("1.2.3", map[string]string{"TMPDIR": t.TempDir()})
	installerHook(t, cmd, "cleanup || { trap - 0; exit 1; }", failCleanupRM(`-rf -- $tmp`))
	runFixtureGit(t, f.dir, "add", ".")
	runFixtureGit(t, f.dir, "commit", "--amend", "--no-edit", "-q")
	runFixtureGit(t, f.dir, "tag", "-f", "v1.2.3")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bounded := exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	bounded.Dir, bounded.Env = cmd.Dir, cmd.Env
	bounded.WaitDelay = time.Second
	output, _ := bounded.CombinedOutput()
	if bounded.ProcessState == nil || bounded.ProcessState.ExitCode() != 1 || strings.Contains(string(output), "Published\n") {
		t.Fatalf("manual cleanup contract changed: %v %s", bounded.ProcessState, output)
	}
	assertCleanupWarning(t, string(output), "publish-release: warning: publication staging cleanup failed")
	if puts := f.putLog(t); len(puts) != 5 || !strings.Contains(puts[4], "|releases/latest|") {
		t.Fatalf("publication order changed: %v", puts)
	}
}
