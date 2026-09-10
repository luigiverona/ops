package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
)

type diagnosticRunner func(context.Context, run.Spec) (run.Result, error)

func (f diagnosticRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) { return f(ctx, s) }

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDoctorSourceRecovery(t *testing.T) {
	for _, source := range []config.Source{config.Pacman, config.AUR, config.Flatpak} {
		for _, absent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/absent=%v", source, absent), func(t *testing.T) {
				a, out, base := noActionPrepareRuntime(t, false)
				id := "example"
				if source == config.Flatpak {
					id = "org.example.App"
				}
				data := fmt.Sprintf("version=2\n%s=[%q]\n", source, id)
				if err := os.WriteFile(config.Path(a.Home), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
					if s.Name == "pacman" && s.Args[0] == "-Si" {
						return run.Result{Stderr: "database unavailable; target not found"}, errors.New("query failed")
					}
					return base.Run(ctx, s)
				})
				a.SourceHTTP = &http.Client{Transport: diagnosticTransport(func(*http.Request) (*http.Response, error) {
					status, body := 503, ""
					if absent {
						status = 200
						switch source {
						case config.Pacman:
							body = `{"valid":true,"results":[]}`
						case config.AUR:
							body = `{"version":5,"type":"multiinfo","resultcount":0,"results":[]}`
						case config.Flatpak:
							status = 404
							body = `{"detail":"App not found"}`
						}
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				if code := a.Doctor(context.Background()); code != Issues {
					t.Fatalf("code=%d: %s", code, out)
				}
				text := out.String()
				if strings.Contains(text, "Check ~/.config/ops/apps.toml.") != absent || strings.Contains(text, "Retry later.") == absent {
					t.Fatalf("incorrect recovery: %s", text)
				}
				if !absent && (strings.Contains(text, "check the declared identifier") || strings.Contains(text, "Run ops to prepare")) {
					t.Fatalf("outage blamed configuration: %s", text)
				}
				for _, s := range base.calls {
					if s.Name == "sudo" || s.Interactive {
						t.Fatalf("doctor mutation: %#v", s)
					}
				}
				got, _ := os.ReadFile(config.Path(a.Home))
				if string(got) != data {
					t.Fatal("doctor changed declaration")
				}
			})
		}
	}
}

func TestDoctorKnownUnmetAndHealthyConfiguration(t *testing.T) {
	a, out, base := noActionPrepareRuntime(t, false)
	// Ready managed configuration never asks for live SSH authentication or an agent.
	a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
		if s.Name == "ssh-add" || s.Name == "ssh" && s.Args[0] != "-G" {
			t.Fatalf("health requires session authentication: %#v", s)
		}
		return base.Run(ctx, s)
	})
	if code := a.Doctor(context.Background()); code != Success || out.String() != "Workstation healthy.\n" {
		t.Fatalf("healthy=%d %s", code, out)
	}
	if err := os.WriteFile(config.Path(a.Home), []byte("version=2\nflatpak=[\"org.example.App\"]"), 0600); err != nil {
		t.Fatal(err)
	}
	a.SourceHTTP = &http.Client{Transport: diagnosticTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"org.example.App"}`))}, nil
	})}
	out.Reset()
	if code := a.Doctor(context.Background()); code != Issues {
		t.Fatalf("unmet=%d %s", code, out)
	}
	if !strings.Contains(out.String(), "The declared application is not installed.") || !strings.Contains(out.String(), "Run ops to prepare") || strings.Contains(out.String(), "apps.toml") || strings.Contains(out.String(), "Retry later") {
		t.Fatal(out)
	}
}

func TestFailureAndFinalObservationContracts(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		fail, present, inspectFail bool
	}{
		{"failure and unmet", true, false, false},
		{"command success but unmet", false, false, false},
		{"failure but ready", true, true, false},
		{"failure then inspection failure", true, false, true},
		{"success then inspection failure", false, false, true},
		{"successful convergence", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, out, base := noActionPrepareRuntime(t, false)
			cfg := config.Config{Version: 2, Applications: []config.Application{{Source: config.Flatpak, Identifier: "org.example.App"}, {Source: config.Flatpak, Identifier: "org.example.Later"}}}
			p := plan.Build(cfg, readyExecutionState(), plan.Facts{
				cfg.Applications[0]: {State: plan.Install}, cfg.Applications[1]: {State: plan.Install},
			})
			later, inspected := false, false
			a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				if s.Name == "pacman" && s.Args[0] == "-Q" {
					return run.Result{}, nil
				}
				if s.Name == "flatpak" && s.Args[0] == "install" {
					if s.Args[len(s.Args)-1] == "org.example.Later" {
						later = true
						return run.Result{}, nil
					}
					if tc.fail {
						if s.FailureOutput != run.FailureCombined {
							t.Fatal("Flatpak evidence not enabled")
						}
						return run.Result{}, fmt.Errorf("install application: %w", &run.Error{Name: "flatpak", Err: errors.New("exit status 1"), Evidence: "error: Failed to install: checksum mismatch"})
					}
					return run.Result{}, nil
				}
				if s.Name == "pacman" && s.Args[0] == "-Qq" {
					inspected = true
					if tc.inspectFail {
						return run.Result{}, errors.New("package database inspection unavailable")
					}
				}
				if s.Name == "flatpak" && s.Args[0] == "list" {
					result := "org.example.Later\n"
					if tc.present {
						result += "org.example.App\n"
					}
					return run.Result{Stdout: result}, nil
				}
				return base.Run(ctx, s)
			})
			code := a.preparePlan(context.Background(), cfg, p, ui.UI{In: strings.NewReader("y\n"), Out: out})
			want := Issues
			if tc.inspectFail {
				want = Fatal
			} else if !tc.fail && tc.present {
				want = Success
			}
			if code != want || !later || !inspected {
				t.Fatalf("code=%d later=%v inspected=%v: %s", code, later, inspected, out)
			}
			text := out.String()
			if strings.Count(text, "checksum mismatch") != boolInt(tc.fail) {
				t.Fatalf("lost/duplicate concrete evidence: %s", text)
			}
			if !tc.fail && strings.Contains(text, "\nFailed\n") {
				t.Fatalf("invented command failure: %s", text)
			}
			if tc.inspectFail {
				if !strings.Contains(text, "Unable to verify final workstation state.") || strings.Contains(text, "Workstation setup incomplete.") || strings.Contains(text, "Workstation ready.") {
					t.Fatalf("invented final state: %s", text)
				}
			} else if !tc.present {
				if !strings.Contains(text, "the declared application is not installed from its selected source") || strings.Count(text, "\norg.example.App\n") != 1 {
					t.Fatalf("lost/duplicate final observation: %s", text)
				}
			} else if tc.fail && !strings.Contains(text, "Workstation configuration is ready; operations reported issues.") {
				t.Fatalf("healthy state obscured operation failure: %s", text)
			}
			if strings.Contains(text, "\norg.example.Later\n") {
				t.Fatalf("successful application in failure report: %s", text)
			}
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestCoreFailureStillReinspectsWithoutContinuingMutation(t *testing.T) {
	a, out, base := noActionPrepareRuntime(t, false)
	inspected := false
	a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
		if s.Name == "sudo" && len(s.Args) > 2 && s.Args[1] == "pacman" {
			if s.Args[2] != "-Syu" {
				t.Fatal("continued package mutation")
			}
			return run.Result{}, &run.Error{Name: "sudo", Err: errors.New("exit status 1"), Presented: true, Evidence: "already streamed transaction diagnostic"}
		}
		if s.Name == "sudo" {
			return run.Result{}, nil
		}
		if s.Name == "pacman" && s.Args[0] == "-Qq" {
			inspected = true
		}
		return base.Run(ctx, s)
	})
	cfg := config.Config{Version: 2}
	p := plan.Build(cfg, readyExecutionState(), nil)
	p.FullUpgrade = true
	code := a.preparePlan(context.Background(), cfg, p, ui.UI{In: strings.NewReader("y\n"), Out: out})
	if code != Fatal || !inspected || !strings.Contains(out.String(), "Arch system upgrade") || !strings.Contains(out.String(), "Workstation configuration is ready;") {
		t.Fatalf("code=%d inspected=%v: %s", code, inspected, out)
	}
	if strings.Contains(out.String(), "Recent output") || strings.Contains(out.String(), "already streamed") {
		t.Fatal("replayed streamed failure")
	}
}

func TestWrappedEvidenceRenderingAndEmptyOutput(t *testing.T) {
	for _, evidence := range []string{"", "src/main.c:42: undefined reference\n==> ERROR: A failure occurred in build()."} {
		var out bytes.Buffer
		underlying := &run.Error{Name: "makepkg", Err: errors.New("exit status 4"), Evidence: evidence}
		wrapped := fmt.Errorf("build browser: %w", fmt.Errorf("reviewed source: %w", underlying))
		Runtime{Out: &out}.report("ready", "ready", "ready", []issue{*setupIssue("browser", wrapped)})
		if strings.Contains(out.String(), "Recent output:") != (evidence != "") {
			t.Fatal(out.String())
		}
		if evidence != "" && strings.Count(out.String(), "undefined reference") != 1 {
			t.Fatal(out.String())
		}
		if !errors.Is(wrapped, underlying) {
			t.Fatal("lost underlying error")
		}
	}
}

func TestDoctorReportsServiceAndIdentityConfiguration(t *testing.T) {
	var out bytes.Buffer
	p := plan.Plan{Core: readyCore(), GitStatus: "configuration required", SSHStatus: "configuration required", GitHubStatus: "configuration required", Applications: []plan.Application{
		{Declaration: config.Application{Source: config.Pacman, Identifier: "mullvad-vpn"}, State: plan.Configure, Services: []string{"mullvad-daemon.service"}},
	}}
	if code := (Runtime{Out: &out}).reportDoctor(p, nil, false); code != Issues {
		t.Fatalf("code=%d", code)
	}
	for _, want := range []string{"Git: configuration required", "SSH: configuration required", "GitHub: configuration required", "Required service is not enabled and active: mullvad-daemon.service", "Run ops to prepare"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, &out)
		}
	}
	if strings.Contains(out.String(), "apps.toml") || strings.Contains(out.String(), "Retry later") {
		t.Fatal(out.String())
	}
}

func TestFinalFlatpakInspectionFailureIsUnknownNotMissing(t *testing.T) {
	for _, query := range []string{"list", "remotes"} {
		t.Run(query, func(t *testing.T) {
			a, out, base := noActionPrepareRuntime(t, false)
			cfg := config.Config{Version: 2}
			p := plan.Build(cfg, readyExecutionState(), nil)
			p.AddFlathub = true
			a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				if s.Name == "pacman" && s.Args[0] == "-Q" {
					return run.Result{}, nil
				}
				if s.Name == "flatpak" && s.Args[0] == query {
					return run.Result{}, errors.New("unable to read user installation")
				}
				return base.Run(ctx, s)
			})
			if code := a.preparePlan(context.Background(), cfg, p, ui.UI{In: strings.NewReader("y\n"), Out: out}); code != Fatal {
				t.Fatalf("code=%d %s", code, out)
			}
			if !strings.Contains(out.String(), "Unable to verify final workstation state.") || strings.Contains(out.String(), "Workstation setup incomplete.") || strings.Contains(out.String(), "apps.toml") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestFinalUnavailableFreshnessDoesNotInventUnmetConfiguration(t *testing.T) {
	p := plan.Build(config.Config{Version: 2}, readyExecutionState(), nil)
	p.SSHStatus = "unavailable"
	p.SSHHostKeyFreshness = plan.SSHHostKeyFreshnessUnavailable
	result := execution{applied: true}
	result.observe(p)
	var out bytes.Buffer
	if code := (Runtime{Out: &out, Err: &out}).reportExecution(result); code != Issues {
		t.Fatalf("code=%d", code)
	}
	text := out.String()
	if !strings.Contains(text, "Workstation prepared; checks remain unavailable.") || !strings.Contains(text, "Retry later.") {
		t.Fatal(text)
	}
	if strings.Contains(text, "Workstation setup incomplete.") || strings.Contains(text, "Workstation ready.") || strings.Contains(text, "Run ops again.") {
		t.Fatalf("unknown freshness changed configuration readiness: %s", text)
	}
}
