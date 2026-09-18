package archrepo

import (
	"context"
	"fmt"
	"sync"

	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/run"
)

// TrustedRunner supplies explicit official-source operations alongside ordinary
// command execution. General system upgrades still use the ordinary runner.
type TrustedRunner struct {
	run.Runner
	mu     sync.Mutex
	source *archtrust.Source
}

func NewTrustedRunner(runner run.Runner) *TrustedRunner {
	return &TrustedRunner{Runner: runner, source: archtrust.NewSource()}
}

// WithRunner attaches the transaction terminal without discarding the source
// snapshot already used for inspection and planning.
func (r *TrustedRunner) WithRunner(runner run.Runner) *TrustedRunner {
	return &TrustedRunner{Runner: runner, source: r.officialSource()}
}

func (r *TrustedRunner) officialSource() *archtrust.Source {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.source
}

func (r *TrustedRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	configuration, err := r.Runner.Run(ctx, run.Spec{Name: "pacman-conf", FailureOutput: run.FailureStderr})
	if err != nil {
		return run.Result{}, &archtrust.SourceError{Err: err}
	}
	if _, _, err := OfficialConfig(configuration.Stdout); err != nil {
		return run.Result{}, &archtrust.SourceError{Err: err}
	}
	return r.officialSource().Query(ctx, r.Runner, args)
}

func (r *TrustedRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	return r.officialSource().CachedInstalled(ctx, r.Runner, target)
}

// Query cannot silently fall back to user sync databases. Tests must supply
// explicit official-source evidence, just as the production runner does.
func Query(ctx context.Context, runner run.Runner, args []string) (run.Result, error) {
	trusted, ok := runner.(interface {
		OfficialQuery(context.Context, []string) (run.Result, error)
	})
	if !ok {
		return run.Result{}, &archtrust.SourceError{Err: fmt.Errorf("independent official source is unavailable")}
	}
	return trusted.OfficialQuery(ctx, args)
}

func InstalledContent(ctx context.Context, runner run.Runner, target string) (bool, error) {
	trusted, ok := runner.(interface {
		OfficialInstalled(context.Context, string) (bool, error)
	})
	if !ok {
		return false, fmt.Errorf("authenticated official content evidence is unavailable")
	}
	return trusted.OfficialInstalled(ctx, target)
}

func (r *TrustedRunner) OfficialPrepare(ctx context.Context, targets []string) (*archtrust.Prepared, error) {
	return r.officialSource().Prepare(ctx, r.Runner, targets)
}
func Prepare(ctx context.Context, runner run.Runner, targets []string) (*archtrust.Prepared, error) {
	trusted, ok := runner.(interface {
		OfficialPrepare(context.Context, []string) (*archtrust.Prepared, error)
	})
	if !ok {
		return nil, fmt.Errorf("authenticated official transaction preparation unavailable")
	}
	return trusted.OfficialPrepare(ctx, targets)
}
func (r *TrustedRunner) Run(ctx context.Context, spec run.Spec) (run.Result, error) {
	result, err := r.Runner.Run(ctx, spec)
	if err == nil && spec.Name == "sudo" {
		for _, arg := range spec.Args {
			if arg == "-Syu" {
				r.mu.Lock()
				r.source = r.source.Next()
				r.mu.Unlock()
				break
			}
		}
	}
	return result, err
}

func (r *TrustedRunner) OfficialInstalledVersion(ctx context.Context, target, version string) (bool, error) {
	return r.officialSource().CachedInstalledVersion(ctx, r.Runner, target, version)
}

// Legacy explicit test capabilities may authenticate the current identity only.
// Missing exact-version capability never falls back to authenticating N+1.
func InstalledVersionContent(ctx context.Context, runner run.Runner, target, version, current string) (bool, error) {
	if trusted, ok := runner.(interface {
		OfficialInstalledVersion(context.Context, string, string) (bool, error)
	}); ok {
		return trusted.OfficialInstalledVersion(ctx, target, version)
	}
	if version != current {
		return false, archtrust.ErrExactVersionUnavailable
	}
	return InstalledContent(ctx, runner, target)
}
