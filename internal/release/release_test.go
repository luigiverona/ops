package release

import (
	"context"
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

func TestUnconfiguredTrustFailsClosed(t *testing.T) {
	client := Client{}
	if _, err := client.DownloadVerified(context.Background(), "1.0.0"); err == nil || !strings.Contains(err.Error(), "trust is not configured") {
		t.Fatalf("error = %v", err)
	}
}

func TestSignatureAndChecksumVerification(t *testing.T) {
	trust, artifacts := signedArtifacts(t, "1.2.3")
	tests := []struct {
		name    string
		mutate  func(map[string][]byte)
		wantErr string
	}{
		{"valid", func(map[string][]byte) {}, ""},
		{"invalid signature", func(a map[string][]byte) { a[SignatureName][0] ^= 0xff }, "signature verification failed"},
		{"wrong checksum", func(a map[string][]byte) {
			a[ChecksumsName] = []byte(strings.Repeat("0", 64) + "  " + BinaryName + "\n")
			signArtifact(t, artifacts["homedir"], a)
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

func signedArtifacts(t *testing.T, version string) (Trust, map[string][]byte) {
	t.Helper()
	for _, command := range []string{"gpg", "gpgv"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skip(command + " unavailable")
		}
	}
	home := t.TempDir()
	_ = os.Chmod(home, 0o700)
	cmd := exec.Command("gpg", "--batch", "--homedir", home, "--passphrase", "", "--quick-generate-key", "ops release test <ops@example.invalid>", "ed25519", "sign", "0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate key: %v: %s", err, output)
	}
	list := exec.Command("gpg", "--batch", "--homedir", home, "--with-colons", "--list-keys")
	output, err := list.Output()
	if err != nil {
		t.Fatal(err)
	}
	var fingerprint string
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 9 && fields[0] == "fpr" {
			fingerprint = fields[9]
			break
		}
	}
	export := exec.Command("gpg", "--batch", "--homedir", home, "--armor", "--export", fingerprint)
	public, err := export.Output()
	if err != nil {
		t.Fatal(err)
	}
	binary := []byte("#!/bin/sh\nprintf 'ops " + version + "\\n'\n")
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, BinaryName)
	_ = os.WriteFile(binaryPath, binary, 0o755)
	hash, _ := FileChecksum(binaryPath)
	artifacts := map[string][]byte{BinaryName: binary, ChecksumsName: []byte(hash + "  " + BinaryName + "\n"), "homedir": []byte(home)}
	signArtifact(t, []byte(home), artifacts)
	return Trust{Fingerprint: fingerprint, PublicKey: string(public)}, artifacts
}

func signArtifact(t *testing.T, home []byte, artifacts map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	manifest := filepath.Join(dir, ChecksumsName)
	signature := filepath.Join(dir, SignatureName)
	_ = os.WriteFile(manifest, artifacts[ChecksumsName], 0o600)
	cmd := exec.Command("gpg", "--batch", "--homedir", string(home), "--detach-sign", "--output", signature, manifest)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sign: %v: %s", err, output)
	}
	artifacts[SignatureName], _ = os.ReadFile(signature)
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
