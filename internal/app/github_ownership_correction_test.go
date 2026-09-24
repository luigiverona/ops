package app

import (
	"bytes"
	"context"
	"errors"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
	"strings"
	"testing"
)

func TestGitHubExpectedStateOwnershipStopsLifecycle(t *testing.T) {
	home, fingerprint, p := unauthenticatedGitHubFixture(t)
	base := &prepareRunner{home: home, sshFingerprint: fingerprint}
	cause := &run.OwnershipError{Err: errors.New("GitHub query cleanup failed")}
	failed, later := false, 0
	runner := diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
		if failed {
			later++
		}
		if s.Name == "gh" && len(s.Args) > 1 && s.Args[1] == "status" {
			failed = true
			return run.Result{}, errors.Join(fixtureExit(1), cause)
		}
		return base.Run(ctx, s)
	})
	var output bytes.Buffer
	a := Runtime{Ownership: testOwnership{}, Runner: runner, Home: home, Out: &output, Err: &output}
	code := a.executeForTest(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &output})
	if code != Fatal || !failed || later != 0 {
		t.Fatalf("code=%d reached=%v later=%d output=%s", code, failed, later, &output)
	}
	if strings.Contains(output.String(), "Signing in to GitHub") {
		t.Fatal("mutation after failed query")
	}
}
