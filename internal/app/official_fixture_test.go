package app

import (
	"context"
	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

// These command fakes explicitly supply synthetic official-source evidence.
// Native authentication is tested with isolated signatures and real payloads.
func (r *agentPreservationRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *agentPreservationRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *applicationAURRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *applicationAURRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *aurOrderRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *aurOrderRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *cancelAfterMutation) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *cancelAfterMutation) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *doctorRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *doctorRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *githubFake) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *githubFake) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *githubRegistrationRaceRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *githubRegistrationRaceRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *lifecycleRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *lifecycleRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *prepareRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *prepareRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r *updateRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r *updateRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r cancelAfterGitName) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r cancelAfterGitName) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r diagnosticRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r diagnosticRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}
func (r skippedAURRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
}
func (r skippedAURRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return testpkg.FakeContent(ctx, r, target)
}

func (r *agentPreservationRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *applicationAURRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *aurOrderRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *cancelAfterMutation) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *doctorRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *githubFake) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *githubRegistrationRaceRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *lifecycleRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *prepareRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r *updateRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r cancelAfterGitName) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r diagnosticRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}

func (r skippedAURRunner) OfficialPrepare(_ context.Context, targets []string) (*archtrust.Prepared, error) {
	return testpkg.FakePrepared(targets)
}
