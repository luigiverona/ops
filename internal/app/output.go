package app

import (
	"fmt"
	"sort"
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

func readyApplicationCount(p plan.Plan) int {
	ready := 0
	for _, application := range p.Applications {
		if application.State == "ready" {
			ready++
		}
	}
	return ready
}

func (a Runtime) showPlan(p plan.Plan) {
	fmt.Fprintln(a.Out, "Plan")
	sections := planSections(p)
	hasActions := false
	hasDiagnostics := false
	for _, section := range sections {
		hasActions = hasActions || !section.Diagnostic
		hasDiagnostics = hasDiagnostics || section.Diagnostic
	}
	if !hasActions && (p.SSHHostKeyFreshness == plan.SSHHostKeyFreshnessUnavailable || hasDiagnostics) {
		fmt.Fprintln(a.Out, "\nNo changes planned")
	} else if !hasActions {
		fmt.Fprintln(a.Out, "\nNo changes\n  workstation is already ready")
	}
	for _, section := range sections {
		fmt.Fprintf(a.Out, "\n%s\n%s", section.Name, ui.RenderTable(section.Rows))
	}

	readyCore, readyApps := 0, 0
	for _, component := range plan.CoreOrder {
		if p.Core[component] == "ready" {
			readyCore++
		}
	}
	for _, application := range p.Applications {
		if application.State == "ready" {
			readyApps++
		}
	}
	if readyCore > 0 || readyApps > 0 {
		fmt.Fprintln(a.Out, "\nUnchanged")
		if readyCore > 0 {
			fmt.Fprintf(a.Out, "  %d core components\n", readyCore)
		}
		if readyApps > 0 {
			fmt.Fprintf(a.Out, "  %d applications\n", readyApps)
		}
	}
}

type outputSection struct {
	Name       string
	Rows       []ui.TableRow
	Diagnostic bool
}

func planSections(p plan.Plan) []outputSection {
	var sections []outputSection
	var systemRows []ui.TableRow
	if p.EnableMultilib {
		systemRows = append(systemRows, ui.TableRow{Item: "multilib", Action: actionEnable, Detail: "pacman repository"})
	}
	if p.FullUpgrade {
		systemRows = append(systemRows, ui.TableRow{Item: "full system upgrade", Action: actionUpgrade, Detail: fullUpgradeDetail})
	}
	if len(systemRows) > 0 {
		sections = append(sections, outputSection{Name: "System", Rows: systemRows})
	}

	var coreRows []ui.TableRow
	packages := append([]string(nil), p.CorePackages...)
	sort.Strings(packages)
	for _, pkg := range packages {
		coreRows = append(coreRows, ui.TableRow{Item: pkg, Action: actionInstall, Detail: "pacman"})
	}
	if p.AddFlathub {
		coreRows = append(coreRows, ui.TableRow{Item: "flathub", Action: actionEnable, Detail: "Flatpak remote"})
	}
	if len(coreRows) > 0 {
		sections = append(sections, outputSection{Name: "Core", Rows: coreRows})
	}

	var applicationRows, diagnosticRows []ui.TableRow
	for _, application := range p.Applications {
		if application.State == "ready" {
			continue
		}
		if application.State == "configure" {
			applicationRows = append(applicationRows, ui.TableRow{Item: application.Declaration.Identifier, Action: actionConfigure, Detail: "install reason / required services"})
			for _, service := range application.Services {
				applicationRows = append(applicationRows, ui.TableRow{Item: application.Declaration.Identifier + " -> " + service, Action: actionEnable, Detail: "systemd"})
			}
			continue
		}
		if application.State != "install" {
			detail := string(application.Declaration.Source)
			if application.Cause != "" {
				detail += "; " + application.Cause
			}
			diagnosticRows = append(diagnosticRows, ui.TableRow{Item: application.Declaration.Identifier, Action: string(application.State), Detail: detail})
			continue
		}
		for _, pkg := range application.AURPackages {
			applicationRows = append(applicationRows, ui.TableRow{Item: application.Declaration.Identifier + " -> " + pkg.Name, Action: actionInstall, Detail: buildPackageDetail(pkg)})
		}
		for _, fingerprint := range application.AURSigningKeys {
			applicationRows = append(applicationRows, ui.TableRow{Item: application.Declaration.Identifier + " -> " + fingerprint, Action: actionConfigure, Detail: "AUR signing key"})
		}
		detail := string(application.Declaration.Source)
		if application.Declaration.Source == "aur" {
			detail += "; review required"
		}
		applicationRows = append(applicationRows, ui.TableRow{Item: application.Declaration.Identifier, Action: actionInstall, Detail: detail})
		services := append([]string(nil), application.Services...)
		sort.Strings(services)
		for _, service := range services {
			applicationRows = append(applicationRows, ui.TableRow{Item: application.Declaration.Identifier + " -> " + service, Action: actionEnable, Detail: "systemd"})
		}
	}
	if len(applicationRows) > 0 {
		sections = append(sections, outputSection{Name: "Applications", Rows: applicationRows})
	}
	if len(diagnosticRows) > 0 {
		sections = append(sections, outputSection{Name: "Application diagnostics", Rows: diagnosticRows, Diagnostic: true})
	}

	var accessRows []ui.TableRow
	if p.ConfigureGit {
		accessRows = append(accessRows, ui.TableRow{Item: "git", Action: actionConfigure, Detail: "user identity; input required"})
	}
	if p.ReviewSSHIdentities {
		accessRows = append(accessRows, ui.TableRow{Item: "SSH identities", Action: actionReview, Detail: "unrelated local keys"})
	}
	if p.CreateSSHIdentity {
		accessRows = append(accessRows, ui.TableRow{Item: "SSH identity", Action: actionConfigure, Detail: "managed Ed25519 key"})
	}
	if p.ReviewSSHAgent {
		accessRows = append(accessRows, ui.TableRow{Item: "ssh-agent identities", Action: actionReview, Detail: "unrelated loaded keys"})
	}
	if p.LoadSSHAgent {
		accessRows = append(accessRows, ui.TableRow{Item: "ssh-agent managed key", Action: actionConfigure, Detail: "load identity"})
	}
	if p.ConfigureSSH {
		accessRows = append(accessRows, ui.TableRow{Item: "github.com SSH configuration", Action: actionConfigure, Detail: "managed identity and host trust"})
	}
	if p.AuthenticateGitHub {
		accessRows = append(accessRows, ui.TableRow{Item: "github", Action: actionAuthenticate, Detail: "CLI login; SSH-key permission"})
	}
	if p.RefreshGitHubSSHKeyScope {
		accessRows = append(accessRows, ui.TableRow{Item: "github", Action: actionAuthenticate, Detail: "add SSH-key management permission"})
	}
	if p.ReviewGitHubKeys {
		detail := "account keys"
		action := actionReview
		if p.GitHubKeyStateUnknown {
			action = actionInspect
			if p.GitHubKeyAfterIdentity {
				detail = "reconcile after identity creation"
			} else if p.RefreshGitHubSSHKeyScope {
				detail = "reconcile after authorization refresh"
			} else {
				detail = "reconcile after login"
			}
		}
		accessRows = append(accessRows, ui.TableRow{Item: "GitHub SSH keys", Action: action, Detail: detail})
	}
	if p.ConfigureGitHubKey {
		detail := "managed key"
		if p.GitHubKeyAfterIdentity {
			detail = "register after identity creation, if missing"
		} else if p.RefreshGitHubSSHKeyScope {
			detail = "register after authorization refresh, if missing"
		} else if p.GitHubKeyStateUnknown {
			detail = "register after login, if missing"
		}
		accessRows = append(accessRows, ui.TableRow{Item: "GitHub SSH key", Action: actionConfigure, Detail: detail})
	}
	if len(accessRows) > 0 {
		sections = append(sections, outputSection{Name: "Identity and access", Rows: accessRows})
	}
	if p.SSHHostKeyFreshness == plan.SSHHostKeyFreshnessUnavailable {
		sections = append(sections, outputSection{
			Name: "Checks", Diagnostic: true,
			Rows: []ui.TableRow{{Item: "GitHub SSH host-key freshness", Action: "unavailable", Detail: "retry later"}},
		})
	}
	return sections
}

func buildPackageDetail(pkg plan.BuildPackage) string {
	detail := "pacman"
	if len(pkg.Provides) > 0 {
		detail += "; provides " + strings.Join(pkg.Provides, ", ")
	}
	if pkg.AsExplicit {
		detail += "; requested application"
	}
	if len(pkg.Purposes) > 0 {
		detail += "; " + strings.Join(pkg.Purposes, "/") + " dependency"
	}
	return detail
}

func (a Runtime) showProgress(item, action, detail string) {
	a.showProgressRows([]ui.TableRow{{Item: item, Action: action, Detail: detail}})
}

func (a Runtime) showProgressRows(rows []ui.TableRow) {
	if len(rows) == 0 {
		return
	}
	if a.presentation == nil {
		fmt.Fprintf(a.Out, "\nProgress\n%s", ui.RenderTable(rows))
		return
	}
	if !a.presentation.progressStarted {
		fmt.Fprint(a.Out, "\nProgress\n")
		a.presentation.progressStarted = true
	}
	a.presentation.reviewActive = false
	fmt.Fprint(a.Out, ui.RenderTable(rows))
}

func (a Runtime) showExternal(program, purpose string) {
	a.showProgress(program, "external", purpose)
}

func (a Runtime) showReview(name string, fields []ui.Field) {
	if a.presentation == nil || !a.presentation.reviewActive {
		fmt.Fprint(a.Out, "\nReview\n")
		if a.presentation != nil {
			a.presentation.reviewActive = true
			a.presentation.progressStarted = false
		}
	}
	fmt.Fprintf(a.Out, "\n%s\n%s", ui.PrintableASCII(name), ui.RenderFields(fields))
}

func (a Runtime) report(p plan.Plan, ready int, gitStatus, sshStatus, githubStatus string, problems []issue) {
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
	fmt.Fprintf(a.Out, "\nFinal\n%s\n", ui.RenderFields([]ui.Field{{Name: "system", Value: "ready"}, {Name: "core", Value: coreSummary(p)}, {Name: "apps", Value: fmt.Sprintf("%d/%d", ready, len(p.Applications))}, {Name: "git", Value: gitStatus}, {Name: "ssh", Value: sshStatus}, {Name: "github", Value: githubStatus}}))
	if len(problems) > 0 {
		fmt.Fprintln(a.Out, "Workstation completed with issues.")
		return
	}
	if sshStatus == "unavailable" {
		fmt.Fprintln(a.Out, "Workstation prepared; checks remain unavailable.")
		return
	}
	if gitStatus == "skipped" || sshStatus == "skipped" || githubStatus == "skipped" {
		fmt.Fprintln(a.Out, "Workstation prepared.")
		return
	}
	fmt.Fprintln(a.Out, "Workstation ready.")
}

func (a Runtime) reportSkipped(p plan.Plan) {
	fmt.Fprintf(a.Out, "\nFinal\n%s\n", ui.RenderFields([]ui.Field{{Name: "system", Value: "skipped"}, {Name: "core", Value: "skipped"}, {Name: "apps", Value: fmt.Sprintf("%d/%d", readyApplicationCount(p), len(p.Applications))}, {Name: "git", Value: p.GitStatus}, {Name: "ssh", Value: p.SSHStatus}, {Name: "github", Value: p.GitHubStatus}}))
	fmt.Fprintln(a.Out, "Workstation preparation skipped.")
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
	fields = append(fields, ui.Field{Name: "cause", Value: err.Error()}, ui.Field{Name: "impact", Value: impact}, ui.Field{Name: "action", Value: "resolve the error and run ops again"})
	fmt.Fprint(a.Err, ui.RenderFields(fields))
	fmt.Fprint(a.Err, "\nFinal\n")
	fmt.Fprint(a.Err, ui.RenderFields([]ui.Field{{Name: "system", Value: "stopped"}}))
	fmt.Fprintln(a.Err, "Workstation preparation stopped.")
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

// DefaultRuntime creates production process dependencies.

func coreSummary(p plan.Plan) string {
	ready, required := 0, 0
	for _, component := range plan.CoreOrder {
		if p.Core[component] == "not required" {
			continue
		}
		required++
		if p.Core[component] == "ready" {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, required)
}
