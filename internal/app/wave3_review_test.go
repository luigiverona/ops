package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
)

func TestDoctorAURAvailabilityDoesNotPrepareBuilds(t *testing.T) {
	a, out, base := noActionPrepareRuntime(t, false)
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GNUPGHOME", home)
	if err := os.WriteFile(filepath.Join(home, "pubring.kbx"), []byte("public keyring fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(a.Home), []byte("version=2\naur=[\"example\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const commit = "0123456789012345678901234567890123456789"
	a.SourceHTTP = &http.Client{Transport: diagnosticTransport(func(req *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.HasPrefix(req.URL.Path, "/rpc/"):
			body = `{"version":5,"type":"multiinfo","resultcount":1,"results":[{"Name":"example","PackageBase":"example"}]}`
		case strings.HasSuffix(req.URL.Path, "/info/refs"):
			ref := commit + " HEAD\x00object-format=sha1\n"
			body = "001e# service=git-upload-pack\n0000" + fmt.Sprintf("%04x%s", len(ref)+4, ref) + "0000"
		default:
			body = "pkgbase = example\npkgver = 1\npkgrel = 1\nvalidpgpkeys = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\npkgname = example\n"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	created, gpgCalled := false, false
	a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
		if s.Name == "pacman" && s.Args[0] == "-T" {
			return run.Result{}, nil
		}
		if s.Name == "gpg" {
			gpgCalled = true
			args := strings.Join(s.Args, " ")
			if strings.Contains(args, "--gpgconf-list") {
				return run.Result{Stdout: "use_keyboxd:16:0:\n"}, nil
			}
			for i, arg := range s.Args {
				if arg == "--homedir" && s.Args[i+1] != home {
					if data, err := os.ReadFile(filepath.Join(s.Args[i+1], "pubring.kbx")); err == nil && string(data) == "public keyring fixture" {
						created = true
					}
				}
			}
			return run.Result{}, nil
		}
		return base.Run(ctx, s)
	})
	code := a.Doctor(context.Background())
	if created || gpgCalled {
		t.Fatalf("doctor entered GPG build preparation: files created=%v gpg called=%v code=%d output=%s", created, gpgCalled, code, out)
	}
	if code != Issues || !strings.Contains(out.String(), "The declared application is not installed.") {
		t.Fatalf("code=%d %s", code, out)
	}
}

func TestDoctorDoesNotExecuteModifiedSSHConfiguration(t *testing.T) {
	a, out, base := noActionPrepareRuntime(t, false)
	path := filepath.Join(a.Home, ".ssh", "ops_config")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "match-executed")
	data = append(data, []byte(fmt.Sprintf("Match exec \"touch %s\"\n", marker))...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
		if s.Name == "ssh" {
			return (run.Exec{}).Run(ctx, s)
		}
		return base.Run(ctx, s)
	})
	code := a.Doctor(context.Background())
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("doctor executed SSH Match command; code=%d output=%s", code, out)
	}
}

func TestJoinedFailureEvidencePreservesDistinctCommands(t *testing.T) {
	var out strings.Builder
	streamed := &run.Error{Name: "sudo", Presented: true, Err: diagnosticExit(1)}
	cleanup := &run.Error{Name: "sudo", Evidence: "cannot remove protected staged artifact: permission denied", Err: diagnosticExit(1)}
	reportEvidence(&out, fmt.Errorf("install artifacts: %w", errors.Join(streamed, cleanup, fmt.Errorf("same cleanup: %w", cleanup))))
	if strings.Count(out.String(), cleanup.Evidence) != 1 {
		t.Fatalf("independent cleanup evidence lost or duplicated: %s", &out)
	}
}

func TestFinalObservationUpdatesEveryDistinctFailure(t *testing.T) {
	for _, state := range []plan.ApplicationState{plan.Ready, plan.Configure, plan.Unresolved} {
		declaration := config.Application{Source: config.Pacman, Identifier: "example"}
		result := execution{applied: true, problems: []issue{
			{State: "Failed", Name: declaration.Identifier, Source: string(declaration.Source), Cause: "installation failed", Impact: "setup incomplete"},
			{State: "Failed", Name: declaration.Identifier, Source: string(declaration.Source), Cause: "cleanup failed", Impact: "setup incomplete"},
		}}
		p := plan.Build(config.Config{Version: 2}, readyExecutionState(), nil)
		p.Applications = []plan.Application{{Declaration: declaration, State: state}}
		result.observe(p)
		if len(result.problems) != 2 {
			t.Fatalf("distinct failures lost or duplicated: %+v", result.problems)
		}
		for _, problem := range result.problems {
			if problem.Observed == "" || state == plan.Ready && problem.Impact != "" {
				t.Fatalf("stale final state: %+v", problem)
			}
		}
		var out strings.Builder
		code := (Runtime{Out: &out, Err: &out}).reportExecution(result)
		if code != Issues || strings.Count(out.String(), "installation failed") != 1 || strings.Count(out.String(), "cleanup failed") != 1 {
			t.Fatalf("code=%d %s", code, &out)
		}
	}
}

func TestCancellationSuppressesPreviouslyRecordedEvidence(t *testing.T) {
	a, out, base := noActionPrepareRuntime(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := config.Config{Version: 2, Applications: []config.Application{
		{Source: config.Flatpak, Identifier: "org.example.First"},
		{Source: config.Flatpak, Identifier: "org.example.Second"},
	}}
	p := plan.Build(cfg, readyExecutionState(), plan.Facts{cfg.Applications[0]: {State: plan.Install}, cfg.Applications[1]: {State: plan.Install}})
	a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
		if s.Name == "pacman" && s.Args[0] == "-Q" {
			return run.Result{}, nil
		}
		if s.Name == "flatpak" && s.Args[0] == "install" {
			if s.Args[len(s.Args)-1] == "org.example.Second" {
				cancel()
				return run.Result{}, context.Canceled
			}
			return run.Result{}, &run.Error{Name: "flatpak", Err: diagnosticExit(1), Evidence: "recorded concrete failure"}
		}
		if s.Name == "pacman" && s.Args[0] == "-Qq" {
			t.Fatal("reinspection after cancellation")
		}
		return base.Run(ctx, s)
	})
	code := a.preparePlan(ctx, cfg, p, ui.UI{In: strings.NewReader("y\n"), Out: out})
	if code != Fatal || !strings.Contains(out.String(), "Interrupted. Earlier changes may remain.") {
		t.Fatalf("code=%d %s", code, out)
	}
	for _, forbidden := range []string{"recorded concrete failure", "Recent output:", "\nIssues\n", "Workstation setup incomplete.", "Workstation ready."} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatalf("cancellation replayed diagnostic footer: %s", out)
		}
	}
}
