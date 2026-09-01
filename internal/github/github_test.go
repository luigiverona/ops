package github

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type call struct{ spec run.Spec }
type fakeRunner struct {
	calls []call
	fn    func(run.Spec) (run.Result, error)
}

func (f *fakeRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, call{spec})
	return f.fn(spec)
}

var testKey = publicKey(1)
var otherKey = publicKey(2)

func publicKey(fill byte) string {
	typeName := []byte("ssh-ed25519")
	blob := make([]byte, 4+len(typeName)+4+32)
	blob[3] = byte(len(typeName))
	copy(blob[4:], typeName)
	offset := 4 + len(typeName)
	blob[offset+3] = 32
	for i := offset + 4; i < len(blob); i++ {
		blob[i] = fill
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " test"
}

func TestAuthenticationStatesAndLoginFlags(t *testing.T) {
	auth := false
	f := &fakeRunner{fn: func(spec run.Spec) (run.Result, error) {
		if len(spec.Args) > 1 && spec.Args[0] == "auth" && spec.Args[1] == "login" {
			auth = true
			return run.Result{}, nil
		}
		if len(spec.Args) > 1 && spec.Args[0] == "auth" && spec.Args[1] == "status" && auth {
			return run.Result{}, nil
		}
		return run.Result{}, errors.New("unauthenticated")
	}}
	m := Manager{Runner: f}
	if m.Authenticated(context.Background()) {
		t.Fatal("unexpected auth")
	}
	if err := m.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(f.calls[1].spec.Args, " ")
	if !strings.Contains(args, "--git-protocol ssh") || !strings.Contains(args, "--skip-ssh-key") || !strings.Contains(args, "--scopes admin:public_key") {
		t.Fatalf("unsafe login args: %s", args)
	}
	if !f.calls[1].spec.Interactive || f.calls[1].spec.Interaction == "" {
		t.Fatalf("login terminal ownership was not declared: %#v", f.calls[1].spec)
	}
}

func TestRefreshSSHKeyScopeUsesSupportedGHCommand(t *testing.T) {
	f := &fakeRunner{fn: func(spec run.Spec) (run.Result, error) {
		if len(spec.Args) > 1 && spec.Args[0] == "auth" && spec.Args[1] == "refresh" {
			return run.Result{}, nil
		}
		if len(spec.Args) > 1 && spec.Args[0] == "auth" && spec.Args[1] == "status" {
			return run.Result{}, nil
		}
		return run.Result{}, errors.New("unexpected command")
	}}
	if err := (Manager{Runner: f}).RefreshSSHKeyScope(context.Background()); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(f.calls[0].spec.Args, " ")
	if args != "auth refresh --hostname github.com --scopes admin:public_key" || !f.calls[0].spec.Interactive || f.calls[0].spec.Interaction == "" {
		t.Fatalf("refresh=%#v", f.calls[0].spec)
	}
}

func TestIsSSHKeyScopeError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "known user keys scope diagnostic", err: ghKeyAPIError(`gh: This API operation needs the "admin:public_key" scope`), want: true},
		{name: "ordinary 401", err: ghKeyAPIError("gh: Bad credentials (HTTP 401)"), want: false},
		{name: "ordinary 403", err: ghKeyAPIError("gh: Resource not accessible by integration (HTTP 403)"), want: false},
		{name: "unrelated 404", err: ghKeyAPIError("gh: Not Found (HTTP 404)"), want: false},
		{name: "rate limit", err: ghKeyAPIError("gh: API rate limit exceeded (HTTP 403)"), want: false},
		{name: "network", err: ghKeyAPIError("dial tcp: network unavailable"), want: false},
		{name: "malformed JSON", err: errors.New("parse GitHub SSH keys: invalid character '<' looking for beginning of value"), want: false},
		{name: "unrelated scope text", err: ghKeyAPIError(`admin:public_key is mentioned by an unrelated scope error`), want: false},
		{name: "wrong gh operation", err: &run.Error{Name: "gh", Args: []string{"api", "user/repos"}, Stderr: `This API operation needs the "admin:public_key" scope`, Err: errors.New("exit status 1")}, want: false},
		{name: "unstructured wrapped text", err: errors.New(`gh: This API operation needs the "admin:public_key" scope`), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsSSHKeyScopeError(test.err); got != test.want {
				t.Fatalf("IsSSHKeyScopeError(%v)=%v, want %v", test.err, got, test.want)
			}
		})
	}
}

func ghKeyAPIError(stderr string) error {
	return &run.Error{Name: "gh", Args: []string{"api", "--paginate", "user/keys"}, Stderr: stderr, Err: errors.New("exit status 1")}
}

func TestKeysAndDeleteFailure(t *testing.T) {
	f := &fakeRunner{fn: func(spec run.Spec) (run.Result, error) {
		if spec.Args[0] == "api" {
			return run.Result{Stdout: `[{"id":1,"title":"one","key":"` + testKey + `"},{"id":2,"title":"two","key":"` + otherKey + `"}]`}, nil
		}
		return run.Result{}, errors.New("delete denied")
	}}
	m := Manager{Runner: f}
	keys, err := m.Keys(context.Background())
	if err != nil || len(keys) != 2 || keys[0].Fingerprint == "" {
		t.Fatalf("keys=%#v err=%v", keys, err)
	}
	if err := m.Delete(context.Background(), keys[0]); err == nil {
		t.Fatal("expected delete error")
	}
}

func TestAddManagedAndDuplicate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ops.pub")
	_ = os.WriteFile(path, []byte(testKey+"\n"), 0o644)
	remote := "[]"
	f := &fakeRunner{fn: func(spec run.Spec) (run.Result, error) {
		if spec.Args[0] == "api" {
			return run.Result{Stdout: remote}, nil
		}
		return run.Result{}, nil
	}}
	m := Manager{Runner: f}
	added, err := m.AddManaged(context.Background(), path)
	if err != nil || !added {
		t.Fatalf("added=%v err=%v", added, err)
	}
	remote = `[{"id":1,"title":"existing","key":"` + testKey + `"}]`
	added, err = m.AddManaged(context.Background(), path)
	if err != nil || added {
		t.Fatalf("duplicate added=%v err=%v", added, err)
	}
}

func TestAuthFailureAndSSHVerification(t *testing.T) {
	f := &fakeRunner{fn: func(spec run.Spec) (run.Result, error) {
		if spec.Name == "ssh" {
			return run.Result{Stderr: "Hi! You've successfully authenticated, but GitHub does not provide shell access."}, errors.New("exit status 1")
		}
		return run.Result{}, errors.New("auth failed")
	}}
	m := Manager{Runner: f}
	if err := m.Login(context.Background()); err == nil {
		t.Fatal("expected auth failure")
	}
	if err := m.VerifySSH(context.Background()); err != nil {
		t.Fatal(err)
	}
}
