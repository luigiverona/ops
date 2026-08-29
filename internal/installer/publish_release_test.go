package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const publicationFingerprint = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type publicationFixture struct {
	dir, bin, state, log, script, version string
}

func publishScriptPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("../../script/publish-release.sh")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPublishReleasePOSIXSyntax(t *testing.T) {
	if output, err := exec.Command("sh", "-n", publishScriptPath(t)).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v: %s", err, output)
	}
}

func TestPublishReleasePreconditions(t *testing.T) {
	t.Run("invalid version", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		output, err := fixture.run("1.2", nil)
		assertPublishFailure(t, output, err, "usage: script/publish-release.sh VERSION")
	})

	t.Run("missing endpoint", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		output, err := fixture.run("1.2.3", map[string]string{"OPS_R2_ENDPOINT": ""})
		assertPublishFailure(t, output, err, "OPS_R2_ENDPOINT is required")
	})

	t.Run("malformed endpoint", func(t *testing.T) {
		tests := []string{
			"http://11111111111111111111111111111111.r2.cloudflarestorage.com",
			"https://user@11111111111111111111111111111111.r2.cloudflarestorage.com",
			"https://11111111111111111111111111111111.r2.cloudflarestorage.com/path",
			"https://11111111111111111111111111111111.r2.cloudflarestorage.com?query=yes",
			"https://11111111111111111111111111111111.r2.cloudflarestorage.com#fragment",
			"https://example.invalid/path/11111111111111111111111111111111.r2.cloudflarestorage.com",
			"https://short.r2.cloudflarestorage.com",
			"https://gggggggggggggggggggggggggggggggg.r2.cloudflarestorage.com",
		}
		for _, endpoint := range tests {
			fixture := newPublicationFixture(t, "1.2.3", true)
			output, err := fixture.run("1.2.3", map[string]string{"OPS_R2_ENDPOINT": endpoint})
			assertPublishFailure(t, output, err, "valid Cloudflare R2 HTTPS endpoint")
			assertNoAWS(t, fixture)
		}
	})

	t.Run("dirty repository", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		writeTestFile(t, filepath.Join(fixture.dir, "dirty"), "dirty\n", 0o600)
		output, err := fixture.run("1.2.3", nil)
		assertPublishFailure(t, output, err, "repository is not clean")
	})

	t.Run("missing tag", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", false)
		output, err := fixture.run("1.2.3", nil)
		assertPublishFailure(t, output, err, "exact intended tag v1.2.3 does not exist")
	})

	t.Run("tag points at another commit", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		runFixtureGit(t, fixture.dir, "tag", "-d", "v1.2.3")
		writeTestFile(t, filepath.Join(fixture.dir, "second"), "second\n", 0o600)
		runFixtureGit(t, fixture.dir, "add", "second")
		runFixtureGit(t, fixture.dir, "commit", "-q", "-m", "second")
		runFixtureGit(t, fixture.dir, "tag", "v1.2.3", "HEAD~1")
		output, err := fixture.run("1.2.3", nil)
		assertPublishFailure(t, output, err, "v1.2.3 does not identify the current commit")
	})
}

func TestPublishReleaseRejectsMissingAndUnsafeArtifacts(t *testing.T) {
	t.Run("missing prepared directory", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		if err := os.RemoveAll(filepath.Dir(fixture.releasePath("checksums.txt"))); err != nil {
			t.Fatal(err)
		}
		output, err := fixture.run("1.2.3", nil)
		assertPublishFailure(t, output, err, "is not a safe prepared release directory")
		assertNoPut(t, fixture)
	})

	t.Run("missing", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		if err := os.Remove(fixture.releasePath("checksums.txt.sig")); err != nil {
			t.Fatal(err)
		}
		output, err := fixture.run("1.2.3", nil)
		assertPublishFailure(t, output, err, "is not a safe regular file")
		assertNoPut(t, fixture)
	})

	t.Run("symlink", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		path := fixture.releasePath("checksums.txt.sig")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "signature")
		writeTestFile(t, outside, "signature\n", 0o600)
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		output, err := fixture.run("1.2.3", nil)
		assertPublishFailure(t, output, err, "is not a safe regular file")
		assertNoPut(t, fixture)
	})

	t.Run("symlinked dist ancestor", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		dist := filepath.Join(fixture.dir, "dist")
		outside := filepath.Join(t.TempDir(), "dist")
		if err := os.Rename(dist, outside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, dist); err != nil {
			t.Fatal(err)
		}
		output, err := fixture.run("1.2.3", nil)
		assertPublishFailure(t, output, err, "dist is not a safe release directory")
		assertNoPut(t, fixture)
	})
}

func TestPublishReleaseVerifiesPreparedReleaseBeforeMutation(t *testing.T) {
	tests := []struct {
		name, artifact, content, want string
	}{
		{"checksum mismatch", "ops-linux-x86_64", "#!/bin/sh\nprintf 'ops 1.2.3\\n'\n# changed\n", "checksum verification failed"},
		{"wrong binary version", "ops-linux-x86_64", "#!/bin/sh\nprintf 'ops 9.9.9\\n'\n", "unexpected version"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPublicationFixture(t, "1.2.3", true)
			path := fixture.releasePath(test.artifact)
			writeTestFile(t, path, test.content, 0o700)
			if test.name == "wrong binary version" {
				fixture.refreshManifest(t)
			}
			output, err := fixture.run("1.2.3", nil)
			assertPublishFailure(t, output, err, test.want)
			assertNoPut(t, fixture)
		})
	}
}

func TestPublishReleaseSigningTrustFailsClosed(t *testing.T) {
	t.Run("embedded key lacks pinned signing subkey", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		output, err := fixture.run("1.2.3", map[string]string{
			"OPS_TEST_FINGERPRINT": strings.Repeat("B", 40),
		})
		assertPublishFailure(t, output, err, "pinned signing subkey is absent")
		assertNoPut(t, fixture)
	})

	t.Run("signature has wrong signer", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		output, err := fixture.run("1.2.3", map[string]string{
			"OPS_TEST_GPG_SIGNER": strings.Repeat("B", 40),
		})
		assertPublishFailure(t, output, err, "prepared release signature status is invalid")
		assertNoPut(t, fixture)
	})

	t.Run("gpg verification process fails", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		output, err := fixture.run("1.2.3", map[string]string{
			"OPS_TEST_GPG_VERIFY_EXIT": "1",
		})
		assertPublishFailure(t, output, err, "prepared release signature verification failed")
		assertNoPut(t, fixture)
	})
}

func TestPublishReleaseFirstPublicationOrderMetadataAndPager(t *testing.T) {
	fixture := newPublicationFixture(t, "1.2.3", true)
	output, err := fixture.run("1.2.3", nil)
	if err != nil {
		t.Fatalf("publish failed: %v\n%s", err, output)
	}

	puts := fixture.putLog(t)
	wantKeys := []string{
		"releases/1.2.3/ops-linux-x86_64",
		"releases/1.2.3/checksums.txt",
		"releases/1.2.3/checksums.txt.sig",
		"install",
		"releases/latest",
	}
	if len(puts) != len(wantKeys) {
		t.Fatalf("put log=%q", puts)
	}
	for i, want := range wantKeys {
		fields := strings.Split(puts[i], "|")
		if len(fields) != 8 || fields[2] != want {
			t.Fatalf("put %d=%q, want key %q", i, puts[i], want)
		}
		if fields[7] != "pager=disabled" {
			t.Fatalf("AWS pager was not disabled: %q", puts[i])
		}
		if fields[6] != "profile=ops-r2" {
			t.Fatalf("unexpected default AWS profile: %q", puts[i])
		}
		if i < 3 && fields[5] != "*" {
			t.Fatalf("immutable put is not conditional: %q", puts[i])
		}
		if i == 3 && fields[5] != "" {
			t.Fatalf("installer put was unexpectedly conditional: %q", puts[i])
		}
		if i == 4 && fields[5] != "*" {
			t.Fatalf("initial latest put lacks its absence condition: %q", puts[i])
		}
	}

	assertPutMetadata(t, puts[0], "application/octet-stream", "public, max-age=31536000, immutable")
	assertPutMetadata(t, puts[1], "text/plain; charset=utf-8", "public, max-age=31536000, immutable")
	assertPutMetadata(t, puts[2], "application/pgp-signature", "public, max-age=31536000, immutable")
	assertPutMetadata(t, puts[3], "text/plain; charset=utf-8", "no-store")
	assertPutMetadata(t, puts[4], "text/plain; charset=utf-8", "no-store")

	log := fixture.readLog(t)
	if !strings.Contains(log, "curl|releases/latest|404") {
		t.Fatalf("first-release 404 was not observed: %s", log)
	}
}

func TestPublishReleaseExistingImmutableObject(t *testing.T) {
	t.Run("identical is accepted", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		fixture.seedObject(t, "releases/1.2.3/ops-linux-x86_64", fixture.releasePath("ops-linux-x86_64"), "application/octet-stream", "public, max-age=31536000, immutable")
		output, err := fixture.run("1.2.3", nil)
		if err != nil {
			t.Fatalf("retry failed: %v\n%s", err, output)
		}
		for _, line := range fixture.putLog(t) {
			if strings.Contains(line, "|releases/1.2.3/ops-linux-x86_64|") {
				t.Fatalf("existing immutable object was overwritten: %q", line)
			}
		}
	})

	t.Run("different bytes are rejected", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		other := filepath.Join(t.TempDir(), "other")
		writeTestFile(t, other, "different\n", 0o600)
		fixture.seedObject(t, "releases/1.2.3/ops-linux-x86_64", other, "application/octet-stream", "public, max-age=31536000, immutable")
		output, err := fixture.run("1.2.3", nil)
		assertPublishFailure(t, output, err, "R2 content verification failed")
		assertNoLatestPut(t, fixture)
	})

	t.Run("different metadata are rejected", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		fixture.seedObject(t, "releases/1.2.3/ops-linux-x86_64", fixture.releasePath("ops-linux-x86_64"), "text/plain", "public, max-age=31536000, immutable")
		output, err := fixture.run("1.2.3", nil)
		assertPublishFailure(t, output, err, "unexpected content type")
		assertNoLatestPut(t, fixture)
	})

	t.Run("different cache control is rejected", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		fixture.seedObject(t, "releases/1.2.3/ops-linux-x86_64", fixture.releasePath("ops-linux-x86_64"), "application/octet-stream", "no-store")
		output, err := fixture.run("1.2.3", nil)
		assertPublishFailure(t, output, err, "unexpected cache control")
		assertNoLatestPut(t, fixture)
	})
}

func TestPublishReleaseConditionalRaceIsVerified(t *testing.T) {
	fixture := newPublicationFixture(t, "1.2.3", true)
	key := "releases/1.2.3/ops-linux-x86_64"
	output, err := fixture.run("1.2.3", map[string]string{"OPS_TEST_AWS_RACE_KEY": key})
	if err != nil {
		t.Fatalf("raced publication failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Verified raced immutable object: "+key) {
		t.Fatalf("race was not verified: %s", output)
	}
}

func TestPublishReleaseConditionalRaceMismatchIsRejected(t *testing.T) {
	for _, mode := range []string{"bytes", "metadata"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newPublicationFixture(t, "1.2.3", true)
			key := "releases/1.2.3/ops-linux-x86_64"
			output, err := fixture.run("1.2.3", map[string]string{
				"OPS_TEST_AWS_RACE_KEY":  key,
				"OPS_TEST_AWS_RACE_MODE": mode,
			})
			if mode == "bytes" {
				assertPublishFailure(t, output, err, "R2 content verification failed")
			} else {
				assertPublishFailure(t, output, err, "unexpected cache control")
			}
			assertNoLatestPut(t, fixture)
		})
	}
}

func TestPublishReleasePartialPublicationCanBeRetried(t *testing.T) {
	fixture := newPublicationFixture(t, "1.2.3", true)
	failKey := "releases/1.2.3/checksums.txt.sig"
	output, err := fixture.run("1.2.3", map[string]string{"OPS_TEST_AWS_FAIL_PUT_KEY": failKey})
	assertPublishFailure(t, output, err, "could not publish immutable object")
	assertNoLatestPut(t, fixture)
	for _, key := range []string{"releases/1.2.3/ops-linux-x86_64", "releases/1.2.3/checksums.txt"} {
		if _, err := os.Stat(filepath.Join(fixture.state, "objects", filepath.FromSlash(key))); err != nil {
			t.Fatalf("partial object %s missing: %v", key, err)
		}
	}

	if err := os.WriteFile(fixture.log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err = fixture.run("1.2.3", nil)
	if err != nil {
		t.Fatalf("safe retry failed: %v\n%s", err, output)
	}
	assertLatestIsLastPut(t, fixture)
}

func TestPublishReleaseUsesStagedVerifiedArtifacts(t *testing.T) {
	fixture := newPublicationFixture(t, "1.2.3", true)
	original, err := os.ReadFile(fixture.releasePath("ops-linux-x86_64"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := fixture.run("1.2.3", map[string]string{
		"OPS_TEST_MUTATE_DIST_AFTER_STAGE": "yes",
	})
	if err != nil {
		t.Fatalf("publish failed after dist mutation: %v\n%s", err, output)
	}
	published, err := os.ReadFile(filepath.Join(fixture.state, "objects", "releases", "1.2.3", "ops-linux-x86_64"))
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != string(original) {
		t.Fatal("publication reread the mutated dist binary instead of using the verified snapshot")
	}
	mutated, err := os.ReadFile(fixture.releasePath("ops-linux-x86_64"))
	if err != nil {
		t.Fatal(err)
	}
	if string(mutated) == string(original) {
		t.Fatal("test did not mutate the original dist binary")
	}
}

func TestPublishReleaseFailureBeforeLatestDoesNotAdvertise(t *testing.T) {
	fixture := newPublicationFixture(t, "1.2.3", true)
	output, err := fixture.run("1.2.3", map[string]string{"OPS_TEST_CURL_FAIL_KEY": "install"})
	assertPublishFailure(t, output, err, "could not fetch public object install")
	assertNoLatestPut(t, fixture)
}

func TestPublishReleaseHostedLatestFailsClosed(t *testing.T) {
	tests := []struct {
		name, mode, want string
	}{
		{"network failure", "network", "could not inspect public latest release"},
		{"malformed body", "malformed", "public latest is inconsistent"},
		{"unexpected status", "500", "public latest is inconsistent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPublicationFixture(t, "1.2.3", true)
			output, err := fixture.run("1.2.3", map[string]string{"OPS_TEST_LATEST_MODE": test.mode})
			assertPublishFailure(t, output, err, test.want)
			assertNoPut(t, fixture)
		})
	}
}

func TestPublishReleaseAuthoritativeLatestFailsClosed(t *testing.T) {
	t.Run("authenticated read failure", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		output, err := fixture.run("1.2.3", map[string]string{
			"OPS_TEST_AWS_LATEST_HEAD_FAILURE": "yes",
		})
		assertPublishFailure(t, output, err, "could not inspect authoritative R2 latest release")
		assertNoPut(t, fixture)
	})

	t.Run("unexpected mutable metadata", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		current := filepath.Join(t.TempDir(), "latest")
		writeTestFile(t, current, "1.2.2\n", 0o600)
		fixture.seedObject(t, "releases/latest", current, "text/plain; charset=utf-8", "public, max-age=60")
		output, err := fixture.run("1.2.3", nil)
		assertPublishFailure(t, output, err, "authoritative R2 latest has unexpected cache control")
		assertNoPut(t, fixture)
	})
}

func TestPublishReleaseSemanticRollbackProtection(t *testing.T) {
	tests := []struct {
		name, current string
		wantSuccess   bool
	}{
		{"downgrade rejected", "2.0.0", false},
		{"same version retry", "1.2.3", true},
		{"newer version", "1.2.2", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPublicationFixture(t, "1.2.3", true)
			current := filepath.Join(t.TempDir(), "latest")
			writeTestFile(t, current, test.current+"\n", 0o600)
			fixture.seedObject(t, "releases/latest", current, "text/plain; charset=utf-8", "no-store")
			output, err := fixture.run("1.2.3", nil)
			if test.wantSuccess && err != nil {
				t.Fatalf("publish failed: %v\n%s", err, output)
			}
			if !test.wantSuccess {
				assertPublishFailure(t, output, err, "refusing to move latest backward")
				assertNoPut(t, fixture)
			} else {
				assertLatestIsLastPut(t, fixture)
			}
		})
	}
}

func TestPublishReleaseVeryLargeSemanticVersionComponents(t *testing.T) {
	version := "100000000000000000000000000000000000000.0.0"
	fixture := newPublicationFixture(t, version, true)
	current := filepath.Join(t.TempDir(), "latest")
	writeTestFile(t, current, "99999999999999999999999999999999999999.999.999\n", 0o600)
	fixture.seedObject(t, "releases/latest", current, "text/plain; charset=utf-8", "no-store")
	output, err := fixture.run(version, nil)
	if err != nil {
		t.Fatalf("large semantic version was not compared correctly: %v\n%s", err, output)
	}
	assertLatestIsLastPut(t, fixture)
}

func TestPublishReleaseLatestCompareAndSwapRejectsConcurrentChange(t *testing.T) {
	fixture := newPublicationFixture(t, "1.2.3", true)
	current := filepath.Join(t.TempDir(), "latest")
	writeTestFile(t, current, "1.2.2\n", 0o600)
	fixture.seedObject(t, "releases/latest", current, "text/plain; charset=utf-8", "no-store")
	output, err := fixture.run("1.2.3", map[string]string{
		"OPS_TEST_AWS_CHANGE_LATEST_BEFORE_PUT": "yes",
	})
	assertPublishFailure(t, output, err, "conditional latest update failed")
	latest, err := os.ReadFile(filepath.Join(fixture.state, "objects", "releases", "latest"))
	if err != nil {
		t.Fatal(err)
	}
	if string(latest) != "9.9.9\n" {
		t.Fatalf("concurrent latest state was overwritten: %q", latest)
	}
}

func TestPublishReleaseLocalLock(t *testing.T) {
	t.Run("active lock rejects second publisher", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		lockPath := filepath.Join(fixture.dir, ".git", "ops-publish.lock")
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		}()
		output, runErr := fixture.run("1.2.3", nil)
		assertPublishFailure(t, output, runErr, "another local release publication is already running")
		assertNoAWS(t, fixture)
	})

	t.Run("unlocked persistent file is not stale", func(t *testing.T) {
		fixture := newPublicationFixture(t, "1.2.3", true)
		writeTestFile(t, filepath.Join(fixture.dir, ".git", "ops-publish.lock"), "", 0o600)
		output, err := fixture.run("1.2.3", nil)
		if err != nil {
			t.Fatalf("inert lock file blocked publication: %v\n%s", err, output)
		}
	})
}

func newPublicationFixture(t *testing.T, version string, tag bool) *publicationFixture {
	t.Helper()
	dir := t.TempDir()
	fixture := &publicationFixture{
		dir: dir, bin: filepath.Join(dir, "fake-bin"), state: filepath.Join(dir, "r2"),
		log: filepath.Join(dir, "calls.log"), script: filepath.Join(dir, "script", "publish-release.sh"), version: version,
	}
	for _, path := range []string{
		filepath.Join(dir, "script"), filepath.Join(dir, "internal", "release"),
		fixture.bin, filepath.Join(fixture.state, "objects"), filepath.Join(fixture.state, "meta"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	publisher, err := os.ReadFile(publishScriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.script, publisher, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, ".gitignore"), "/dist/\n/r2/\n/calls.log\n", 0o600)
	writeTestFile(t, filepath.Join(dir, "script", "install-fixture"), "rendered installer\n", 0o600)
	writeTestFile(t, filepath.Join(dir, "script", "render-install.sh"), `#!/bin/sh
set -eu
if [ "${OPS_TEST_MUTATE_DIST_AFTER_STAGE:-}" = yes ]; then
    printf '%s\n' '#!/bin/sh' "printf 'ops compromised\\n'" > "dist/release-v$OPS_TEST_VERSION/ops-linux-x86_64"
    chmod 0700 "dist/release-v$OPS_TEST_VERSION/ops-linux-x86_64"
fi
cp script/install-fixture "$1"
chmod 0755 "$1"
`, 0o700)
	writeTestFile(t, filepath.Join(dir, "internal", "release", "signing-fingerprint"), publicationFingerprint+"\n", 0o600)
	writeTestFile(t, filepath.Join(dir, "internal", "release", "signing-key.asc"), "test public key\n", 0o600)
	writeTestFile(t, filepath.Join(fixture.bin, "gpg"), fakePublicationGPG, 0o700)
	writeTestFile(t, filepath.Join(fixture.bin, "aws"), fakePublicationAWS, 0o700)
	writeTestFile(t, filepath.Join(fixture.bin, "curl"), fakePublicationCurl, 0o700)
	writeTestFile(t, fixture.log, "", 0o600)

	runFixtureGit(t, dir, "init", "-q")
	runFixtureGit(t, dir, "add", ".")
	runFixtureGit(t, dir, "commit", "-q", "-m", "fixture")
	if tag {
		runFixtureGit(t, dir, "tag", "v"+version)
	}

	releaseDir := filepath.Join(dir, "dist", "release-v"+version)
	if err := os.MkdirAll(releaseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(releaseDir, "ops-linux-x86_64"), "#!/bin/sh\nprintf 'ops "+version+"\\n'\n", 0o700)
	writeTestFile(t, filepath.Join(releaseDir, "checksums.txt.sig"), "test signature\n", 0o600)
	fixture.refreshManifest(t)
	return fixture
}

func (f *publicationFixture) refreshManifest(t *testing.T) {
	t.Helper()
	binary, err := os.ReadFile(f.releasePath("ops-linux-x86_64"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	writeTestFile(t, f.releasePath("checksums.txt"), hex.EncodeToString(sum[:])+"  ops-linux-x86_64\n", 0o600)
}

func (f *publicationFixture) releasePath(name string) string {
	return filepath.Join(f.dir, "dist", "release-v"+f.version, name)
}

func (f *publicationFixture) run(version string, overrides map[string]string) ([]byte, error) {
	cmd := exec.Command("sh", f.script, version)
	cmd.Dir = f.dir
	env := filteredPublicationEnv()
	values := map[string]string{
		"PATH":                 f.bin + ":" + os.Getenv("PATH"),
		"OPS_R2_ENDPOINT":      "https://11111111111111111111111111111111.r2.cloudflarestorage.com",
		"OPS_TEST_STATE":       f.state,
		"OPS_TEST_LOG":         f.log,
		"OPS_TEST_FINGERPRINT": publicationFingerprint,
		"OPS_TEST_LATEST_MODE": "404",
		"OPS_TEST_VERSION":     version,
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

func filteredPublicationEnv() []string {
	var env []string
	for _, value := range os.Environ() {
		key := strings.SplitN(value, "=", 2)[0]
		if key == "PATH" || key == "AWS_PAGER" || key == "OPS_R2_ENDPOINT" ||
			key == "OPS_R2_PROFILE" || strings.HasPrefix(key, "OPS_TEST_") {
			continue
		}
		env = append(env, value)
	}
	return env
}

func (f *publicationFixture) seedObject(t *testing.T, key, source, contentType, cacheControl string) {
	t.Helper()
	object := filepath.Join(f.state, "objects", filepath.FromSlash(key))
	meta := filepath.Join(f.state, "meta", filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(object), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(meta), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(object, data, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, meta+".type", contentType+"\n", 0o600)
	writeTestFile(t, meta+".cache", cacheControl+"\n", 0o600)
	writeTestFile(t, meta+".etag", "\"seed-etag\"\n", 0o600)
}

func (f *publicationFixture) readLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (f *publicationFixture) putLog(t *testing.T) []string {
	t.Helper()
	var puts []string
	for _, line := range strings.Split(f.readLog(t), "\n") {
		if strings.HasPrefix(line, "aws|put-object|") {
			puts = append(puts, line)
		}
	}
	return puts
}

func assertPublishFailure(t *testing.T, output []byte, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(string(output), want) {
		t.Fatalf("output=%s err=%v, want failure containing %q", output, err, want)
	}
}

func assertNoPut(t *testing.T, fixture *publicationFixture) {
	t.Helper()
	if puts := fixture.putLog(t); len(puts) != 0 {
		t.Fatalf("unexpected mutation: %q", puts)
	}
}

func assertNoAWS(t *testing.T, fixture *publicationFixture) {
	t.Helper()
	for _, line := range strings.Split(fixture.readLog(t), "\n") {
		if strings.HasPrefix(line, "aws|") {
			t.Fatalf("AWS was invoked unexpectedly: %q", line)
		}
	}
}

func assertNoLatestPut(t *testing.T, fixture *publicationFixture) {
	t.Helper()
	for _, line := range fixture.putLog(t) {
		if strings.Contains(line, "|releases/latest|") {
			t.Fatalf("latest was updated after a prior failure: %q", line)
		}
	}
}

func assertLatestIsLastPut(t *testing.T, fixture *publicationFixture) {
	t.Helper()
	puts := fixture.putLog(t)
	if len(puts) == 0 || !strings.Contains(puts[len(puts)-1], "|releases/latest|") {
		t.Fatalf("latest is not the final put: %q", puts)
	}
}

func assertPutMetadata(t *testing.T, line, contentType, cacheControl string) {
	t.Helper()
	fields := strings.Split(line, "|")
	if len(fields) != 8 || fields[3] != contentType || fields[4] != cacheControl {
		t.Fatalf("metadata mismatch in %q, want type=%q cache=%q", line, contentType, cacheControl)
	}
}

func runFixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	base := []string{"-c", "user.name=ops test", "-c", "user.email=ops@example.invalid"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func writeTestFile(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

const fakePublicationGPG = `#!/bin/sh
set -eu
case " $* " in
    *" --show-keys "*)
        printf 'pub:u:255:22:PRIMARY:0:0:::::c:::::ed25519::::0:\n'
        printf 'fpr:::::::::CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC:\n'
        printf 'sub:u:255:22:SUBKEY:0:0:::::s:::::ed25519::::0:\n'
        printf 'fpr:::::::::%s:\n' "$OPS_TEST_FINGERPRINT"
        ;;
    *" --import "*) exit 0 ;;
    *" --verify "*)
        printf '[GNUPG:] VALIDSIG %s 0 0 0 0 0 0 0 0 0\n' "${OPS_TEST_GPG_SIGNER:-$OPS_TEST_FINGERPRINT}"
        exit "${OPS_TEST_GPG_VERIFY_EXIT:-0}"
        ;;
    *) exit 1 ;;
esac
`

const fakePublicationAWS = `#!/bin/sh
set -eu
[ "${AWS_PAGER+x}" = x ] && [ -z "$AWS_PAGER" ] || {
    printf '%s\n' 'AWS_PAGER is not disabled' >&2
    exit 90
}
profile=
endpoint=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --profile) profile=$2; shift 2 ;;
        --endpoint-url) endpoint=$2; shift 2 ;;
        *) break ;;
    esac
done
[ "$1" = s3api ]
operation=$2
shift 2
key=
body=
content_type=
cache_control=
if_match=
if_none_match=
query=
destination=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --bucket) shift 2 ;;
        --key) key=$2; shift 2 ;;
        --body) body=$2; shift 2 ;;
        --content-type) content_type=$2; shift 2 ;;
        --cache-control) cache_control=$2; shift 2 ;;
        --if-match) if_match=$2; shift 2 ;;
        --if-none-match) if_none_match=$2; shift 2 ;;
        --query) query=$2; shift 2 ;;
        --output) shift 2 ;;
        --*) shift ;;
        *) destination=$1; shift ;;
    esac
done
object=$OPS_TEST_STATE/objects/$key
metadata=$OPS_TEST_STATE/meta/$key
condition=$if_none_match$if_match
printf 'aws|%s|%s|%s|%s|%s|profile=%s|pager=disabled\n' \
    "$operation" "$key" "$content_type" "$cache_control" "$condition" "$profile" >> "$OPS_TEST_LOG"

write_etag() {
    counter=$OPS_TEST_STATE/etag-counter
    if [ -f "$counter" ]; then
        number=$(cat "$counter")
    else
        number=0
    fi
    number=$((number + 1))
    printf '%s\n' "$number" > "$counter"
    printf '"fake-etag-%s"\n' "$number" > "$metadata.etag"
}

case "$operation" in
    head-object)
        if [ "$key" = releases/latest ] && [ "${OPS_TEST_AWS_LATEST_HEAD_FAILURE:-}" = yes ]; then
            printf 'simulated authenticated HeadObject failure\n' >&2
            exit 70
        fi
        if [ ! -f "$object" ]; then
            printf 'An error occurred (404) when calling HeadObject: Not Found\n' >&2
            exit 255
        fi
        case "$query" in
            ContentType) cat "$metadata.type" ;;
            CacheControl) cat "$metadata.cache" ;;
            '[ETag,ContentType,CacheControl]')
                printf '%s\t%s\t%s\n' \
                    "$(cat "$metadata.etag")" \
                    "$(cat "$metadata.type")" \
                    "$(cat "$metadata.cache")"
                ;;
            '') printf '{}\n' ;;
            *) exit 2 ;;
        esac
        ;;
    get-object)
        [ -f "$object" ] || exit 255
        if [ -n "$if_match" ] && [ "$(cat "$metadata.etag")" != "$if_match" ]; then
            printf 'PreconditionFailed\n' >&2
            exit 255
        fi
        cp "$object" "$destination"
        printf '{}\n'
        ;;
    put-object)
        if [ "${OPS_TEST_AWS_FAIL_PUT_KEY:-}" = "$key" ]; then
            printf 'simulated PutObject failure\n' >&2
            exit 70
        fi
        if [ "${OPS_TEST_AWS_RACE_KEY:-}" = "$key" ] && [ ! -f "$object" ]; then
            mkdir -p "$(dirname "$object")" "$(dirname "$metadata")"
            case "${OPS_TEST_AWS_RACE_MODE:-matching}" in
                bytes) printf 'different raced bytes\n' > "$object" ;;
                *) cp "$body" "$object" ;;
            esac
            printf '%s\n' "$content_type" > "$metadata.type"
            if [ "${OPS_TEST_AWS_RACE_MODE:-matching}" = metadata ]; then
                printf '%s\n' 'no-store' > "$metadata.cache"
            else
                printf '%s\n' "$cache_control" > "$metadata.cache"
            fi
            write_etag
            printf 'PreconditionFailed\n' >&2
            exit 255
        fi
        if [ "$key" = releases/latest ] && [ "${OPS_TEST_AWS_CHANGE_LATEST_BEFORE_PUT:-}" = yes ]; then
            mkdir -p "$(dirname "$object")" "$(dirname "$metadata")"
            printf '9.9.9\n' > "$object"
            printf '%s\n' 'text/plain; charset=utf-8' > "$metadata.type"
            printf '%s\n' 'no-store' > "$metadata.cache"
            write_etag
        fi
        if [ "$if_none_match" = '*' ] && [ -f "$object" ]; then
            printf 'PreconditionFailed\n' >&2
            exit 255
        fi
        if [ -n "$if_match" ] && { [ ! -f "$object" ] || [ "$(cat "$metadata.etag")" != "$if_match" ]; }; then
            printf 'PreconditionFailed\n' >&2
            exit 255
        fi
        mkdir -p "$(dirname "$object")" "$(dirname "$metadata")"
        cp "$body" "$object"
        printf '%s\n' "$content_type" > "$metadata.type"
        printf '%s\n' "$cache_control" > "$metadata.cache"
        write_etag
        printf '{}\n'
        ;;
    *) exit 2 ;;
esac
`

const fakePublicationCurl = `#!/bin/sh
set -eu
fail_http=no
output=
write_out=no
url=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --fail) fail_http=yes; shift ;;
        --output|-o) output=$2; shift 2 ;;
        --write-out|-w) write_out=yes; shift 2 ;;
        --proto|--proto-redir) shift 2 ;;
        --silent|--show-error|--location|--tlsv1.2) shift ;;
        https://*) url=$1; shift ;;
        *) exit 2 ;;
    esac
done
key=${url#https://ops.luigiverona.dev/}
if [ "${OPS_TEST_CURL_FAIL_KEY:-}" = "$key" ]; then
    printf 'simulated network failure\n' >&2
    exit 6
fi
object=$OPS_TEST_STATE/objects/$key
status=200
if [ "$key" = releases/latest ] && [ ! -f "$object" ]; then
    case "${OPS_TEST_LATEST_MODE:-404}" in
        network) printf 'simulated DNS failure\n' >&2; exit 6 ;;
        malformed) printf 'not-a-version\nsecond-line\n' > "$output" ;;
        404) status=404; : > "$output" ;;
        500) status=500; printf 'server error\n' > "$output" ;;
        *) exit 2 ;;
    esac
elif [ -f "$object" ]; then
    cp "$object" "$output"
else
    status=404
    : > "$output"
fi
printf 'curl|%s|%s\n' "$key" "$status" >> "$OPS_TEST_LOG"
if [ "$write_out" = yes ]; then
    printf '%s' "$status"
fi
if [ "$fail_http" = yes ] && [ "$status" -ge 400 ]; then
    exit 22
fi
`
