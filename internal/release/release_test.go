package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

func TestManifestChecksum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ChecksumsName)
	valid := strings.Repeat("a", 64) + "  " + BinaryName + "\n"
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ManifestChecksum(path, BinaryName); err != nil || got != strings.Repeat("a", 64) {
		t.Fatalf("got %q, %v", got, err)
	}
	_ = os.WriteFile(path, []byte(valid+valid), 0o600)
	if _, err := ManifestChecksum(path, BinaryName); err == nil {
		t.Fatal("expected duplicate failure")
	}
	_ = os.WriteFile(path, []byte("bad  "+BinaryName+"\n"), 0o600)
	if _, err := ManifestChecksum(path, BinaryName); err == nil {
		t.Fatal("expected invalid hash failure")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, test := range []struct {
		a, b string
		want int
	}{{"1.0.0", "1.0.1", -1}, {"2.0.0", "1.9.9", 1}, {"1.2.3", "1.2.3", 0}} {
		got, err := CompareVersions(test.a, test.b)
		if err != nil || got != test.want {
			t.Fatalf("%s %s: %d %v", test.a, test.b, got, err)
		}
	}
	if _, err := CompareVersions("dev", "1.0.0"); err == nil {
		t.Fatal("expected invalid version")
	}
}

func TestDefaultTrustProvisioned(t *testing.T) {
	trust := DefaultTrust()

	if trust.Fingerprint != "EB564BFFD8F63A984BF72A0237A80EDB682BBBFD" {
		t.Fatalf("fingerprint = %q", trust.Fingerprint)
	}
	if !strings.Contains(trust.PublicKey, "-----BEGIN PGP PUBLIC KEY BLOCK-----") {
		t.Fatal("embedded public key is missing")
	}
}

func TestInvalidTrustFailsClosed(t *testing.T) {
	client := Client{
		Trust: Trust{
			Fingerprint: "INVALID",
			PublicKey:   "not a public key",
		},
	}

	if _, err := client.DownloadVerified(context.Background(), "1.0.0"); err == nil ||
		!strings.Contains(err.Error(), "release trust is not configured") {
		t.Fatalf("error = %v", err)
	}
}

func TestSignatureAndChecksumVerification(t *testing.T) {
	untrustedHome := t.TempDir()
	_ = os.WriteFile(filepath.Join(untrustedHome, "gpg.conf"), []byte("option-that-must-never-be-consumed\n"), 0o600)
	t.Setenv("GNUPGHOME", untrustedHome)
	trust, artifacts, signer := signedArtifacts(t, "1.2.3")
	tests := []struct {
		name    string
		mutate  func(map[string][]byte)
		wantErr string
	}{
		{"valid", func(map[string][]byte) {}, ""},
		{"invalid signature", func(a map[string][]byte) { a[SignatureName][0] ^= 0xff }, "signature verification failed"},
		{"wrong checksum", func(a map[string][]byte) {
			a[ChecksumsName] = []byte(strings.Repeat("0", 64) + "  " + BinaryName + "\n")
			signArtifact(t, signer, a, "")
		}, "checksum verification failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copyArtifacts := map[string][]byte{}
			for k, v := range artifacts {
				copyArtifacts[k] = append([]byte(nil), v...)
			}
			test.mutate(copyArtifacts)
			server := artifactServer(copyArtifacts)
			defer server.Close()
			client := Client{BaseURL: server.URL, Trust: trust, Runner: run.Exec{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard}}
			verified, err := client.DownloadVerified(context.Background(), "1.2.3")
			if test.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				_ = verified.Close()
			} else if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestSignatureStatusRejectsUnsafeStates(t *testing.T) {
	fingerprint := strings.Repeat("A", 40)
	other := strings.Repeat("B", 40)
	for name, test := range map[string]struct {
		status string
		err    error
	}{
		"valid current subkey": {"[GNUPG:] VALIDSIG " + fingerprint + " 0 0 0 0 0 0 0 0 0\n", nil},
		"wrong signer":         {"[GNUPG:] VALIDSIG " + other + " 0 0 0 0 0 0 0 0 0\n", nil},
		"revoked key":          {"[GNUPG:] REVKEYSIG key\n[GNUPG:] VALIDSIG " + fingerprint + " 0 0 0 0 0 0 0 0 0\n", nil},
		"expired key":          {"[GNUPG:] EXPKEYSIG key\n[GNUPG:] VALIDSIG " + fingerprint + " 0 0 0 0 0 0 0 0 0\n", nil},
		"expired signature":    {"[GNUPG:] EXPSIG key\n[GNUPG:] VALIDSIG " + fingerprint + " 0 0 0 0 0 0 0 0 0\n", nil},
		"bad signature":        {"[GNUPG:] BADSIG key\n", errors.New("gpg failed")},
		"process error":        {"[GNUPG:] VALIDSIG " + fingerprint + " 0 0 0 0 0 0 0 0 0\n", errors.New("gpg failed")},
		"multiple signatures":  {"[GNUPG:] VALIDSIG " + fingerprint + "\n[GNUPG:] VALIDSIG " + fingerprint + "\n", nil},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateSignatureStatus(test.status, fingerprint, test.err)
			if name == "valid current subkey" && err != nil {
				t.Fatal(err)
			}
			if name != "valid current subkey" && err == nil {
				t.Fatal("unsafe status was accepted")
			}
		})
	}
}

func TestRealGPGRejectsWrongExpiredAndRevokedSigners(t *testing.T) {
	requireGPG(t)
	t.Run("wrong signer", func(t *testing.T) {
		pinned := newSigner(t, "", "0")
		wrong := newSigner(t, "", "0")
		_, artifacts := artifactsForSigner(t, "1.2.3", wrong, "")
		trust := Trust{Fingerprint: pinned.signing, PublicKey: pinned.public + "\n" + wrong.public}
		assertVerificationFails(t, trust, artifacts, "key other than the pinned")
	})
	t.Run("expired signing subkey", func(t *testing.T) {
		signer := newSigner(t, "20250101T000000", "1d")
		trust, artifacts := artifactsForSigner(t, "1.2.3", signer, "20250101T010000")
		assertVerificationFails(t, trust, artifacts, "expired")
	})
	t.Run("revoked signing key", func(t *testing.T) {
		signer := newSigner(t, "", "0")
		_, artifacts := artifactsForSigner(t, "1.2.3", signer, "")
		revokeSigner(t, &signer)
		trust := Trust{Fingerprint: signer.signing, PublicKey: signer.public}
		assertVerificationFails(t, trust, artifacts, "revoked")
	})
}

func assertVerificationFails(t *testing.T, trust Trust, artifacts map[string][]byte, want string) {
	t.Helper()
	server := artifactServer(artifacts)
	defer server.Close()
	client := Client{BaseURL: server.URL, Trust: trust, Runner: run.Exec{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard}}
	if verified, err := client.DownloadVerified(context.Background(), "1.2.3"); err == nil {
		_ = verified.Close()
		t.Fatal("unsafe signer was accepted")
	} else if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
		t.Fatalf("verification failed for the wrong reason: %v", err)
	}
}

type testSigner struct {
	home, primary, signing, public string
}

func requireGPG(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg unavailable")
	}
}

func signedArtifacts(t *testing.T, version string) (Trust, map[string][]byte, testSigner) {
	t.Helper()
	requireGPG(t)
	signer := newSigner(t, "", "0")
	trust, artifacts := artifactsForSigner(t, version, signer, "")
	return trust, artifacts, signer
}

func newSigner(t *testing.T, fakeTime, expiration string) testSigner {
	t.Helper()
	home := t.TempDir()
	_ = os.Chmod(home, 0o700)
	base := []string{"--homedir", home, "--no-options", "--batch", "--no-tty", "--pinentry-mode", "loopback", "--passphrase", ""}
	if fakeTime != "" {
		base = append(base, "--faked-system-time", fakeTime)
	}
	cmd := exec.Command("gpg", append(base, "--quick-generate-key", "ops release test <ops@example.invalid>", "ed25519", "cert", "0")...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate key: %v: %s", err, output)
	}
	list := exec.Command("gpg", "--homedir", home, "--no-options", "--batch", "--with-colons", "--list-keys")
	output, err := list.Output()
	if err != nil {
		t.Fatal(err)
	}
	primary := firstFingerprint(string(output))
	add := exec.Command("gpg", append(base, "--quick-add-key", primary, "ed25519", "sign", expiration)...)
	if output, err := add.CombinedOutput(); err != nil {
		t.Fatalf("add signing subkey: %v: %s", err, output)
	}
	list = exec.Command("gpg", "--homedir", home, "--no-options", "--batch", "--with-colons", "--list-keys", primary)
	output, err = list.Output()
	if err != nil {
		t.Fatal(err)
	}
	fingerprints := allFingerprints(string(output))
	if len(fingerprints) < 2 {
		t.Fatal("generated key has no signing subkey")
	}
	export := exec.Command("gpg", "--homedir", home, "--no-options", "--batch", "--armor", "--export", primary)
	public, err := export.Output()
	if err != nil {
		t.Fatal(err)
	}
	return testSigner{home: home, primary: primary, signing: fingerprints[1], public: string(public)}
}

func artifactsForSigner(t *testing.T, version string, signer testSigner, fakeTime string) (Trust, map[string][]byte) {
	t.Helper()
	binary := []byte("#!/bin/sh\nprintf 'ops " + version + "\\n'\n")
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, BinaryName)
	_ = os.WriteFile(binaryPath, binary, 0o755)
	hash, _ := FileChecksum(binaryPath)
	artifacts := map[string][]byte{BinaryName: binary, ChecksumsName: []byte(hash + "  " + BinaryName + "\n")}
	signArtifact(t, signer, artifacts, fakeTime)
	return Trust{Fingerprint: signer.signing, PublicKey: signer.public}, artifacts
}

func signArtifact(t *testing.T, signer testSigner, artifacts map[string][]byte, fakeTime string) {
	t.Helper()
	dir := t.TempDir()
	manifest := filepath.Join(dir, ChecksumsName)
	signature := filepath.Join(dir, SignatureName)
	_ = os.WriteFile(manifest, artifacts[ChecksumsName], 0o600)
	args := []string{"--homedir", signer.home, "--no-options", "--batch", "--no-tty", "--pinentry-mode", "loopback", "--passphrase", ""}
	if fakeTime != "" {
		args = append(args, "--faked-system-time", fakeTime)
	}
	args = append(args, "--local-user", signer.signing+"!", "--detach-sign", "--output", signature, manifest)
	cmd := exec.Command("gpg", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sign: %v: %s", err, output)
	}
	artifacts[SignatureName], _ = os.ReadFile(signature)
}

func firstFingerprint(output string) string {
	values := allFingerprints(output)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func allFingerprints(output string) []string {
	var values []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 9 && fields[0] == "fpr" {
			values = append(values, fields[9])
		}
	}
	return values
}

func revokeSigner(t *testing.T, signer *testSigner) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(signer.home, "openpgp-revocs.d", "*.rev"))
	if err != nil || len(files) != 1 {
		t.Fatalf("find revocation certificate: %v, %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, ":-----BEGIN PGP PUBLIC KEY BLOCK-----")
	if start < 0 {
		t.Fatal("revocation certificate armor not found")
	}
	armor := strings.ReplaceAll(text[start:], "\n:", "\n")
	armor = strings.TrimPrefix(armor, ":")
	path := filepath.Join(t.TempDir(), "revoke.asc")
	if err := os.WriteFile(path, []byte(armor), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("gpg", "--homedir", signer.home, "--no-options", "--batch", "--import", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("import revocation certificate: %v: %s", err, output)
	}
	export := exec.Command("gpg", "--homedir", signer.home, "--no-options", "--batch", "--armor", "--export", signer.primary)
	public, err := export.Output()
	if err != nil {
		t.Fatal(err)
	}
	signer.public = string(public)
}

func artifactServer(artifacts map[string][]byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		if data, ok := artifacts[name]; ok {
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
}

type replaceRunner struct {
	version             string
	failFinal, failSudo bool
	target              string
}

func (f *replaceRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	if spec.Name == "sudo" {
		if f.failSudo {
			return run.Result{}, fmt.Errorf("sudo failed")
		}
		args := spec.Args
		if len(args) > 0 && args[0] == "-n" {
			args = args[1:]
		}
		switch args[0] {
		case "install":
			return run.Result{}, copyFile(args[len(args)-2], args[len(args)-1])
		case "cp":
			return run.Result{}, copyFile(args[len(args)-2], args[len(args)-1])
		case "mv":
			return run.Result{}, os.Rename(args[len(args)-2], args[len(args)-1])
		case "rm":
			for _, path := range args[3:] {
				_ = os.Remove(path)
			}
			return run.Result{}, nil
		}
	}
	if spec.Name == f.target && f.failFinal {
		return run.Result{}, fmt.Errorf("broken")
	}
	return run.Result{Stdout: "ops " + f.version + "\n"}, nil
}
func copyFile(from, to string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, data, 0o755)
}

func TestAtomicReplaceAndPreservation(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "verified")
	target := filepath.Join(dir, "ops")
	_ = os.WriteFile(source, []byte("new"), 0o755)
	_ = os.WriteFile(target, []byte("old"), 0o755)
	runner := &replaceRunner{version: "2.0.0", target: target}
	if err := Replace(context.Background(), runner, source, target, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "new" {
		t.Fatal("new binary not installed")
	}
	_ = os.WriteFile(target, []byte("old"), 0o755)
	runner.failFinal = true
	if err := Replace(context.Background(), runner, source, target, "2.0.0"); err == nil {
		t.Fatal("expected postcondition failure")
	}
	if data, _ := os.ReadFile(target); string(data) != "old" {
		t.Fatal("prior binary not restored")
	}
}

func TestReplaceSudoFailurePreservesBinary(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "verified")
	target := filepath.Join(dir, "ops")
	_ = os.WriteFile(source, []byte("new"), 0o755)
	_ = os.WriteFile(target, []byte("old"), 0o755)
	runner := &replaceRunner{version: "2.0.0", target: target, failSudo: true}
	if err := Replace(context.Background(), runner, source, target, "2.0.0"); err == nil {
		t.Fatal("expected sudo failure")
	}
	if data, _ := os.ReadFile(target); string(data) != "old" {
		t.Fatal("binary changed")
	}
}
