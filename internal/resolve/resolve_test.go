package resolve

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type missingRunner struct{}

func (missingRunner) Run(context.Context, run.Spec) (run.Result, error) {
	return run.Result{Stderr: "error: target not found: steam"}, errors.New("exit 1")
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDisabledMultilibPackageResolvesThroughOfficialAPI(t *testing.T) {
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body := `{"valid":true,"results":[{"pkgname":"steam","repo":"multilib","arch":"x86_64","depends":["lib32-glibc"],"optdepends":[],"conflicts":[]}]}`
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	resolver := Resolver{Runner: missingRunner{}, Client: client}
	pkg, found, err := resolver.Pacman(context.Background(), "steam")
	if err != nil || !found || pkg.Repository != "multilib" || len(pkg.Required) != 1 {
		t.Fatalf("package=%#v found=%v err=%v", pkg, found, err)
	}
}
