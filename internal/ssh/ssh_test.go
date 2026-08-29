package ssh

import (
	"context"
	"encoding/base64"
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

func managerFor(t *testing.T) Manager {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen unavailable")
	}
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	return Manager{Home: home, Runner: run.Exec{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard}}
}

func generate(t *testing.T, m Manager, name string) {
	t.Helper()
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(m.dir(), name), "-C", "test")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, output)
	}
}

func TestDiscoverNoKeysAndUnrelatedFiles(t *testing.T) {
	m := managerFor(t)
	for name, data := range map[string]string{
		"config": "Host example\n", "known_hosts": "example ssh-ed25519 AAAA\n",
		"authorized_keys": "not a key", "certificate.pub": "ssh-ed25519-cert-v01@openssh.com AAAA",
		"malformed": "-----BEGIN OPENSSH PRIVATE KEY-----\nbad",
	} {
		_ = os.WriteFile(filepath.Join(m.dir(), name), []byte(data), 0o600)
	}
	_ = os.Mkdir(filepath.Join(m.dir(), "directory"), 0o700)
	_ = os.Symlink("/etc/passwd", filepath.Join(m.dir(), "linked-key"))
	ids, err := m.Discover(context.Background())
	if err != nil || len(ids) != 0 {
		t.Fatalf("identities = %#v, %v", ids, err)
	}
}

func TestAuthorizedKeysIsNeverAnIdentityFile(t *testing.T) {
	m := managerFor(t)
	generate(t, m, "source")
	public, _ := os.ReadFile(filepath.Join(m.dir(), "source.pub"))
	_ = os.Remove(filepath.Join(m.dir(), "source.pub"))
	_ = os.WriteFile(filepath.Join(m.dir(), "authorized_keys"), public, 0o600)
	ids, err := m.Discover(context.Background())
	if err != nil || len(ids) != 1 || ids[0].PublicPath != "" {
		t.Fatalf("authorized_keys was classified as identity pair: %#v, %v", ids, err)
	}
}

func TestDiscoverPairsMultipleAndOrphanPublic(t *testing.T) {
	m := managerFor(t)
	generate(t, m, "first")
	generate(t, m, "second")
	data, _ := os.ReadFile(filepath.Join(m.dir(), "second.pub"))
	_ = os.WriteFile(filepath.Join(m.dir(), "orphan.pub"), data, 0o644)
	ids, err := m.Discover(context.Background())
	if err != nil || len(ids) != 2 {
		t.Fatalf("identities = %#v, %v", ids, err)
	}
	for _, id := range ids {
		if id.PublicPath == "" || id.Fingerprint == "" {
			t.Fatalf("unpaired identity: %#v", id)
		}
	}
}

func TestDeleteExactIdentityOnly(t *testing.T) {
	m := managerFor(t)
	generate(t, m, "delete-me")
	untouched := filepath.Join(m.dir(), "known_hosts")
	_ = os.WriteFile(untouched, []byte("keep"), 0o600)
	ids, _ := m.Discover(context.Background())
	if err := m.Delete(context.Background(), ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(untouched); err != nil {
		t.Fatal("unrelated file was removed")
	}
	if _, err := os.Stat(ids[0].PrivatePath); !os.IsNotExist(err) {
		t.Fatal("private key remains")
	}
}

func TestDeleteRejectsSymlinkSwap(t *testing.T) {
	m := managerFor(t)
	generate(t, m, "key")
	ids, _ := m.Discover(context.Background())
	if err := os.Remove(ids[0].PrivatePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", ids[0].PrivatePath); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(context.Background(), ids[0]); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

type configRunner struct{ home string }

func (f configRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	if spec.Name == "ssh" && len(spec.Args) > 0 && spec.Args[0] == "-G" {
		return run.Result{Stdout: "identityfile " + filepath.Join(f.home, ".ssh", "ops") + "\nidentitiesonly yes\nuserknownhostsfile " + filepath.Join(f.home, ".ssh", "ops_known_hosts") + "\nstricthostkeychecking yes\n"}, nil
	}
	return run.Result{}, fmt.Errorf("unexpected command: %s", spec.Name)
}

func TestConfigureGitHubPreservesConfigAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	_ = os.Mkdir(dir, 0o700)
	original := "Host example.com\n    IdentityFile ~/.ssh/example\n"
	_ = os.WriteFile(filepath.Join(dir, "config"), []byte(original), 0o600)
	unrelatedKnownHosts := filepath.Join(dir, "known_hosts")
	_ = os.WriteFile(unrelatedKnownHosts, []byte("example.com ssh-ed25519 unrelated\n"), 0o600)
	m := managerWithMetadata(t, home)
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "config"))
	if strings.Count(string(data), "Include ~/.ssh/ops_config") != 1 || !strings.Contains(string(data), original) {
		t.Fatalf("config not preserved: %s", data)
	}
	managed, _ := os.ReadFile(filepath.Join(dir, "ops_config"))
	if !strings.HasPrefix(string(managed), managedMarker+"\n") || !strings.Contains(string(managed), "IdentitiesOnly yes") || !strings.Contains(string(managed), "StrictHostKeyChecking yes") {
		t.Fatal("missing deterministic identity selection")
	}
	knownHosts, _ := os.ReadFile(filepath.Join(dir, "ops_known_hosts"))
	if !strings.Contains(string(knownHosts), "github.com ssh-ed25519 ") || !validManagedKnownHosts(filepath.Join(dir, "ops_known_hosts")) {
		t.Fatalf("managed GitHub host keys are invalid: %s", knownHosts)
	}
	if data, _ := os.ReadFile(unrelatedKnownHosts); string(data) != "example.com ssh-ed25519 unrelated\n" {
		t.Fatal("ordinary known_hosts was modified")
	}
	if !m.GitHubConfigured(context.Background()) {
		t.Fatal("fresh GitHub SSH configuration was not recognized")
	}
}

func TestConfigureGitHubRejectsMalformedOrMissingMetadata(t *testing.T) {
	for name, body := range map[string]string{
		"malformed": `{"ssh_keys":[`,
		"missing":   `{"ssh_keys":[]}`,
		"invalid":   `{"ssh_keys":["ssh-ed25519 not-base64"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
			defer server.Close()
			m := Manager{Home: home, Runner: configRunner{home}, HTTP: server.Client(), MetadataURL: server.URL}
			if err := m.ConfigureGitHub(context.Background()); err == nil {
				t.Fatal("expected metadata failure")
			}
			if _, err := os.Lstat(filepath.Join(home, ".ssh", "ops_known_hosts")); !os.IsNotExist(err) {
				t.Fatal("host-key file was created from invalid metadata")
			}
		})
	}
}

func TestConfigureGitHubRefusesUnownedOrSymlinkedManagedFiles(t *testing.T) {
	for _, name := range []string{"ops_config", "ops_known_hosts"} {
		t.Run(name+" unknown", func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".ssh")
			_ = os.Mkdir(dir, 0o700)
			path := filepath.Join(dir, name)
			_ = os.WriteFile(path, []byte("user content\n"), 0o600)
			m := managerWithMetadata(t, home)
			if err := m.ConfigureGitHub(context.Background()); err == nil || !strings.Contains(err.Error(), "unrecognized") {
				t.Fatalf("error = %v", err)
			}
			data, _ := os.ReadFile(path)
			if string(data) != "user content\n" {
				t.Fatal("unowned file was modified")
			}
		})
		t.Run(name+" symlink", func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".ssh")
			_ = os.Mkdir(dir, 0o700)
			target := filepath.Join(home, "outside")
			_ = os.WriteFile(target, []byte("outside\n"), 0o600)
			_ = os.Symlink(target, filepath.Join(dir, name))
			m := managerWithMetadata(t, home)
			if err := m.ConfigureGitHub(context.Background()); err == nil {
				t.Fatal("expected symlink rejection")
			}
			data, _ := os.ReadFile(target)
			if string(data) != "outside\n" {
				t.Fatal("symlink target was modified")
			}
		})
	}
}

func TestConfigureGitHubPreflightsMainConfigBeforeManagedWrites(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	_ = os.Mkdir(dir, 0o700)
	outside := filepath.Join(home, "outside-config")
	_ = os.WriteFile(outside, []byte("keep\n"), 0o600)
	_ = os.Symlink(outside, filepath.Join(dir, "config"))
	m := managerWithMetadata(t, home)
	if err := m.ConfigureGitHub(context.Background()); err == nil {
		t.Fatal("expected main config symlink rejection")
	}
	for _, name := range []string{"ops_config", "ops_known_hosts"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was written before preflight completed", name)
		}
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "keep\n" {
		t.Fatal("main config symlink target was modified")
	}
}

func TestInvalidManagedKnownHostsIsDetectedAndRepaired(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	_ = os.Mkdir(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "ops_config"), []byte(managedMarker+"\nHost github.com\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "ops_known_hosts"), []byte(managedMarker+"\ngithub.com ssh-ed25519 invalid\n"), 0o600)
	m := managerWithMetadata(t, home)
	if m.GitHubConfigured(context.Background()) {
		t.Fatal("invalid managed host-key file was accepted")
	}
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !m.GitHubConfigured(context.Background()) {
		t.Fatal("managed host-key file was not repaired")
	}
}

func TestConfigureGitHubMigratesExactLegacyOpsConfig(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	_ = os.Mkdir(dir, 0o700)
	legacy := "Host github.com\n    HostName github.com\n    User git\n    IdentityFile ~/.ssh/ops\n    IdentitiesOnly yes\n"
	_ = os.WriteFile(filepath.Join(dir, "ops_config"), []byte(legacy), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "config"), []byte("Include ~/.ssh/ops_config\n"), 0o600)
	m := managerWithMetadata(t, home)
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "ops_config"))
	if !strings.HasPrefix(string(data), managedMarker+"\n") {
		t.Fatal("exact legacy configuration was not migrated to marked ownership")
	}
}

func managerWithMetadata(t *testing.T, home string) Manager {
	t.Helper()
	body := `{"ssh_keys":["` + testHostKey("ssh-ed25519", 1) + `","` + testHostKey("ssh-rsa", 2) + `"]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
	t.Cleanup(server.Close)
	return Manager{Home: home, Runner: configRunner{home}, HTTP: server.Client(), MetadataURL: server.URL}
}

func testHostKey(keyType string, fill byte) string {
	typeName := []byte(keyType)
	blob := make([]byte, 4+len(typeName)+4+32)
	blob[3] = byte(len(typeName))
	copy(blob[4:], typeName)
	offset := 4 + len(typeName)
	blob[offset+3] = 32
	for i := offset + 4; i < len(blob); i++ {
		blob[i] = fill
	}
	return keyType + " " + base64.StdEncoding.EncodeToString(blob)
}
