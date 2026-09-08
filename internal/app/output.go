package app

import (
	"fmt"
	"strings"

	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/ui"
)

func planIssues(p plan.Plan) []issue {
	problems := make([]issue, 0)
	for _, application := range p.Applications {
		if !application.State.Problem() {
			continue
		}
		problems = append(problems, issue{State: titleState(string(application.State)), Name: application.Declaration.Identifier, Source: string(application.Declaration.Source), Cause: application.Cause, Impact: "application was not installed", Action: "check the declared identifier and source, then run ops again"})
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
	var install, configure []string
	for _, application := range p.Applications {
		switch application.State {
		case "install":
			install = append(install, application.Declaration.Identifier)
		case "configure":
			configure = append(configure, application.Declaration.Identifier)
		}
	}
	if len(install) > 0 {
		fmt.Fprintln(a.Out, "\nInstall")
		for _, name := range install {
			fmt.Fprintf(a.Out, "  %s\n", ui.PrintableASCII(name))
		}
	}
	if p.ConfigureGit {
		configure = append(configure, "Git")
	}
	if p.CreateSSHIdentity || p.ConfigureSSH || p.LoadSSHAgent || p.ReviewSSHIdentities || p.ReviewSSHAgent {
		configure = append(configure, "SSH")
	}
	if p.AuthenticateGitHub || p.RefreshGitHubSSHKeyScope || p.ConfigureGitHubKey || p.ReviewGitHubKeys {
		configure = append(configure, "GitHub")
	}
	if p.AddFlathub {
		configure = append(configure, "Flatpak applications")
	}
	if len(configure) > 0 {
		fmt.Fprintf(a.Out, "\nConfigure\n  %s\n", ui.PrintableASCII(strings.Join(configure, ", ")))
	}
	if p.EnableMultilib {
		fmt.Fprintln(a.Out, "\nRequired system repositories will be enabled.")
	}
	if p.FullUpgrade {
		fmt.Fprintln(a.Out, "\nThe system will be updated.")
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
		fmt.Fprintf(a.Out, "\nCannot install %s: %s\n", ui.PrintableASCII(problem.Name), ui.PrintableASCII(problem.Cause))
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
	if len(problems) > 0 {
		fmt.Fprint(a.Out, "\nIssues\n")
		states := []string{"Unresolved", "Unavailable", "Skipped", "Failed"}
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
			fmt.Fprintf(a.Out, "\n%s\n", state)
			for _, problem := range problems {
				if problem.State != state {
					continue
				}
				fmt.Fprintf(a.Out, "\n%s\n", ui.PrintableASCII(problem.Name))
				fields := make([]ui.Field, 0, 5)
				if problem.Source != "" {
					fields = append(fields, ui.Field{Name: "source", Value: problem.Source})
				}
				if problem.Stage != "" {
					fields = append(fields, ui.Field{Name: "stage", Value: problem.Stage})
				}
				fields = append(fields, ui.Field{Name: "cause", Value: problem.Cause}, ui.Field{Name: "impact", Value: problem.Impact}, ui.Field{Name: "action", Value: problem.Action})
				fmt.Fprint(a.Out, ui.RenderFields(fields))
			}
		}
	}
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

func (a Runtime) fatal(err error) int {
	a.renderFatal("ops", "", err, "workstation preparation could not safely continue")
	return Fatal
}

func (a Runtime) coreFatal(name string, err error, impact string) int {
	a.renderFatal(name, "core", err, impact)
	return Fatal
}

func (a Runtime) renderFatal(name, stage string, err error, impact string) {
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
	fields = append(fields, ui.Field{Name: "cause", Value: err.Error()}, ui.Field{Name: "impact", Value: impact}, ui.Field{Name: "action", Value: action})
	fmt.Fprint(a.Err, ui.RenderFields(fields))
	if name == "ops update" {
		fmt.Fprintln(a.Err, "Update stopped.")
	} else {
		fmt.Fprintln(a.Err, "Workstation preparation stopped.")
	}
}

func setupIssue(name string, err error) *issue {
	return &issue{State: "Failed", Name: name, Stage: "setup", Cause: err.Error(), Impact: "setup is incomplete", Action: "resolve the error and run ops again"}
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
