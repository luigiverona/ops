package archrepo

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type unavailableRunner struct{}

func (unavailableRunner) Run(context.Context, run.Spec) (run.Result, error) {
	return run.Result{}, errors.New("unavailable")
}

func TestAttachingTerminalRetainsIndependentEvidence(t *testing.T) {
	original := NewTrustedRunner(unavailableRunner{})
	input := bytes.NewBufferString("transaction decision\n")
	attached := original.WithRunner(run.Exec{In: input})
	if attached.source != original.source || attached.Runner.(run.Exec).In != input {
		t.Fatal("terminal attachment lost source evidence or terminal input")
	}
}

func TestOfficialOperationsNeverFallBackToRawRunner(t *testing.T) {
	runner := unavailableRunner{}
	if _, err := Query(context.Background(), runner, []string{"-Si", "--", "git"}); err == nil {
		t.Fatal("raw query accepted")
	}
	if ready, err := InstalledContent(context.Background(), runner, "extra/git"); err == nil || ready {
		t.Fatal("raw installed metadata accepted")
	}
	if _, err := Prepare(context.Background(), runner, []string{"extra/git"}); err == nil {
		t.Fatal("raw transaction accepted")
	}
}
