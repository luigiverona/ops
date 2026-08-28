package aur

import (
	"context"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type fakeRunner struct{ spec run.Spec }

func (f *fakeRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.spec = spec
	return run.Result{}, nil
}
func TestInstallUsesParuAsNormalUserWithReview(t *testing.T) {
	f := &fakeRunner{}
	m := Manager{Runner: f}
	if err := m.Install(context.Background(), "example-bin"); err != nil {
		t.Fatal(err)
	}
	if f.spec.Name != "paru" || !f.spec.Interactive {
		t.Fatalf("unsafe AUR invocation: %#v", f.spec)
	}
	for _, arg := range f.spec.Args {
		if arg == "--skipreview" || arg == "--noconfirm" {
			t.Fatalf("review bypassed: %#v", f.spec.Args)
		}
	}
}
