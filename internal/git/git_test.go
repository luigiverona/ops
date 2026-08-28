package git

import (
	"context"
	"errors"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type fakeRunner struct {
	name, email string
	writes      int
}

func (f *fakeRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	last := spec.Args[len(spec.Args)-1]
	if len(spec.Args) >= 4 && spec.Args[2] != "--get" {
		if spec.Args[2] == "user.name" {
			f.name = last
		} else if spec.Args[2] == "user.email" {
			f.email = last
		}
		f.writes++
		return run.Result{}, nil
	}
	if last == "user.name" {
		if f.name == "" {
			return run.Result{}, errors.New("unset")
		}
		return run.Result{Stdout: f.name + "\n"}, nil
	}
	if last == "user.email" {
		if f.email == "" {
			return run.Result{}, errors.New("unset")
		}
		return run.Result{Stdout: f.email + "\n"}, nil
	}
	return run.Result{}, errors.New("unexpected")
}
func TestSetOnlyMissingOrInvalidValues(t *testing.T) {
	f := &fakeRunner{name: "Existing", email: "invalid"}
	m := Manager{Runner: f}
	current := m.Inspect(context.Background())
	if err := m.SetMissing(context.Background(), current, "Replacement", "valid@example.com"); err != nil {
		t.Fatal(err)
	}
	if f.name != "Existing" || f.email != "valid@example.com" || f.writes != 1 {
		t.Fatalf("name=%q email=%q writes=%d", f.name, f.email, f.writes)
	}
}
func TestIdentityValidation(t *testing.T) {
	if !ValidName("User") || ValidName("\n") || !ValidEmail("a@b.example") || ValidEmail("not-an-email") {
		t.Fatal("validation mismatch")
	}
}
