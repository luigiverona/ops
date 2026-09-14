package app

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
)

func planIssues(p plan.Plan) []issue {
	problems := make([]issue, 0)
	for _, application := range p.Applications {
		if !application.State.Problem() {
			continue
		}
		problems = append(problems, issue{State: titleState(string(application.State)), Name: application.Declaration.Identifier, Source: string(application.Declaration.Source), Cause: application.Cause, Err: application.Err, Impact: "the declared application remains unmet", Action: applicationAction(application)})
	}
	return problems
}

// showPlan presents intent, not the execution graph. The reviewed plan remains
// the only authority for package and configuration mutations.
func (a Runtime) showPlan(p plan.Plan) {
	if !p.HasActions() {
		if p.SSHHostKeyFreshness == plan.SSHHostKeyFreshnessUnavailable {
			fmt.Fprintln(a.Out, "GitHub SSH host-key freshness unavailable; retry later.")
		}
		return
	}
	fmt.Fprintln(a.Out, "Workstation setup")
	var install, configure, services []string
	seenServices := map[string]bool{}
	for _, application := range p.Applications {
		if application.State.Actionable() {
			for _, service := range application.Services {
				if !seenServices[service] {
					services = append(services, service)
					seenServices[service] = true
				}
			}
		}
		label := application.Declaration.Identifier + " (" + sourceLabel(application.Declaration.Source) + ")"
		switch application.State {
		case "install":
			if application.Cause != "" {
				label += "; " + application.Cause
			}
			install = append(install, label)
		case "configure":
			configure = append(configure, label)
		}
	}
	if len(install) > 0 {
		fmt.Fprintln(a.Out, "\nInstall")
		for _, name := range install {
			fmt.Fprintf(a.Out, "  %s\n", ui.PrintableASCII(name))
		}
	}
	if p.ConfigureGit {
		configure = append(configure, "Git identity")
	}
	if p.CreateSSHIdentity || p.ConfigureSSH || p.LoadSSHAgent || p.ReviewSSHIdentities || p.ReviewSSHAgent {
		configure = append(configure, "SSH for GitHub")
	}
	if p.AuthenticateGitHub {
		configure = append(configure, "GitHub authentication")
	}
	if p.RefreshGitHubSSHKeyScope {
		configure = append(configure, "GitHub SSH key access")
	}
	if p.ConfigureGitHubKey {
		if p.GitHubKeyStateUnknown {
			configure = append(configure, "Register this workstation's SSH key with GitHub if needed")
		} else {
			configure = append(configure, "Register this workstation's SSH key with GitHub")
		}
	} else if p.ReviewGitHubKeys {
		configure = append(configure, "GitHub SSH keys")
	}

	if p.EnableFlathub {
		configure = append(configure, "Enable existing user flathub remote at https://dl.flathub.org/repo/")
	}
	if p.AddFlathub {
		configure = append(configure, "Flathub for user Flatpak applications")
	}
	if len(configure) > 0 {
		fmt.Fprintln(a.Out, "\nConfigure")
		for _, item := range configure {
			fmt.Fprintf(a.Out, "  %s\n", ui.PrintableASCII(item))
		}
	}
	if len(services) > 0 {
		sort.Strings(services)
		fmt.Fprintln(a.Out, "\nEnable and start")
		for _, service := range services {
			fmt.Fprintf(a.Out, "  %s\n", ui.PrintableASCII(service))
		}
	}
	if p.ConfigureSSH {
		fmt.Fprintln(a.Out, "\nManage GitHub SSH settings separately; preserve other host configuration.")
	}
	if p.EnableMultilib {
		fmt.Fprintln(a.Out, "\nEnable multilib.")
	}
	if p.FullUpgrade {
		fmt.Fprintln(a.Out, "\nThe system will be updated.")
		fmt.Fprintln(a.Out, "  Upgrade using all configured repositories, including custom repositories.")
		fmt.Fprintln(a.Out, "  Install managed official targets from core, extra, or multilib; exclude custom repositories from those installations.")
	}
	dependencies := len(p.CorePackages) > 0
	for _, application := range p.Applications {
		if application.State == plan.Install {
			dependencies = dependencies || len(application.AURPackages) > 0
		}
	}
	if dependencies {
		fmt.Fprintln(a.Out, "\nRequired dependencies will be installed.")
	}
	for _, problem := range planIssues(p) {
		fmt.Fprintf(a.Out, "\nCannot install %s: %s\n", ui.PrintableASCII(problem.Name), ui.PrintableASCII(ui.DiagnosticExcerpt(problem.Cause, false)))
	}
	if p.SSHHostKeyFreshness == plan.SSHHostKeyFreshnessUnavailable {
		fmt.Fprintln(a.Out, "\nGitHub SSH host-key freshness unavailable; retry later.")
	}
	fmt.Fprintln(a.Out)
}

func (a Runtime) progress(message string) {
	fmt.Fprintln(a.Out, ui.PrintableASCII(message))
}

func (a Runtime) report(gitStatus, sshStatus, githubStatus string, problems []issue) {
	a.reportProblems(a.Out, problems)
	if len(problems) > 0 {
		fmt.Fprintln(a.Out, "Workstation setup incomplete.")
		return
	}
	if sshStatus == "unavailable" {
		fmt.Fprintln(a.Out, "Workstation prepared; checks remain unavailable.")
		return
	}
	if gitStatus != "ready" || sshStatus != "ready" || githubStatus != "ready" {
		fmt.Fprintln(a.Out, "Workstation setup incomplete. Run ops doctor before retrying.")
		return
	}
	fmt.Fprintln(a.Out, "Workstation ready.")
}

func (a Runtime) reportProblems(out io.Writer, problems []issue) {
	if len(problems) > 0 {
		fmt.Fprint(out, "\nIssues\n")
		states := []string{"Unresolved", "Unavailable", "Skipped", "Failed", "Unmet"}
		for _, state := range states {
			found := false
			for _, problem := range problems {
				if problem.State == state {
					found = true
					break
				}
			}
			if !found {
				continue
			}
			if state != "Skipped" {
				fmt.Fprintf(out, "\n%s\n", state)
			}
			for _, problem := range problems {
				if problem.State != state {
					continue
				}
				if state == "Skipped" {
					fmt.Fprintf(out, "Skipped %s.\n", ui.PrintableASCII(problem.Name))
					if problem.Observed != "" {
						fmt.Fprintf(out, "  After setup: %s\n", ui.PrintableASCII(problem.Observed))
						if problem.Action != "" {
							fmt.Fprintf(out, "  %s\n", ui.PrintableASCII(problem.Action))
						}
					}
					continue
				}
				fmt.Fprintf(out, "\n%s\n", ui.PrintableASCII(problem.Name))
				fields := make([]ui.Field, 0, 5)
				if problem.Source != "" {
					fields = append(fields, ui.Field{Name: "source", Value: problem.Source})
				}
				if problem.Stage != "" {
					fields = append(fields, ui.Field{Name: "stage", Value: problem.Stage})
				}
				fields = append(fields, ui.Field{Name: "cause", Value: ui.DiagnosticExcerpt(problem.Cause, false)})
				if problem.Impact != "" {
					fields = append(fields, ui.Field{Name: "impact", Value: problem.Impact})
				}
				fmt.Fprint(out, ui.RenderFields(fields))
				reportEvidence(out, problem.Err)
				fields = nil
				if problem.Observed != "" {
					fields = append(fields, ui.Field{Name: "after setup", Value: problem.Observed})
				}
				if problem.Action != "" {
					fields = append(fields, ui.Field{Name: "action", Value: problem.Action})
				}
				fmt.Fprint(out, ui.RenderFields(fields))
			}
		}
	}
}

func (a Runtime) fatal(err error) int {
	a.renderFatal("ops", "", err, "workstation preparation could not safely continue")
	return Fatal
}

func (a Runtime) renderFatal(name, stage string, err error, impact string) {
	if !a.claimConclusion() {
		return
	}
	fmt.Fprint(a.Err, "Issues\n\nFailed\n\n")
	fmt.Fprintln(a.Err, ui.PrintableASCII(name))
	fields := []ui.Field{}
	if stage != "" {
		fields = append(fields, ui.Field{Name: "stage", Value: stage})
	}
	action := "resolve the error and run ops again"
	if name == "ops update" {
		action = "resolve the error and run ops update again"
	}
	fields = append(fields, ui.Field{Name: "cause", Value: ui.DiagnosticExcerpt(err.Error(), false)}, ui.Field{Name: "impact", Value: impact}, ui.Field{Name: "action", Value: action})
	fmt.Fprint(a.Err, ui.RenderFields(fields))
	reportEvidence(a.Err, err)
	if name == "ops update" {
		fmt.Fprintln(a.Err, "Update stopped.")
	} else {
		fmt.Fprintln(a.Err, "Workstation preparation stopped.")
	}
}

func setupIssue(name string, err error) *issue {
	return &issue{State: "Failed", Name: name, Stage: "setup", Cause: err.Error(), Err: err, Component: setupComponent(name), Impact: "setup is incomplete", Action: "resolve the error and run ops again"}
}

func linePresent(output, want string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func titleState(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func sourceLabel(source config.Source) string {
	switch source {
	case config.AUR:
		return "AUR"
	case config.Flatpak:
		return "Flatpak"
	default:
		return string(source)
	}
}

func setupComponent(name string) string {
	switch name {
	case "ssh-agent", "SSH":
		return "SSH"
	case "GitHub authentication", "GitHub authorization", "GitHub SSH keys", "GitHub SSH key", "GitHub SSH verification":
		return "GitHub"
	default:
		return name
	}
}

func applicationAction(application plan.Application) string {
	if application.ConfirmedAbsent {
		return "Check ~/.config/ops/apps.toml."
	}
	if application.State == plan.Unavailable {
		return "Retry later."
	}
	if application.State.Actionable() {
		return "Run ops again."
	}
	return "Resolve the reported prerequisite or build error, then run ops again."
}

func reportEvidence(out io.Writer, err error) {
	seen := make(map[*run.Error]bool)
	var report func(error)
	report = func(err error) {
		if command, ok := err.(*run.Error); ok {
			if seen[command] || command.Presented {
				return
			}
			seen[command] = true
			excerpt := ui.DiagnosticExcerpt(command.Evidence, command.EvidenceTruncated)
			if excerpt != "" {
				fmt.Fprintln(out, "  Recent output:")
				for _, line := range strings.Split(excerpt, "\n") {
					fmt.Fprintf(out, "    %s\n", line)
				}
			}
			return
		}
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, cause := range joined.Unwrap() {
				report(cause)
			}
		} else {
			if cause := errors.Unwrap(err); cause != nil {
				report(cause)
			}
		}
	}
	report(err)
}
