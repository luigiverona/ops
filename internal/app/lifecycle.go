package app

import (
	"context"
	"fmt"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/ui"
)

// execution records operation results, not evidence of convergence.
type execution struct {
	status           int
	applied, skipped bool
	git, ssh, github string
	problems         []issue
}

// preparePlan always rediscovers actual state after applying an approved plan.
func (a Runtime) preparePlan(ctx context.Context, cfg config.Config, p plan.Plan, terminal ui.UI) int {
	result := a.executePlan(ctx, p, terminal)
	if result.applied {
		observed, err := a.inspectState(ctx, cfg)
		if err != nil {
			return a.fatal(fmt.Errorf("final re-inspection failed after changes; run ops doctor before retrying: %w", err))
		}
		remaining := plan.Build(cfg, observed, nil)
		result.git, result.ssh, result.github = remaining.GitStatus, remaining.SSHStatus, remaining.GitHubStatus
		if remaining.HasActions() || len(planIssues(remaining)) > 0 {
			result.problems = append(result.problems, issue{State: "Failed", Name: "final verification", Cause: "re-inspection found remaining work", Impact: "workstation has not converged", Action: "run ops doctor, resolve the reported issues, then run ops again"})
		}
	}
	return a.reportExecution(result)
}

func (a Runtime) reportExecution(result execution) int {
	if result.status != Success || result.skipped {
		return result.status
	}
	if !result.applied && len(result.problems) == 0 && result.git == "ready" && result.ssh == "ready" && result.github == "ready" {
		fmt.Fprintln(a.Out, "Workstation already ready.")
		return Success
	}
	a.report(result.git, result.ssh, result.github, result.problems)
	if len(result.problems) > 0 || result.git != "ready" || result.ssh != "ready" || result.github != "ready" {
		if result.applied {
			fmt.Fprintln(a.Out, "Earlier changes may remain. Run ops doctor before retrying.")
		}
		return Issues
	}
	return Success
}
