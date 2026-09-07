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
	fmt.Fprintln(a.Out, "Doctor\n\nSystem\n  platform        ready\n  privilege       normal user")
	fmt.Fprintln(a.Out, "\nCore")
	for _, component := range plan.CoreOrder {
		fmt.Fprintf(a.Out, "  %-15s %s\n", ui.PrintableASCII(component), ui.PrintableASCII(p.Core[component]))
		actionable = actionable || (p.Core[component] != "ready" && p.Core[component] != "not required")
	}
	fmt.Fprintln(a.Out, "\nApplications")
	if len(p.Applications) == 0 {
		fmt.Fprintln(a.Out, "  declared        none")
	}
	for _, application := range p.Applications {
		fmt.Fprintf(a.Out, "  %-15s %s\n", ui.PrintableASCII(application.Declaration.Identifier), ui.PrintableASCII(string(application.State)))
		if application.Cause != "" {
			fmt.Fprintf(a.Out, "    %s\n", ui.PrintableASCII(application.Cause))
		}
		actionable = actionable || application.State != "ready"
	}
	fmt.Fprintf(a.Out, "\nConfiguration\n  git             %s\n  ssh             %s\n  github          %s\n", ui.PrintableASCII(p.GitStatus), ui.PrintableASCII(p.SSHStatus), ui.PrintableASCII(p.GitHubStatus))
	actionable = actionable || p.GitStatus != "ready" || p.SSHStatus != "ready" || p.GitHubStatus != "ready"
	if p.SSHHostKeyFreshness == plan.SSHHostKeyFreshnessUnavailable {
		fmt.Fprintln(a.Out, "\nChecks\n  GitHub SSH host-key freshness  unavailable  retry later")
	}
	if actionable {
		fmt.Fprintln(a.Out, "\nIssues detected. Run ops to prepare the workstation.")
		return Issues
	}
	fmt.Fprintln(a.Out, "\nNo actionable issues detected.")
	return Success
}
