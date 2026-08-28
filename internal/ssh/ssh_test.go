package ssh

import (
	"context"
	"fmt"
	"io"
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
		return run.Result{Stdout: "identityfile " + filepath.Join(f.home, ".ssh", "ops") + "\nidentitiesonly yes\n"}, nil
	}
	return run.Result{}, fmt.Errorf("unexpected command: %s", spec.Name)
}

func TestConfigureGitHubPreservesConfigAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	_ = os.Mkdir(dir, 0o700)
	original := "Host example.com\n    IdentityFile ~/.ssh/example\n"
	_ = os.WriteFile(filepath.Join(dir, "config"), []byte(original), 0o600)
	m := Manager{Home: home, Runner: configRunner{home}}
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
	if !strings.Contains(string(managed), "IdentitiesOnly yes") {
		t.Fatal("missing deterministic identity selection")
	}
}
