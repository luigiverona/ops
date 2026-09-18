package arch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

func TestOfficialMountPreservesHostDatabasesAndUsesIndependentMetadata(t *testing.T) {
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare unavailable")
	}
	probe, err := (run.Exec{}).Run(context.Background(), run.Spec{Name: "unshare", Args: []string{"--user", "--map-root-user", "--mount", "--propagation", "private", "--", "/bin/true"}})
	if err != nil {
		t.Skipf("unprivileged mount namespaces unavailable: %s", probe.Stderr)
	}
	f := testpkg.NewPacmanFixture(t)
	f.Sync(t, "core", testpkg.FixturePackage{Name: "ops-safe-library", Version: "1-1", Packager: "Official", Payload: "library"})
	f.Sync(t, "extra", testpkg.FixturePackage{Name: "ops-official-entry", Version: "1-1", Packager: "Official", Payload: "program", Depends: "ops-safe-library", Provides: "ops-trusted-provider=1"})
	f.Sync(t, "multilib")
	spoof := testpkg.NewPacmanFixture(t)
	spoof.Sync(t, "core", testpkg.FixturePackage{Name: "ops-injected-member", Version: "1-1", Packager: "Custom", Payload: "injected"})
	spoof.Sync(t, "extra", testpkg.FixturePackage{Name: "ops-false-provider", Version: "1-1", Packager: "Custom", Payload: "custom", Depends: "ops-injected-member", Provides: "ops-trusted-provider=1"})
	spoof.Sync(t, "multilib")
	input := "[options]\nArchitecture = x86_64\nSigLevel = Required\n[core]\nServer = https://custom.example/core\n[extra]\nServer = https://custom.example/extra\n[multilib]\nServer = https://custom.example/multilib\n"
	conf, _, err := archrepo.OfficialConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(f.Dir, "independent.conf")
	if err := os.WriteFile(config, []byte(conf), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat("/var/lib/pacman/sync")
	if err != nil {
		t.Fatal(err)
	}
	// First expose a malicious local [core]/[extra] database inside this
	// disposable namespace. Prove it can inject a member and change the provider
	// and dependency metadata, then apply the production official mount boundary.
	spoofScript := "set -eu\nmount --bind \"$1\" /var/lib/pacman/sync\nshift\nexec \"$@\""
	prefix := []string{"--user", "--map-root-user", "--mount", "--propagation", "private", "--", "/bin/sh", "-c", spoofScript, "ops-spoof-test", filepath.Join(spoof.Dir, "db/sync")}
	queryArgs := []string{"pacman", "--config", config, "-Sp", "--noconfirm", "--print-format", "%r/%n\t%P", "--", "ops-trusted-provider>=1"}
	malicious, err := (run.Exec{}).Run(context.Background(), run.Spec{Name: "unshare", Args: append(append([]string(nil), prefix...), queryArgs...)})
	if err != nil || !strings.Contains(malicious.Stdout, "extra/ops-false-provider") || !strings.Contains(malicious.Stdout, "core/ops-injected-member") {
		t.Fatalf("invalid spoofed metadata fixture: %s %s %v", malicious.Stdout, malicious.Stderr, err)
	}
	args := append(append([]string(nil), prefix...), "/bin/sh", "-c", officialMountScript, "ops-official-test", filepath.Join(f.Dir, "db/sync"))
	args = append(args, queryArgs...)
	result, err := (run.Exec{}).Run(context.Background(), run.Spec{Name: "unshare", Args: args})
	if err != nil {
		t.Fatalf("native independent transaction: %v %s", err, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "extra/ops-official-entry\tops-trusted-provider=1\n") || !strings.Contains(result.Stdout, "core/ops-safe-library\t\n") || strings.Contains(result.Stdout, "ops-false-provider") || strings.Contains(result.Stdout, "ops-injected-member") {
		t.Fatal("independent provider/dependency/transaction metadata lost", result.Stdout)
	}
	after, err := os.Stat("/var/lib/pacman/sync")
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("host sync database mount changed", err)
	}
	// The private mount did not publish the synthetic official package to host
	// queries, and the normal local DB/lock were never replaced or relocated.
	result, err = (run.Exec{}).Run(context.Background(), run.Spec{Name: "pacman", Args: []string{"-Si", "--", "extra/ops-official-entry"}})
	if err == nil {
		t.Fatal("private source escaped to host databases", result.Stdout)
	}
}
