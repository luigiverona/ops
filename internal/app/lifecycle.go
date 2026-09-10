package app

import (
	"context"
	"fmt"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/ui"
)

// execution records operations separately from authoritative final observations.
type execution struct {
	status                           int
	applied, skipped                 bool
	git, ssh, github                 string
	problems                         []issue
	inspected, unmet, stopInspection bool
	inspectionErr                    error
}

// preparePlan rediscovers actual state after mutation, including stopped core
// work. Cancellation and lost interactive input never start further commands.
func (a Runtime) preparePlan(ctx context.Context, cfg config.Config, p plan.Plan, terminal ui.UI) (code int) {
	a, finish := a.withInterruption(ctx, "setup")
	defer finish(&code)
	result := a.executePlan(ctx, p, terminal)
	if ctx.Err() != nil {
		return Fatal
	}
	if result.applied && !result.stopInspection {
		observed, err := a.inspectState(ctx, cfg)
		if ctx.Err() != nil {
			return Fatal
		}
		if err != nil {
			result.inspectionErr = err
		} else {
			result.observe(plan.Build(cfg, observed, nil))
		}
	}
	return a.reportExecution(result)
}

// observe attaches the final state to the matching issue rather than inventing
// a second command failure. Source resolution is deliberately not repeated:
// the final inspection proves local state, not source availability.
func (r *execution) observe(p plan.Plan) {
	r.inspected = true
	r.git, r.ssh, r.github = p.GitStatus, p.SSHStatus, p.GitHubStatus
	record := func(name, source, state string, unmet bool) bool {
		r.unmet = r.unmet || unmet
		for i := range r.problems {
			problem := &r.problems[i]
			if source != "" && problem.Source == source && problem.Name == name || source == "" && problem.Source == "" && (problem.Component == name || problem.Name == name) {
				problem.Observed = state
				if !unmet {
					problem.Impact = ""
				}
				return true
			}
		}
		if unmet {
			r.problems = append(r.problems, issue{State: "Unmet", Name: name, Source: source, Cause: state, Action: "Run ops again."})
			return true
		}
		return false
	}
	for _, application := range p.Applications {
		state := "the declared application is ready"
		switch application.State {
		case plan.Ready:
		case plan.Configure:
			state = "application configuration remains incomplete"
			if len(application.Services) > 0 {
				state = "required service is not enabled and active: " + application.Services[0]
			}
		default:
			state = "the declared application is not installed from its selected source"
		}
		record(application.Declaration.Identifier, string(application.Declaration.Source), state, application.State != plan.Ready)
	}
	for _, component := range plan.CoreOrder {
		state := p.Core[component]
		if state != "" && state != "not required" {
			record(component, "", "required component is "+state, state != "ready")
		}
	}
	for _, component := range []struct{ name, core, status string }{{"Git", "git", p.GitStatus}, {"SSH", "ssh", p.SSHStatus}, {"GitHub", "github", p.GitHubStatus}} {
		if core := p.Core[component.core]; core == "missing" {
			continue
		}
		state := component.status
		if component.name == "SSH" && p.SSHHostKeyFreshness == plan.SSHHostKeyFreshnessUnavailable {
			state = "GitHub SSH host-key freshness unavailable; retry later"
		}
		if component.status == "unavailable" {
			if !record(component.name, "", state, false) {
				r.problems = append(r.problems, issue{State: "Unavailable", Name: component.name, Cause: state, Action: "Retry later."})
			}
			continue
		}
		record(component.name, "", state, component.status != "ready")
	}
}

func (a Runtime) reportExecution(result execution) int {
	// Pre-approval errors have already been presented by their owning boundary.
	if result.skipped || result.status != Success && len(result.problems) == 0 {
		return result.status
	}
	if !a.claimConclusion() {
		return Fatal
	}
	if result.inspectionErr != nil {
		a.reportProblems(a.Out, result.problems)
		fmt.Fprintln(a.Out, "Unable to verify final workstation state.")
		fmt.Fprint(a.Out, ui.RenderFields([]ui.Field{{Name: "cause", Value: ui.DiagnosticExcerpt(result.inspectionErr.Error(), false)}}))
		reportEvidence(a.Out, result.inspectionErr)
		fmt.Fprintln(a.Out, "Earlier changes may remain. Run ops doctor before retrying.")
		return Fatal
	}
	if result.inspected {
		a.reportProblems(a.Out, result.problems)
		if result.unmet {
			fmt.Fprintln(a.Out, "Workstation setup incomplete.")
		} else if result.ssh == "unavailable" {
			fmt.Fprintln(a.Out, "Workstation prepared; checks remain unavailable.")
		} else if len(result.problems) > 0 {
			fmt.Fprintln(a.Out, "Workstation configuration is ready; operations reported issues.")
		} else {
			fmt.Fprintln(a.Out, "Workstation ready.")
			return Success
		}
		if result.status == Fatal {
			return Fatal
		}
		return Issues
	}
	if result.status == Fatal {
		a.reportProblems(a.Err, result.problems)
		fmt.Fprintln(a.Err, "Workstation preparation stopped.")
		if result.applied {
			fmt.Fprintln(a.Err, "Earlier changes may remain. Run ops doctor before retrying.")
		}
		return Fatal
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
