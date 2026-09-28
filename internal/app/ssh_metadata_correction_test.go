package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/inspect"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
)

type sshMetadataTransport func(*http.Request) (*http.Response, error)

func (f sshMetadataTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func sshMetadataCauses() map[string]error {
	owner := &run.OwnershipError{Err: errors.New("original SSH metadata ownership failure")}
	return map[string]error{"ownership": owner, "wrapped ownership": fmt.Errorf("metadata transport: %w", owner), "cancel": context.Canceled, "deadline": context.DeadlineExceeded}
}

func TestSSHMetadataStopsPrepare(t *testing.T) {
	causes := sshMetadataCauses()
	causes["real HTTP timeout"] = context.DeadlineExceeded
	for name, cause := range causes {
		t.Run(name, func(t *testing.T) {
			a, out, base := noActionPrepareRuntime(t, false)
			owner := &gateOwner{}
			a.Ownership = owner
			before := readDoctorSSHFiles(t, filepath.Join(a.Home, ".ssh"))
			calls, failedAt, requests, later, laterGitHub := 0, 0, 0, 0, 0
			a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				calls++
				if requests != 0 {
					later++
					if s.Name == "gh" {
						laterGitHub++
					}
				}
				if s.Name == "sudo" || s.Interactive {
					t.Fatalf("unexpected mutation: %+v", s)
				}
				return base.Run(ctx, s)
			})
			transport := http.RoundTripper(sshMetadataTransport(func(*http.Request) (*http.Response, error) { return nil, cause }))
			if name == "real HTTP timeout" {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					select {
					case <-r.Context().Done():
					case <-time.After(2 * time.Second):
					}
				}))
				defer server.Close()
				transport = server.Client().Transport
				a.SSHMetadataURL = server.URL
			}
			var fetched error
			a.SSHHTTP = &http.Client{Transport: sshMetadataTransport(func(r *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(r)
				fetched = err
				requests++
				failedAt = calls
				if run.OwnershipFailed(cause) {
					owner.poison = cause
				}
				return response, err
			})}
			if name == "real HTTP timeout" {
				a.SSHHTTP.Timeout = 50 * time.Millisecond
			}
			ctx := context.Background() // The returned cause, not the parent, must stop planning.
			code := a.Prepare(ctx)
			t.Logf("Prepare code=%d calls=%d at-metadata=%d requests=%d later=%d later-GitHub=%d", code, calls, failedAt, requests, later, laterGitHub)
			if code != Fatal || requests != 1 || fetched == nil || later != 0 || laterGitHub != 0 || owner.activations != 0 || ctx.Err() != nil {
				t.Fatalf("code=%d requests=%d later=%d activations=%d output=%s", code, requests, later, owner.activations, out)
			}
			// A real http.Client timeout has deadline identity but may describe
			// the transport's cancellation instead of spelling out the sentinel.
			// Assert its identity at the error-returning boundary below.
			if (name != "real HTTP timeout" && !strings.Contains(out.String(), cause.Error())) || strings.Contains(out.String(), "Continue?") || !strings.Contains(out.String(), "Workstation preparation stopped.") {
				t.Fatal(out.String())
			}
			calls, failedAt, requests, later, laterGitHub, owner.poison = 0, 0, 0, 0, 0, nil
			_, err := a.inspectState(ctx, config.Config{})
			if !errors.Is(err, cause) || requests != 1 || later != 0 {
				t.Fatalf("orchestration lost cause: %v requests=%d later=%d", err, requests, later)
			}
			if run.OwnershipFailed(cause) && !run.OwnershipFailed(err) {
				t.Fatal("ownership classification lost")
			}
			after := readDoctorSSHFiles(t, filepath.Join(a.Home, ".ssh"))
			for name, data := range before {
				if string(data) != string(after[name]) {
					t.Fatalf("mutated %s", name)
				}
			}
		})
	}
}

func TestSSHMetadataAvailabilityPlanning(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint(unavailable), func(t *testing.T) {
			a, out, base := noActionPrepareRuntime(t, unavailable)
			owner := &gateOwner{}
			a.Ownership = owner
			want := Success
			if unavailable {
				want = Issues
			}
			if code := a.Prepare(context.Background()); code != want || owner.activations != 0 {
				t.Fatalf("code=%d output=%s", code, out)
			}
			githubCalls := 0
			for _, s := range base.calls {
				if s.Name == "gh" {
					githubCalls++
				}
				if s.Name == "sudo" || s.Interactive {
					t.Fatalf("unexpected mutation: %+v", s)
				}
			}
			if githubCalls != 2 {
				t.Fatalf("ordinary metadata behavior changed: GitHub calls=%d", githubCalls)
			}
		})
	}
}

// Doctor intentionally performs local inspection only. A metadata failure is
// unreachable there; local SSH execution causes must still stop its real path.
func TestDoctorSSHInspectionStopsAndSkipsRemoteMetadata(t *testing.T) {
	for name, cause := range sshMetadataCauses() {
		t.Run(name, func(t *testing.T) {
			a, out, base := noActionPrepareRuntime(t, false)
			owner := &gateOwner{}
			a.Ownership = owner
			requests, failed, later := 0, false, 0
			a.SSHHTTP = &http.Client{Transport: sshMetadataTransport(func(*http.Request) (*http.Response, error) { requests++; return nil, cause })}
			a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				if failed {
					later++
				}
				if s.Name == "ssh" && s.Args[0] == "-G" {
					failed = true
					return run.Result{}, cause
				}
				if s.Name == "sudo" || s.Interactive {
					t.Fatalf("unexpected mutation: %+v", s)
				}
				return base.Run(ctx, s)
			})
			_, err := (inspect.Workstation{Runner: a.Runner, Home: a.Home, PacmanConf: a.PacmanConf, SkipAgent: true}).Local(context.Background())
			if !errors.Is(err, cause) || !failed || later != 0 {
				t.Fatalf("err=%v failed=%v later=%d", err, failed, later)
			}
			failed = false
			if code := a.Doctor(context.Background()); code != Fatal || !failed || later != 0 || requests != 0 || owner.activations != 0 {
				t.Fatalf("code=%d failed=%v later=%d requests=%d output=%s", code, failed, later, requests, out)
			}
			if !strings.Contains(out.String(), cause.Error()) {
				t.Fatal(out.String())
			}
			a.Runner = base
			out.Reset()
			if code := a.Doctor(context.Background()); code != Success || requests != 0 {
				t.Fatalf("offline Doctor changed: code=%d requests=%d output=%s", code, requests, out)
			}
		})
	}
}

func TestSSHMetadataFailureAfterEarlierMutation(t *testing.T) {
	for name, cause := range sshMetadataCauses() {
		for _, duringConfigure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/configure=%v", name, duringConfigure), func(t *testing.T) {
				a, out, base := noActionPrepareRuntime(t, false)
				owner := &gateOwner{}
				a.Ownership = owner
				gitRunner := &prepareRunner{home: a.Home}
				requests, later := 0, 0
				a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
					if requests != 0 {
						later++
					}
					if s.Name == "sudo" {
						t.Fatalf("unexpected sudo: %+v", s)
					}
					if s.Name == "git" {
						return gitRunner.Run(ctx, s)
					}
					return base.Run(ctx, s)
				})
				a.SSHHTTP = &http.Client{Transport: sshMetadataTransport(func(*http.Request) (*http.Response, error) {
					requests++
					if run.OwnershipFailed(cause) {
						owner.poison = cause
					}
					return nil, cause
				})}
				p := plan.Plan{ConfigureGit: true, ConfigureSSH: duringConfigure, AuthenticateGitHub: duringConfigure, SSHStatus: "ready", GitHubStatus: "ready"}
				terminal := func() ui.UI { return ui.UI{In: strings.NewReader("y\nUser\nuser@example.com\n"), Out: out} }
				// Exercise the error-returning boundary as well as the final user report.
				if duringConfigure {
					result := a.executePlan(context.Background(), p, terminal())
					if result.status != Fatal || !result.applied || !result.stopInspection || len(result.problems) == 0 || !errors.Is(result.problems[len(result.problems)-1].Err, cause) {
						t.Fatalf("lost execution cause: %+v", result)
					}
				} else {
					_, err := a.inspectState(context.Background(), config.Config{})
					if !errors.Is(err, cause) {
						t.Fatalf("lost inspection cause: %v", err)
					}
				}
				if requests != 1 || later != 0 {
					t.Fatalf("requests=%d later=%d", requests, later)
				}
				requests, later, owner.poison = 0, 0, nil
				gitRunner.gitName, gitRunner.gitEmail = "", ""
				out.Reset()
				code := a.preparePlan(context.Background(), config.Config{}, p, terminal())
				if code != Fatal || requests != 1 || later != 0 || gitRunner.gitName != "User" || gitRunner.gitEmail != "user@example.com" {
					t.Fatalf("code=%d requests=%d later=%d output=%s", code, requests, later, out)
				}
				if !strings.Contains(out.String(), cause.Error()) || !strings.Contains(out.String(), "Earlier changes may remain") {
					t.Fatal(out.String())
				}
			})
		}
	}
}
