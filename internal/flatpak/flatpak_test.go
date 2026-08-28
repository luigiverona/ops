package flatpak

import (
	"context"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type fakeRunner struct{ calls []run.Spec }

func (f *fakeRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, spec)
	return run.Result{}, nil
}

func TestAllOperationsAreUserScoped(t *testing.T) {
	f := &fakeRunner{}
	m := Manager{Runner: f}
	_ = m.AddFlathub(context.Background())
	_ = m.Install(context.Background(), "org.example.App")
	_ = m.Ready(context.Background(), "org.example.App")
	for _, call := range f.calls {
		if !strings.Contains(" "+strings.Join(call.Args, " ")+" ", " --user ") {
			t.Fatalf("not user scoped: %#v", call.Args)
		}
		if call.Name == "sudo" {
			t.Fatal("flatpak used sudo")
		}
	}
}
