//go:build ownership_integration

package pgp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/release"
	"github.com/luigiverona/ops/internal/run"
)

func TestNativeGPGHelpers(t *testing.T) {
	owner := run.NewOwner()
	if err := owner.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner := run.Exec{Owner: owner}
	for _, keyboxd := range []bool{false, true} {
		home := t.TempDir()
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
				t.Fatalf("public key lost across helper cleanup: %v", err)
			}
		}
	}
	if err := owner.Check(); err != nil {
		t.Fatal(err)
	}
}
