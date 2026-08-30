package installer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const prepareReleaseFingerprint = "EB564BFFD8F63A984BF72A0237A80EDB682BBBFD"

type prepareReleaseFixture struct {
	dir         string
	bin         string
	signingHome string
	gpgLog      string
	script      string
}

func TestPrepareReleaseUsesRepositoryFingerprint(t *testing.T) {
	t.Run("no environment fingerprint required", func(t *testing.T) {
		fixture := newPrepareReleaseFixture(t, prepareReleaseFingerprint+"\n", false)
		output, err := fixture.run(nil)
		if err != nil {
			t.Fatalf("prepare release: %v: %s", err, output)
		}
		fixture.assertPinnedFingerprintUsed(t)
	})

	t.Run("environment fingerprint cannot override repository", func(t *testing.T) {
		fixture := newPrepareReleaseFixture(t, prepareReleaseFingerprint+"\n", false)
		arbitrary := strings.Repeat("A", 40)
		output, err := fixture.run(map[string]string{"OPS_SIGNING_FINGERPRINT": arbitrary})
		if err != nil {
			t.Fatalf("prepare release with ignored override: %v: %s", err, output)
		}
		fixture.assertPinnedFingerprintUsed(t)
		if strings.Contains(fixture.readGPGLog(t), arbitrary) {
			t.Fatalf("environment fingerprint changed trusted key: %s", fixture.readGPGLog(t))
		}
	})
}

func TestPrepareReleaseRepositoryFingerprintFailsClosed(t *testing.T) {
	for name, fingerprint := range map[string]string{
		"lowercase":  strings.ToLower(prepareReleaseFingerprint) + "\n",
		"short":      prepareReleaseFingerprint[:39] + "\n",
		"extra line": prepareReleaseFingerprint + "\n" + prepareReleaseFingerprint + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newPrepareReleaseFixture(t, fingerprint, false)
			output, err := fixture.run(nil)
			assertPrepareFailure(t, output, err, "repository signing fingerprint must contain exactly one uppercase 40-hex fingerprint")
		})
	}

	t.Run("missing", func(t *testing.T) {
		fixture := newPrepareReleaseFixture(t, "", true)
		output, err := fixture.run(nil)
		assertPrepareFailure(t, output, err, "repository signing fingerprint is not a safe regular file")
	})

	t.Run("symlink", func(t *testing.T) {
		fixture := newPrepareReleaseFixture(t, prepareReleaseFingerprint+"\n", true)
		runFixtureGit(t, fixture.dir, "tag", "-d", "v1.0.0")
		target := filepath.Join(fixture.dir, "signing-fingerprint-target")
		writeTestFile(t, target, prepareReleaseFingerprint+"\n", 0o600)
		fingerprintPath := filepath.Join(fixture.dir, "internal", "release", "signing-fingerprint")
		if err := os.Symlink(target, fingerprintPath); err != nil {
			t.Fatal(err)
		}
		fixture.commitAndTag(t)
		output, err := fixture.run(nil)
		assertPrepareFailure(t, output, err, "repository signing fingerprint is not a safe regular file")
	})
}

func TestPrepareReleaseRequiresRepositoryPinnedSubkey(t *testing.T) {
	fixture := newPrepareReleaseFixture(t, prepareReleaseFingerprint+"\n", false)
	output, err := fixture.run(map[string]string{
		"OPS_TEST_AVAILABLE_FINGERPRINT": strings.Repeat("A", 40),
	})
	assertPrepareFailure(t, output, err, "pinned release-signing subkey is unavailable")
}

func TestPrepareReleaseRequiresDedicatedSigningHome(t *testing.T) {
	fixture := newPrepareReleaseFixture(t, prepareReleaseFingerprint+"\n", false)
	output, err := fixture.run(map[string]string{"OPS_SIGNING_GNUPGHOME": ""})
	assertPrepareFailure(t, output, err, "OPS_SIGNING_GNUPGHOME must be a safe dedicated signing home")
}

func newPrepareReleaseFixture(t *testing.T, fingerprint string, omitFingerprint bool) *prepareReleaseFixture {
	t.Helper()
	dir := t.TempDir()
	fixture := &prepareReleaseFixture{
		dir:         dir,
		bin:         t.TempDir(),
		signingHome: t.TempDir(),
		gpgLog:      filepath.Join(t.TempDir(), "gpg.log"),
		script:      filepath.Join(dir, "script", "prepare-release.sh"),
	}
	if err := os.MkdirAll(filepath.Join(dir, "script"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "release"), 0o700); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(releaseScriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, fixture.script, string(script), 0o700)
	writeTestFile(t, filepath.Join(dir, ".gitignore"), "/dist/\n", 0o600)
	if !omitFingerprint {
		writeTestFile(t, filepath.Join(dir, "internal", "release", "signing-fingerprint"), fingerprint, 0o600)
	}
	writeTestFile(t, filepath.Join(fixture.bin, "go"), fakePrepareReleaseGo, 0o700)
	writeTestFile(t, filepath.Join(fixture.bin, "gofmt"), "#!/bin/sh\nexit 0\n", 0o700)
	writeTestFile(t, filepath.Join(fixture.bin, "gpg"), fakePrepareReleaseGPG, 0o700)
	writeTestFile(t, fixture.gpgLog, "", 0o600)

	runFixtureGit(t, dir, "init", "-q")
	fixture.commitAndTag(t)
	return fixture
}

func (f *prepareReleaseFixture) commitAndTag(t *testing.T) {
	t.Helper()
	runFixtureGit(t, f.dir, "add", ".")
	runFixtureGit(t, f.dir, "commit", "-q", "-m", "fixture")
	runFixtureGit(t, f.dir, "tag", "v1.0.0")
}

func (f *prepareReleaseFixture) run(overrides map[string]string) ([]byte, error) {
	cmd := exec.Command("sh", f.script, "1.0.0")
	cmd.Dir = f.dir
	env := filteredPrepareReleaseEnv()
	values := map[string]string{
		"PATH":                           f.bin + ":" + os.Getenv("PATH"),
		"OPS_SIGNING_GNUPGHOME":          f.signingHome,
		"OPS_TEST_AVAILABLE_FINGERPRINT": prepareReleaseFingerprint,
		"OPS_TEST_GPG_LOG":               f.gpgLog,
	}
	for key, value := range overrides {
		values[key] = value
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	cmd.Env = env
	return cmd.CombinedOutput()
}

func filteredPrepareReleaseEnv() []string {
	var env []string
	for _, value := range os.Environ() {
		key := strings.SplitN(value, "=", 2)[0]
		if key == "PATH" || key == "OPS_SIGNING_FINGERPRINT" ||
			key == "OPS_SIGNING_GNUPGHOME" || strings.HasPrefix(key, "OPS_TEST_") {
			continue
		}
		env = append(env, value)
	}
	return env
}

func (f *prepareReleaseFixture) readGPGLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.gpgLog)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (f *prepareReleaseFixture) assertPinnedFingerprintUsed(t *testing.T) {
	t.Helper()
	log := f.readGPGLog(t)
	for _, want := range []string{
		"--list-secret-keys " + prepareReleaseFingerprint + "!",
		"--local-user " + prepareReleaseFingerprint + "!",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("GPG did not use repository fingerprint %q: %s", want, log)
		}
	}
}

func assertPrepareFailure(t *testing.T, output []byte, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(string(output), want) {
		t.Fatalf("output=%s err=%v, want failure containing %q", output, err, want)
	}
}

const fakePrepareReleaseGo = `#!/bin/sh
set -eu
case "${1:-}" in
    env)
        [ "${2:-}" = GOVERSION ]
        printf 'go1.26.7\n'
        ;;
    version)
        printf 'go version go1.26.7 linux/amd64\n'
        ;;
    mod|vet|test)
        exit 0
        ;;
    build)
        output=
        while [ "$#" -gt 0 ]; do
            case "$1" in
                -o) output=$2; shift 2 ;;
                *) shift ;;
            esac
        done
        [ -n "$output" ]
        printf '%s\n' '#!/bin/sh' "printf 'ops 1.0.0\\n'" > "$output"
        chmod 0700 "$output"
        ;;
    *)
        exit 1
        ;;
esac
`

const fakePrepareReleaseGPG = `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$OPS_TEST_GPG_LOG"
case " $* " in
    *" --list-secret-keys "*)
        for argument do requested=$argument; done
        requested=${requested%!}
        [ "$requested" = "$OPS_TEST_AVAILABLE_FINGERPRINT" ] || exit 2
        printf 'ssb:u:255:22:SUBKEY:0:0:::::s:::::ed25519::::0:\n'
        printf 'fpr:::::::::%s:\n' "$OPS_TEST_AVAILABLE_FINGERPRINT"
        ;;
    *" --detach-sign "*)
        signer=
        output=
        while [ "$#" -gt 0 ]; do
            case "$1" in
                --local-user) signer=$2; shift 2 ;;
                --output) output=$2; shift 2 ;;
                *) shift ;;
            esac
        done
        [ "$signer" = "$OPS_TEST_AVAILABLE_FINGERPRINT!" ] || exit 2
        printf 'test signature\n' > "$output"
        ;;
    *" --verify "*)
        printf '[GNUPG:] VALIDSIG %s 0 0 0 0 0 0 0 0 0\n' "$OPS_TEST_AVAILABLE_FINGERPRINT"
        ;;
    *)
        exit 1
        ;;
esac
`
