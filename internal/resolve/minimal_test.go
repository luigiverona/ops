package resolve

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

type minimalResolverRunner struct{ calls []run.Spec }

func (r *minimalResolverRunner) Run(_ context.Context, s run.Spec) (run.Result, error) {
	r.calls = append(r.calls, s)
	if s.Name == "pacman" && s.Args[0] == "-Qq" {
		return run.Result{}, nil
	}
	if s.Name == "pacman" && s.Args[0] == "-Si" {
		return run.Result{Stdout: testpkg.Info(s.Args[len(s.Args)-1])}, nil
	}
	if s.Name == "pacman" && s.Args[0] == "-T" {
		return run.Result{Stdout: "base-devel\n"}, &run.Error{Name: "pacman", Err: dependencyExit(127)}
	}
	if s.Name == "pacman" && s.Args[0] == "-Sp" {
		return run.Result{Stdout: "extra/base-devel\t\nextra/gcc\t\nextra/make\t\n"}, nil
	}
	return run.Result{}, errors.New("executable not installed: " + s.Name)
}

func TestMinimalAURAndFlatpakResolutionHasNoBootstrapCommandCycle(t *testing.T) {
	const oid = "0123456789012345678901234567890123456789"
	cfg, _ := config.Parse([]byte("version=2\naur=[\"example\"]\nflatpak=[\"org.example.App\"]"))
	runner := &minimalResolverRunner{}
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body := `{"id":"org.example.App"}`
		switch {
		case strings.HasPrefix(r.URL.Path, "/rpc/"):
			body = `{"version":5,"type":"multiinfo","resultcount":1,"results":[{"Name":"example","PackageBase":"example"}]}`
		case strings.HasSuffix(r.URL.Path, "/info/refs"):
			body = "001e# service=git-upload-pack\n0000" + packet(oid+" HEAD\x00object-format=sha1\n") + "0000"
		case strings.Contains(r.URL.Path, ".SRCINFO"):
			if r.URL.Query().Get("id") != oid {
				t.Fatal("unbound revision")
			}
			body = "pkgbase = example\npkgver = 1\npkgrel = 1\npkgname = example\n"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	facts := Applications(context.Background(), cfg, plan.State{}, Resolver{Runner: runner, Client: client})
	p := plan.Build(cfg, plan.State{}, facts)
	for _, app := range p.Applications {
		if app.State != plan.Install {
			t.Fatalf("app=%+v", app)
		}
	}
	if len(p.Applications[0].AURPackages) != 3 || !p.AddFlathub {
		t.Fatalf("incomplete dependency plan: %+v", p)
	}
	for _, call := range runner.calls {
		if call.Name != "pacman" || call.Interactive {
			t.Fatalf("hidden prerequisite or interaction: %+v", call)
		}
	}
}

func TestRemoteOutageIsNotAnUnresolvedDeclaration(t *testing.T) {
	cfg, _ := config.Parse([]byte("version=2\nflatpak=[\"org.example.App\"]"))
	client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })}
	facts := Applications(context.Background(), cfg, plan.State{}, Resolver{Client: client})
	if facts[cfg.Applications[0]].State != plan.Unavailable {
		t.Fatalf("outage misclassified: %+v", facts)
	}
}
