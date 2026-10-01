package ssh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type fingerprintFailure struct {
	err   error
	calls int
}

func (r *fingerprintFailure) Run(context.Context, run.Spec) (run.Result, error) {
	r.calls++
	return run.Result{}, r.err
}
func TestDiscoveryLifecycleFailures(t *testing.T) {
	for _, failure := range []error{&run.OwnershipError{Err: errors.New("cleanup")}, context.Canceled, context.DeadlineExceeded, errors.New("malformed key")} {
		t.Run(failure.Error(), func(t *testing.T) {
			home := t.TempDir()
			if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"a", "b"} {
				if err := os.WriteFile(filepath.Join(home, ".ssh", name), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ninvalid\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r := &fingerprintFailure{err: failure}
			m := Manager{Home: home, Runner: r}
			ids, err := m.Discover(context.Background())
			fatal := run.OwnershipFailed(failure) || errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded)
			if fatal {
				if !errors.Is(err, failure) || r.calls != 1 {
					t.Fatalf("lost interruption: %v calls=%d", err, r.calls)
				}
			} else if err != nil || len(ids) != 0 || r.calls != 2 {
				t.Fatalf("ordinary discovery changed: %v", err)
			}
			if fatal {
				if err := m.verifyFingerprint(context.Background(), filepath.Join(home, ".ssh", "a"), "wanted"); !errors.Is(err, failure) {
					t.Fatalf("revalidation lost cause: %v", err)
				}
			}
		})
	}
}
