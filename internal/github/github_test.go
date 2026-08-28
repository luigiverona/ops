package github

import (
	"context"
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

const testKey = "ssh-ed25519 AQID test"
const otherKey = "ssh-ed25519 BAUG other"

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
	if !strings.Contains(args, "--git-protocol ssh") || !strings.Contains(args, "--skip-ssh-key") {
		t.Fatalf("unsafe login args: %s", args)
	}
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
