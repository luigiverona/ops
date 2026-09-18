package archrepo

import (
	"context"
	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

// These command fakes explicitly supply synthetic official-source evidence.
// Native authentication is tested with isolated signatures and real payloads.
func (r queryFunc) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r queryFunc) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}

func (r queryFunc) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}
