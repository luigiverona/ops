package arch

import (
	"context"
	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

// These command fakes explicitly supply synthetic official-source evidence.
// Native authentication is tested with isolated signatures and real payloads.
func (r *artifactStageRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *artifactStageRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *configRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *configRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *failingSudoRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *failingSudoRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *managerRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *managerRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *provenanceRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *provenanceRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r protectedStatRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r protectedStatRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}

func (r *artifactStageRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *configRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *failingSudoRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *managerRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *provenanceRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r protectedStatRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}
