package inspect

import (
	"context"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"os"
	"path/filepath"
	"testing"
)

type finalUntrustedRunner struct {
	stateRunner
	executed []string
}

func (r *finalUntrustedRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if s.Name == "gh" || s.Name == "ssh-keygen" || s.Name == "ssh" {
		r.executed = append(r.executed, s.Name)
	}
	return r.stateRunner.Run(ctx, s)
}
func (r *finalUntrustedRunner) OfficialInstalled(context.Context, string) (bool, error) {
	return false, nil
}
func (r *finalUntrustedRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args})
}
func TestFinalUntrustedCoreExecutables(t *testing.T) {
	t.Run("github", func(t *testing.T) {
		r := &finalUntrustedRunner{}
		w := Workstation{Runner: r, Home: t.TempDir()}
		_, _ = w.External(context.Background(), plan.State{Installed: map[string]bool{"github-cli": true}})
		if len(r.executed) > 0 {
			t.Fatalf("unverified core executed: %v", r.executed)
		}
	})
	t.Run("ssh", func(t *testing.T) {
		r := &finalUntrustedRunner{}
		home := t.TempDir()
		os.Mkdir(filepath.Join(home, ".ssh"), 0700)
		os.WriteFile(filepath.Join(home, ".ssh", "private"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nfixture\n"), 0600)
		_, _ = (Workstation{Runner: r, Home: home, PacmanConf: testPacmanConf(t)}).Local(context.Background())
		if len(r.executed) > 0 {
			t.Fatalf("unverified core executed: %v", r.executed)
		}
	})
}
