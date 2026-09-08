package app

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/resolve"
	"github.com/luigiverona/ops/internal/ui"
)

// Doctor performs the same detection and planning inspections without mutation or sudo.
func (a Runtime) Doctor(ctx context.Context) int {
	if err := a.detect(ctx); err != nil {
		return a.fatal(fmt.Errorf("doctor could not inspect the system: %w", err))
	}
	cfg, err := config.Load(config.Path(a.Home))
	missingConfig := errors.Is(err, os.ErrNotExist)
	if err != nil && !missingConfig {
		return a.fatal(fmt.Errorf("doctor could not inspect configuration: %w", err))
	}
	state, err := a.inspectState(ctx, cfg)
	if err != nil {
		return a.fatal(fmt.Errorf("doctor could not inspect workstation: %w", err))
	}
	facts := resolve.Applications(ctx, cfg, state, resolve.Resolver{Runner: a.Runner})
	p := plan.Build(cfg, state, facts)
	actionable := missingConfig
	if missingConfig {
		fmt.Fprintln(a.Out, "Configuration missing. Create ~/.config/ops/apps.toml with version = 2; see docs/configuration.md. No files changed.")
	}

	for _, component := range plan.CoreOrder {
		if p.Core[component] != "ready" && p.Core[component] != "not required" {
			fmt.Fprintf(a.Out, "  %s: %s\n", ui.PrintableASCII(component), ui.PrintableASCII(p.Core[component]))
			actionable = true
		}
	}
	for _, application := range p.Applications {
		if application.State == "ready" {
			continue
		}
		fmt.Fprintf(a.Out, "  %s: not ready\n", ui.PrintableASCII(application.Declaration.Identifier))
		if application.Cause != "" {
			fmt.Fprintf(a.Out, "    %s\n", ui.PrintableASCII(application.Cause))
		}
		actionable = true
	}
	for _, component := range []struct{ name, status string }{{"Git", p.GitStatus}, {"SSH", p.SSHStatus}, {"GitHub", p.GitHubStatus}} {
		if component.status != "ready" {
			fmt.Fprintf(a.Out, "  %s: %s\n", component.name, ui.PrintableASCII(component.status))
			actionable = true
		}
	}
	if p.SSHHostKeyFreshness == plan.SSHHostKeyFreshnessUnavailable {
		fmt.Fprintln(a.Out, "GitHub SSH host-key freshness unavailable; retry later.")
		actionable = true
	}
	if actionable {
		fmt.Fprintln(a.Out, "\nIssues detected. Run ops to prepare the workstation.")
		return Issues
	}
	fmt.Fprintln(a.Out, "Workstation healthy.")
	return Success
}
