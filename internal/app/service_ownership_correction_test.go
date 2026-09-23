package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
)

func TestServiceInspectionOwnershipLifecycle(t *testing.T) {
	for _, prior := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-mutation", true: "after-mutation"}[prior], func(t *testing.T) {
			a, out, base := noActionPrepareRuntime(t, false)
			gitRunner := &prepareRunner{home: a.Home}
			cfg := config.Config{Version: 2, Applications: []config.Application{{Source: config.Pacman, Identifier: "mullvad-vpn"}}}
			if err := os.WriteFile(config.Path(a.Home), []byte("version=2\npacman=[\"mullvad-vpn\"]"), 0600); err != nil {
				t.Fatal(err)
			}
			original := &run.OwnershipError{Err: errors.New("service cleanup failure")}
			owner := &gateOwner{}
			a.Ownership = owner
			failed, later := false, 0
			a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				if failed {
					later++
				}
				if s.Name == "systemctl" && s.Args[0] == "is-active" {
					failed = true
					owner.poison = original
					return run.Result{Stdout: "inactive\n"}, &run.Error{Name: "systemctl", Err: errors.Join(diagnosticExit(3), original)}
				}
				if s.Name == "systemctl" && s.Args[0] == "is-enabled" {
					return run.Result{Stdout: "enabled\n"}, nil
				}
				if s.Name == "sudo" {
					t.Fatalf("unexpected privileged mutation: %+v", s)
				}
				if s.Name == "git" && prior {
					return gitRunner.Run(ctx, s)
				}
				result, err := base.Run(ctx, s)
				if s.Name == "pacman" && s.Args[0] == "-Sl" {
					result.Stdout += "extra mullvad-vpn 1-1\n"
				}
				if s.Name == "pacman" && (s.Args[0] == "-Qq" || s.Args[0] == "-Qeq") {
					result.Stdout += "mullvad-vpn\n"
				}
				return result, err
			})
			// The application observation boundary must retain the actual error object.
			_, err := a.inspectState(context.Background(), cfg)
			if !errors.Is(err, original) || !run.OwnershipFailed(err) || later != 0 {
				t.Fatalf("inspection: %v later=%d", err, later)
			}
			failed, later, owner.poison = false, 0, nil
			var code int
			if prior {
				gitRunner.gitName, gitRunner.gitEmail = "", ""
				p := plan.Plan{ConfigureGit: true, SSHStatus: "ready", GitHubStatus: "ready"}
				code = a.preparePlan(context.Background(), cfg, p, ui.UI{In: strings.NewReader("y\nUser\nuser@example.com\n"), Out: out})
				if gitRunner.gitName != "User" || gitRunner.gitEmail != "user@example.com" {
					t.Fatal("prior mutation missing")
				}
			} else {
				code = a.Prepare(context.Background())
			}
			if code != Fatal || !failed || later != 0 || owner.poison != original {
				t.Fatalf("code=%d failed=%v later=%d output=%s", code, failed, later, out)
			}
			if strings.Contains(out.String(), "Earlier changes may remain") != prior || strings.Contains(out.String(), "Interrupted.") {
				t.Fatal(out.String())
			}
			if !prior && strings.Contains(out.String(), "Continue?") {
				t.Fatal("inspection failure reached approval", out.String())
			}
		})
	}
}
