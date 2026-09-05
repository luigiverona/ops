package app

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/release"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/version"
)

type updateRunner struct {
	calls []run.Spec
}

func (r *updateRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	r.calls = append(r.calls, spec)
	if spec.Name == "uname" && len(spec.Args) == 1 && spec.Args[0] == "-m" {
		return run.Result{Stdout: "x86_64\n"}, nil
	}
	return run.Result{}, errors.New("unexpected command: " + spec.Name + " " + strings.Join(spec.Args, " "))
}

func TestRuntimeUpdateAlreadyCurrentReturnsSuccessWithoutSudoOrReplacement(t *testing.T) {
	setStableVersion(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(version.Value + "\n"))
	}))
	defer server.Close()
	routeReleaseRequests(t, server)

	var output bytes.Buffer
	runner := &updateRunner{}
	code := Runtime{
		Runner:    runner,
		Out:       &output,
		Err:       &output,
		EUID:      func() int { return 1000 },
		OSRelease: archOSRelease(t),
	}.Update(context.Background())

	if code != Success {
		t.Fatalf("code=%d\n%s", code, output.String())
	}
	for _, want := range []string{"Update", "current         " + version.Value, "latest          " + version.Value, "status          up to date"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q:\n%s", want, output.String())
		}
	}
	assertUpdateNeverPrivilegedOrReplaced(t, runner.calls)
}

func TestRuntimeUpdateLatestDownloadFailureIsFatalBeforeSudoOrReplacement(t *testing.T) {
	setStableVersion(t)
	oldTransport := http.DefaultTransport
	http.DefaultTransport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("release service unavailable")
	})
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	var output bytes.Buffer
	runner := &updateRunner{}
	code := Runtime{
		Runner:    runner,
		Out:       &output,
		Err:       &output,
		EUID:      func() int { return 1000 },
		OSRelease: archOSRelease(t),
	}.Update(context.Background())

	if code != Fatal {
		t.Fatalf("code=%d\n%s", code, output.String())
	}
	if !strings.Contains(output.String(), "resolve latest release") || !strings.Contains(output.String(), "release service unavailable") {
		t.Fatalf("failure was not reported:\n%s", output.String())
	}
	assertUpdateNeverPrivilegedOrReplaced(t, runner.calls)
}

func archOSRelease(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(path, []byte("ID=arch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func setStableVersion(t *testing.T) {
	t.Helper()
	previous := version.Value
	version.Value = "1.0.2"
	t.Cleanup(func() { version.Value = previous })
}

func assertUpdateNeverPrivilegedOrReplaced(t *testing.T, calls []run.Spec) {
	t.Helper()
	for _, call := range calls {
		if call.Name == "sudo" {
			t.Fatalf("sudo was requested: %#v", call)
		}
		if call.Name != "uname" {
			t.Fatalf("replacement or unexpected command ran: %#v", call)
		}
	}
}

func routeReleaseRequests(t *testing.T, server *httptest.Server) {
	t.Helper()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	releaseURL, err := url.Parse(release.DefaultBase)
	if err != nil {
		t.Fatal(err)
	}
	oldTransport := http.DefaultTransport
	http.DefaultTransport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != releaseURL.Scheme || request.URL.Host != releaseURL.Host {
			return nil, errors.New("unexpected HTTP request: " + request.URL.String())
		}
		forwarded := request.Clone(request.Context())
		forwarded.URL.Scheme = serverURL.Scheme
		forwarded.URL.Host = serverURL.Host
		forwarded.Host = serverURL.Host
		return oldTransport.RoundTrip(forwarded)
	})
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
