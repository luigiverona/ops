package app

import (
	"context"
	"fmt"

	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/run"
)

// interruption belongs to one command. Nested lifecycle helpers share its
// phase, but only the owner emits a conclusion.
type interruption struct {
	ctx       context.Context
	mutation  bool
	concluded bool
}

func (a Runtime) withInterruption(ctx context.Context, command string) (Runtime, func(*int)) {
	if a.interruption != nil {
		return a, func(*int) {}
	}
	a.interruption = &interruption{ctx: ctx}
	a.Runner = cancellationRunner{a.Runner}
	return a, func(code *int) {
		if ctx.Err() == nil || a.interruption.concluded {
			return
		}
		*code = Fatal
		message := "Interrupted. No workstation changes made."
		switch command {
		case "update":
			message = "Update interrupted."
			if a.interruption.mutation {
				message += " Check ops --version before retrying ops update."
			}
		case "doctor":
			message = "Inspection interrupted."
		default:
			if a.interruption.mutation {
				message = "Interrupted. Earlier changes may remain. Run ops doctor before retrying."
			}
		}
		fmt.Fprintln(a.Err, "\n"+message)
	}
}

// beginMutation is also used for in-process file changes, which do not pass
// through Runner. A phase that has started may have left changes on failure.
func (a Runtime) beginMutation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.interruption != nil {
		a.interruption.mutation = true
	}
	return nil
}

func (a Runtime) interrupted() bool {
	return a.interruption != nil && a.interruption.ctx.Err() != nil
}

// Guard injected runners as well as real commands. Existing cleanup operations
// explicitly use a separate context and retain their established semantics.
type cancellationRunner struct{ run.Runner }

func (r cancellationRunner) Run(ctx context.Context, spec run.Spec) (run.Result, error) {
	if err := ctx.Err(); err != nil {
		return run.Result{}, err
	}
	result, err := r.Runner.Run(ctx, spec)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, err
}

// Claim the final report before writing it. A signal arriving after this point
// belongs to a completed command and must not append a contradictory conclusion.
func (a Runtime) claimConclusion() bool {
	if a.interrupted() {
		return false
	}
	if a.interruption != nil {
		if a.interruption.concluded {
			return false
		}
		a.interruption.concluded = true
	}
	return true
}

func (r cancellationRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	if err := ctx.Err(); err != nil {
		return run.Result{}, err
	}
	result, err := archrepo.Query(ctx, r.Runner, args)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, err
}
func (r cancellationRunner) OfficialInstalled(ctx context.Context, target string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	result, err := archrepo.InstalledContent(ctx, r.Runner, target)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return result, err
}

func (r cancellationRunner) OfficialPrepare(ctx context.Context, targets []string) (*archtrust.Prepared, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := archrepo.Prepare(ctx, r.Runner, targets)
	if ctx.Err() != nil {
		if result != nil {
			result.Close()
		}
		return nil, ctx.Err()
	}
	return result, err
}

func (r cancellationRunner) OfficialInstalledVersion(ctx context.Context, target, version string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// Query the current identity for compatibility with explicit current-only
	// capabilities; production always has the exact-version capability.
	result, err := archrepo.Query(ctx, r.Runner, []string{"-Si", "--", target})
	if err != nil {
		return false, err
	}
	info, err := archrepo.ParseInfo(result.Stdout)
	if err != nil {
		return false, err
	}
	match, err := archrepo.InstalledVersionContent(ctx, r.Runner, target, version, info["Version"])
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return match, err
}
