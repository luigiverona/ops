package resolve

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

func TestIndependentPacmanRejectsUnexpectedRepository(t *testing.T) {
	for _, repo := range []string{"custom", "extra"} {
		runner := &transactionRunner{output: "Repository : " + repo + "\nName : firefox\n"}
		requests := 0
		client := &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
			requests++
			if strings.Join(req.URL.Query()["repo"], ",") != "Core,Extra,Multilib" {
				t.Fatal(req.URL)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":2,"valid":true,"count":1,"page":1,"num_pages":1,"results":[{"pkgname":"firefox","repo":"extra","arch":"x86_64"}]}`))}, nil
		})}
		pkg, found, err := (Resolver{Runner: runner, Client: client}).Pacman(context.Background(), "firefox")
		if repo == "custom" {
			if err == nil || found || requests != 0 {
				t.Fatalf("unexpected source identity accepted: %+v %v", pkg, err)
			}
			continue
		}
		if err != nil || !found || pkg.Repository != "extra" {
			t.Fatalf("%+v %v", pkg, err)
		}
		if requests != 0 {
			t.Fatalf("API calls %d for %s", requests, repo)
		}
	}
}
func TestOfficialDependencyRejectsCustomOrAmbiguousTransaction(t *testing.T) {
	for _, output := range []string{"custom/rust\tcargo\n", "extra/rust\tcargo\ncustom/llvm-libs\t\n", "extra/rust\tcargo\ncore/rust\tcargo\n", "unknown/rust\tcargo\n", "extra/rust\tcargo\nextra/lib\t\textra\n"} {
		_, err := (Resolver{Runner: &dependencyRunner{transaction: output}}).OfficialDependency(context.Background(), "cargo")
		var queryErr *QueryError
		if !errors.As(err, &queryErr) {
			t.Fatalf("accepted %q: %v", output, err)
		}
	}
}
func TestNativeCustomDeclarationIsNotReady(t *testing.T) {
	declaration := config.Application{Source: config.Pacman, Identifier: "firefox"}
	state := plan.State{Installed: map[string]bool{"firefox": true}, Foreign: map[string]bool{}}
	facts := Applications(context.Background(), config.Config{Applications: []config.Application{declaration}}, state, fakeResolver{pacman: map[string]plan.Package{"firefox": {Name: "firefox", Repository: "extra"}}})
	app := plan.Build(config.Config{Applications: []config.Application{declaration}}, state, facts).Applications[0]
	if app.State != plan.Install || app.Package.Repository != "extra" || !strings.Contains(app.Cause, "requires authenticated official content repair/reverification") {
		t.Fatalf("%+v", app)
	}
}

type mismatchedInstalledProvider struct{ dependencyRunner }

func (r *mismatchedInstalledProvider) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	result, err := r.dependencyRunner.Run(ctx, s)
	if s.Args[0] == "-Qi" {
		result.Stdout = strings.Replace(result.Stdout, "Arch fixture", "Custom packager", 1)
	}
	return result, err
}
func TestSatisfiedCustomProviderFailsClosed(t *testing.T) {
	runner := &mismatchedInstalledProvider{dependencyRunner{satisfied: true, transaction: "extra/rust\tcargo\n"}}
	binding, err := (Resolver{Runner: runner}).OfficialDependency(context.Background(), "cargo")
	if err != nil || binding.Satisfied || binding.Provider != "extra/rust" {
		t.Fatalf("custom provider did not require authenticated repair: %+v %v", binding, err)
	}
}

func TestClosureKeepsExactTargetAndItsVirtualProviderDependency(t *testing.T) {
	runner := &dependencyRunner{transaction: "extra/certificates\t\nextra/certificates-store\tcertificates\n"}
	binding, err := (Resolver{Runner: runner}).OfficialDependency(context.Background(), "certificates")
	if err != nil || binding.Provider != "extra/certificates" || strings.Join(binding.Packages, ",") != "extra/certificates,extra/certificates-store" {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
}

type closureMetadataRunner struct{ output string }

func (r closureMetadataRunner) Run(_ context.Context, s run.Spec) (run.Result, error) {
	switch s.Args[0] {
	case "-T":
		return run.Result{Stdout: "builder\n"}, &run.Error{Name: "pacman", Err: dependencyExit(127)}
	case "-Sp":
		return run.Result{Stdout: "extra/builder\t\n"}, nil
	case "-Si":
		return run.Result{Stdout: r.output}, nil
	}
	return run.Result{}, errors.New("unexpected query")
}

func TestClosureRejectsMissingMalformedAndChangedDependencyMetadata(t *testing.T) {
	valid := testpkg.Info("extra/builder")
	for _, output := range []string{
		strings.Replace(valid, "Depends On : None\n", "", 1),
		strings.Replace(valid, "Depends On : None", "Depends On : !!!", 1),
		strings.Replace(valid, "Repository : extra", "Repository : core", 1),
		valid + "Depends On : None\n",
	} {
		if _, err := (Resolver{Runner: closureMetadataRunner{output}}).OfficialDependency(context.Background(), "builder"); err == nil {
			t.Fatalf("accepted malformed dependency metadata: %q", output)
		}
	}
}

func TestIndependentSourceOutageCannotBeMaskedByAPI(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":2,"valid":true,"count":0,"page":1,"num_pages":1,"results":[]}`))}, nil
	})}
	_, found, err := (Resolver{Runner: diagnosticPacmanRunner{&archtrust.SourceError{Err: errors.New("offline")}}, Client: client}).Pacman(context.Background(), "git")
	var queryErr *QueryError
	if found || !errors.As(err, &queryErr) || requests != 0 {
		t.Fatalf("outage masked: %v %v requests=%d", found, err, requests)
	}
}
