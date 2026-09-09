package app

import (
	"context"
	"fmt"

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
