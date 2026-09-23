package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
)

type discoveryOwnershipRunner struct {
	*prepareRunner
	owner   *gateOwner
	failure error
	failed  bool
	later   int
}

func (r *discoveryOwnershipRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if r.failed {
		r.later++
	}
	if s.Name == "ssh-keygen" && len(s.Args) > 0 && s.Args[0] == "-l" {
		r.failed = true
		if run.OwnershipFailed(r.failure) {
			r.owner.poison = r.failure
		}
		return run.Result{}, r.failure
	}
	return r.prepareRunner.Run(ctx, s)
}
func (r *discoveryOwnershipRunner) CheckMutation() error { return r.owner.Check() }
func TestSSHDiscoveryOwnershipLifecycle(t *testing.T) {
	for _, prior := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-mutation", true: "after-mutation"}[prior], func(t *testing.T) {
			home := t.TempDir()
			if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
				t.Fatal(err)
			}
			key := filepath.Join(home, ".ssh", "candidate")
			content := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nmalformed fixture\n")
			if err := os.WriteFile(key, content, 0600); err != nil {
				t.Fatal(err)
			}
			original := &run.OwnershipError{Err: errors.New("fingerprint cleanup failure")}
			owner := &gateOwner{}
			r := &discoveryOwnershipRunner{prepareRunner: &prepareRunner{home: home}, owner: owner, failure: original}
			var out strings.Builder
			a := Runtime{Home: home, Ownership: owner, Runner: r, Out: &out, Err: &out}
			p := plan.Plan{ConfigureGit: prior, ReviewSSHIdentities: true, ConfigureSSH: true, AuthenticateGitHub: true}
			result := a.executePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\nUser\nuser@example.com\n"), Out: &out})
			if result.status != Fatal || !result.stopInspection || result.applied != prior || r.later != 0 {
				t.Fatalf("wrong lifecycle: %+v later=%d output=%s", result, r.later, &out)
			}
			found := false
			for _, problem := range result.problems {
				if errors.Is(problem.Err, original) {
					found = true
				}
			}
			if !found {
				t.Fatal("original ownership error lost", result.problems)
			}
			if prior && (r.gitName != "User" || r.gitEmail != "user@example.com") {
				t.Fatal("earlier successful change lost")
			}
			if b, err := os.ReadFile(key); err != nil || string(b) != string(content) {
				t.Fatal("candidate mutated", err)
			}
			for _, name := range []string{"ops", "config", "ops_config", "ops_known_hosts"} {
				if _, err := os.Stat(filepath.Join(home, ".ssh", name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("later persistent mutation", name, err)
				}
			}
			a.reportExecution(result)
			if strings.Contains(out.String(), "Earlier changes may remain") != prior || strings.Contains(out.String(), "Interrupted.") {
				t.Fatalf("wrong report classification: %s", &out)
			}
		})
	}
}
