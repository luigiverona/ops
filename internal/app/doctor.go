package app

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/luigiverona/ops/internal/config"
	githubops "github.com/luigiverona/ops/internal/github"
	"github.com/luigiverona/ops/internal/inspect"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/resolve"
	"github.com/luigiverona/ops/internal/ui"
)

// Doctor checks managed local configuration. Live account/key verification
// belongs to setup; an offline session is not an unhealthy workstation.
func (a Runtime) Doctor(ctx context.Context) (code int) {
	a, finish := a.withInterruption(ctx, "doctor")
	defer finish(&code)
	if ctx.Err() != nil {
		return Fatal
	}
	if err := a.detect(ctx); err != nil {
		return a.doctorFatal(fmt.Errorf("doctor could not inspect the system: %w", err))
	}
	cfg, configErr := config.Load(config.Path(a.Home))
	missingConfig := errors.Is(configErr, os.ErrNotExist)
	if configErr != nil && !missingConfig {
		return a.doctorFatal(fmt.Errorf("doctor could not inspect configuration: %w", configErr))
	}
	state, err := (inspect.Workstation{Applications: cfg.Applications, Runner: a.Runner, Home: a.Home, PacmanConf: a.PacmanConf, SkipAgent: true}).Local(ctx)
	if err != nil {
		return a.doctorFatal(fmt.Errorf("doctor could not inspect workstation: %w", err))
	}
	facts := resolve.ApplicationAvailability(ctx, cfg, state, resolve.Resolver{Runner: a.Runner, Client: a.SourceHTTP})
	if ctx.Err() != nil {
		return Fatal
	}
	p := plan.Build(cfg, state, facts)
	// Build also plans remote reconciliation for setup. Doctor reports only
	// the local configuration it inspected, without inventing remote facts.
	p.SSHHostKeyFreshness = plan.SSHHostKeyFreshnessUnknown
	if state.ManagedSSHIdentity && state.SSHConfigurationReady {
		p.SSHStatus = "ready"
	}
	if state.Installed["github-cli"] {
		configured, err := (githubops.Manager{Runner: a.Runner}).Configured(ctx)
		if err != nil {
			return a.doctorFatal(fmt.Errorf("could not inspect local GitHub configuration; run gh auth status to diagnose: %w", err))
		}
		if configured {
			p.GitHubStatus = "ready"
		}
	}
	return a.reportDoctor(p, configErr, missingConfig)
}

func (a Runtime) reportDoctor(p plan.Plan, configErr error, missingConfig bool) int {
	if !a.claimConclusion() {
		return Fatal
	}
	actionable := missingConfig
	prepare := false
	if missingConfig {
		fmt.Fprintf(a.Out, "%s. No files changed.\n", ui.PrintableASCII(configErr.Error()))
	}

	for _, component := range plan.CoreOrder {
		if p.Core[component] != "ready" && p.Core[component] != "not required" {
			fmt.Fprintf(a.Out, "  %s: %s\n", ui.PrintableASCII(component), ui.PrintableASCII(p.Core[component]))
			actionable = true
			prepare = true
		}
	}
	for _, application := range p.Applications {
		if application.State == "ready" {
			continue
		}
		fmt.Fprintf(a.Out, "  %s (%s): not ready\n", ui.PrintableASCII(application.Declaration.Identifier), sourceLabel(application.Declaration.Source))
		if application.Cause != "" {
			fmt.Fprint(a.Out, ui.RenderFields([]ui.Field{{Name: "cause", Value: ui.DiagnosticExcerpt(application.Cause, false)}}))
		}
		reportEvidence(a.Out, application.Err)
		if application.State.Actionable() {
			prepare = true
			if application.State == plan.Install && application.Cause == "" {
				fmt.Fprintln(a.Out, "    The declared application is not installed.")
			}
			if len(application.Services) > 0 {
				fmt.Fprintf(a.Out, "    Required service is not enabled and active: %s\n", ui.PrintableASCII(application.Services[0]))
			}
		} else {
			fmt.Fprintf(a.Out, "    %s\n", applicationAction(application))
		}
		actionable = true
	}
	for _, component := range []struct{ name, status string }{{"Git", p.GitStatus}, {"SSH", p.SSHStatus}, {"GitHub", p.GitHubStatus}} {
		if component.status != "ready" && component.status != "unavailable" {
			fmt.Fprintf(a.Out, "  %s: %s\n", component.name, ui.PrintableASCII(component.status))
			prepare = true
			actionable = true
		}
	}
	if p.SSHHostKeyFreshness == plan.SSHHostKeyFreshnessUnavailable {
		fmt.Fprintln(a.Out, "GitHub SSH host-key freshness unavailable; retry later.")
		actionable = true
	}
	if actionable {
		fmt.Fprintln(a.Out, "\nIssues detected.")
		if prepare {
			fmt.Fprintln(a.Out, "Run ops to prepare the workstation.")
		}
		return Issues
	}
	fmt.Fprintln(a.Out, "Workstation healthy.")
	return Success
}

func (a Runtime) doctorFatal(err error) int {
	if !a.claimConclusion() {
		return Fatal
	}
	fmt.Fprintln(a.Err, "Inspection could not be completed.")
	fmt.Fprint(a.Err, ui.RenderFields([]ui.Field{{Name: "cause", Value: ui.DiagnosticExcerpt(err.Error(), false)}}))
	reportEvidence(a.Err, err)
	fmt.Fprintln(a.Err, "Resolve the inspection error and run ops doctor again.")
	return Fatal
}
