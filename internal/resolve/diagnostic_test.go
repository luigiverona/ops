package resolve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
)

func TestSourceAbsenceRequiresConclusiveResponse(t *testing.T) {
	for _, tc := range []struct {
		name              string
		source            config.Source
		status            int
		body              string
		absent, installed bool
	}{
		{"pacman absent", config.Pacman, 200, `{"version":2,"valid":true,"count":0,"page":1,"num_pages":1,"results":[]}`, true, false},
		{"pacman available", config.Pacman, 200, `{"version":2,"valid":true,"count":1,"page":1,"num_pages":1,"results":[{"pkgname":"example","repo":"extra","arch":"x86_64"}]}`, false, true},
		{"pacman architecture independent", config.Pacman, 200, `{"version":2,"valid":true,"count":1,"page":1,"num_pages":1,"results":[{"pkgname":"example","repo":"core","arch":"any"}]}`, false, true},
		{"pacman incomplete results", config.Pacman, 200, `{"version":2,"valid":true,"count":1,"page":1,"num_pages":2,"results":[]}`, false, false},
		{"pacman malformed record", config.Pacman, 200, `{"version":2,"valid":true,"count":1,"page":1,"num_pages":1,"results":[{}]}`, false, false},
		{"pacman different identifier", config.Pacman, 200, `{"version":2,"valid":true,"count":1,"page":1,"num_pages":1,"results":[{"pkgname":"other","repo":"extra","arch":"x86_64"}]}`, false, false},
		{"pacman repository unavailable", config.Pacman, 503, ``, false, false},
		{"pacman endpoint missing", config.Pacman, 404, ``, false, false},
		{"pacman invalid query", config.Pacman, 200, `{"valid":false,"results":[]}`, false, false},
		{"pacman malformed", config.Pacman, 200, `{}`, false, false},
		{"AUR absent", config.AUR, 200, `{"version":5,"type":"multiinfo","resultcount":0,"results":[]}`, true, false},
		{"AUR RPC unavailable", config.AUR, 503, ``, false, false},
		{"AUR RPC missing", config.AUR, 404, ``, false, false},
		{"AUR RPC error", config.AUR, 200, `{"version":5,"type":"error","resultcount":0,"results":[],"error":"not found"}`, false, false},
		{"AUR malformed", config.AUR, 200, `{}`, false, false},
		{"AUR wrong exact result", config.AUR, 200, `{"version":5,"type":"multiinfo","resultcount":1,"results":[{"Name":"other","PackageBase":"other"}]}`, false, false},
		{"Flathub absent or missing metadata", config.Flatpak, 404, `{"detail":"App not found"}`, false, false},
		{"Flathub available", config.Flatpak, 200, `{"id":"org.example.App"}`, false, true},
		{"Flathub unavailable", config.Flatpak, 503, ``, false, false},
		{"Flathub proxy missing", config.Flatpak, 404, `<html>not found</html>`, false, false},
		{"Flathub wrong endpoint", config.Flatpak, 404, `{"detail":"Not Found"}`, false, false},
		{"Flathub malformed", config.Flatpak, 200, `{}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "example"
			if tc.source == config.Flatpak {
				id = "org.example.App"
			}
			declaration := config.Application{Source: tc.source, Identifier: id}
			client := &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
				expected := map[config.Source]string{config.Pacman: "archlinux.org", config.AUR: "aur.archlinux.org", config.Flatpak: "flathub.org"}[tc.source]
				if req.URL.Host != expected {
					t.Fatalf("source fallback: %s", req.URL)
				}
				if tc.source == config.Pacman && strings.Join(req.URL.Query()["arch"], ",") != "x86_64,any" {
					t.Fatalf("query excludes architecture-independent packages: %s", req.URL)
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			facts := Applications(context.Background(), config.Config{Applications: []config.Application{declaration}}, plan.State{}, Resolver{Runner: missingRunner{}, Client: client})
			fact := facts[declaration]
			if fact.ConfirmedAbsent != tc.absent {
				t.Fatalf("absence=%+v", fact)
			}
			want := plan.Unavailable
			if tc.absent {
				want = plan.Unresolved
			}
			if tc.installed {
				want = plan.Install
			}
			if fact.State != want {
				t.Fatalf("state=%+v want=%s", fact, want)
			}
		})
	}
}

type diagnosticPacmanRunner struct{ err error }

func (r diagnosticPacmanRunner) Run(context.Context, run.Spec) (run.Result, error) {
	return run.Result{}, r.err
}

type dependencyExit int

func (e dependencyExit) Error() string { return fmt.Sprintf("exit status %d", e) }
func (e dependencyExit) ExitCode() int { return int(e) }

type dependencyErrorRunner struct {
	err   error
	calls int
}

func (r *dependencyErrorRunner) Run(context.Context, run.Spec) (run.Result, error) {
	r.calls++
	return run.Result{Stdout: "cargo\n"}, r.err
}

func TestDependencyInspectionErrorCannotMasqueradeAsMissingDependency(t *testing.T) {
	for _, underlying := range []error{errors.New("exit status 127"), &run.Error{Name: "pacman", Err: dependencyExit(1)}} {
		runner := &dependencyErrorRunner{err: underlying}
		_, err := (Resolver{Runner: runner}).OfficialDependency(context.Background(), "cargo")
		if runner.calls != 1 || !errors.Is(err, underlying) || !strings.Contains(err.Error(), "inspect installed dependency") {
			t.Fatalf("inspection error triggered repository resolution: calls=%d err=%v", runner.calls, err)
		}
	}
}

func TestPacmanQueryPreservesUnderlyingFailureWhenAPIIsUnavailable(t *testing.T) {
	underlying := &run.Error{Name: "pacman", Err: errors.New("exit status 1"), Evidence: "error: could not open database"}
	client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })}
	_, found, err := (Resolver{Runner: diagnosticPacmanRunner{underlying}, Client: client}).Pacman(context.Background(), "example")
	if found || !errors.Is(err, underlying) || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

type dependencyOutageResolver struct{ fakeResolver }

func (r dependencyOutageResolver) OfficialDependency(context.Context, string) (plan.OfficialDependency, error) {
	return plan.OfficialDependency{}, fmt.Errorf("dependency context: %w", &QueryError{Err: errors.New("database unavailable; target not found")})
}

func TestAURDependencyQueryFailureIsUnavailable(t *testing.T) {
	declaration := config.Application{Source: config.AUR, Identifier: "paru"}
	resolver := dependencyOutageResolver{fakeResolver{aur: map[string]plan.Package{"paru": {Name: "paru", PackageBase: "paru"}}}}
	fact := Applications(context.Background(), config.Config{Applications: []config.Application{declaration}}, plan.State{}, resolver)[declaration]
	var queryErr *QueryError
	if fact.State != plan.Unavailable || fact.ConfirmedAbsent || !errors.As(fact.Err, &queryErr) {
		t.Fatalf("repository outage became a declaration/build failure: %+v", fact)
	}
}

func TestInconclusiveTransactionMetadataIsAQueryFailure(t *testing.T) {
	for _, output := range []string{"invalid metadata", "other\t\n", "rust\tcargo\nrustup\tcargo\n"} {
		runner := &dependencyRunner{transaction: output}
		_, err := (Resolver{Runner: runner}).OfficialDependency(context.Background(), "cargo")
		var queryErr *QueryError
		if !errors.As(err, &queryErr) {
			t.Fatalf("%q became a build/declaration failure: %v", output, err)
		}
	}
	for _, output := range []string{"invalid metadata", "rust\nrust\n"} {
		_, err := (Resolver{Runner: &dependencyRunner{transaction: output}}).OfficialTransaction(context.Background(), []string{"rust"})
		var queryErr *QueryError
		if !errors.As(err, &queryErr) {
			t.Fatalf("%q became a build/declaration failure: %v", output, err)
		}
	}
}
