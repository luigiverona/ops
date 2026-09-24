//go:build ownership_integration

package pgp

import (
	"context"
	"github.com/luigiverona/ops/internal/testproc"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/release"
	"github.com/luigiverona/ops/internal/run"
)

func TestNativeGPGHelpers(t *testing.T) {
	if testproc.Supervise(t) {
		return
	}
	owner := run.NewOwner()
	if err := owner.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	testproc.RecordScope(t)
	runner := run.Exec{Owner: owner}
	for _, keyboxd := range []bool{false, true} {
		home := t.TempDir()
		if err := os.Chmod(home, 0700); err != nil {
			t.Fatal(err)
		}
		if keyboxd {
			if err := os.WriteFile(filepath.Join(home, "common.conf"), []byte("use-keyboxd\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		for range 3 {
			// Public fixture only. Never generate or use a release-signing private key.
			if _, err := runner.Run(context.Background(), gpgSpec(home, []string{"--import"}, strings.NewReader(release.DefaultTrust().PublicKey))); err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(context.Background(), gpgSpec(home, []string{"--armor", "--export"}))
			if err != nil || !strings.Contains(result.Stdout, "BEGIN PGP PUBLIC KEY BLOCK") {
				t.Fatalf("public key lost across helper cleanup: %v: %s", err, result.Stderr)
			}
		}
	}
	if err := owner.Check(); err != nil {
		t.Fatal(err)
	}
}

// Unlike a cooperative defer, the surviving supervisor can clean daemonized
// helpers after the Go owner is killed. No pre-existing keyring is accessed.
func TestNativeGPGOwnerDeath(t *testing.T) {
	if testproc.SuperviseDeath(t) {
		return
	}
	owner := run.NewOwner()
	if err := owner.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	testproc.RecordScope(t)
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "common.conf"), []byte("use-keyboxd\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := (run.Exec{Owner: owner}).Run(context.Background(), run.Spec{
		Name: "/bin/sh", Args: []string{"-c", `gpg --no-options --batch --homedir "$GNUPGHOME" --list-keys >/dev/null && printf ready > "$OPS_NATIVE_READY"; sleep 20`},
		Env: []string{"GNUPGHOME=" + home}, EphemeralHelpers: true,
	})
	t.Fatal("owner should have been killed", err)
}
