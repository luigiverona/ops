package resolve

import (
	"context"
	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

// These command fakes explicitly supply synthetic official-source evidence.
// Native authentication is tested with isolated signatures and real payloads.
func (r missingRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}

func (r *countingRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *countingRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *dependencyErrorRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *dependencyErrorRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *dependencyRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *dependencyRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *minimalResolverRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *minimalResolverRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *mismatchedInstalledProvider) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *mismatchedInstalledProvider) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *transactionRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *transactionRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *versionRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *versionRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r closureMetadataRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r closureMetadataRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r diagnosticPacmanRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r diagnosticPacmanRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r isolatedPacmanRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r isolatedPacmanRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}

func (r *countingRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *dependencyErrorRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *dependencyRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *minimalResolverRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *mismatchedInstalledProvider) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *transactionRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *versionRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r closureMetadataRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r diagnosticPacmanRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r isolatedPacmanRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}
