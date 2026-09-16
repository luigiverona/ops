package inspect

import (
	"context"
	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

// These command fakes explicitly supply synthetic official-source evidence.
// Native authentication is tested with isolated signatures and real payloads.
func (r *agentStateRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *agentStateRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *customNativeRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *customNativeRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *gitStateRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *gitStateRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *stateRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *stateRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}

func (r *agentStateRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *customNativeRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *gitStateRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *stateRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}
