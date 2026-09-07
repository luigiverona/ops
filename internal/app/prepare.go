package app

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/luigiverona/ops/internal/arch"
	"github.com/luigiverona/ops/internal/aur"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/flatpak"
	githubops "github.com/luigiverona/ops/internal/github"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/resolve"
	"github.com/luigiverona/ops/internal/run"
	sshops "github.com/luigiverona/ops/internal/ssh"
	sudoops "github.com/luigiverona/ops/internal/sudo"
	"github.com/luigiverona/ops/internal/ui"
)

// Prepare executes the interactive reconciliation lifecycle.
func (a Runtime) Prepare(ctx context.Context) int {
	if err := a.detect(ctx); err != nil {
		return a.fatal(err)
	}
	path := config.Path(a.Home)
	cfg, err := config.Load(path)
	if err != nil {
		return a.fatal(err)
	}
	state, err := a.inspectState(ctx, cfg)
	if err != nil {
		return a.fatal(fmt.Errorf("inspect workstation: %w", err))
	}
	facts := resolve.Applications(ctx, cfg, state, resolve.Resolver{Runner: a.Runner})
	p := plan.Build(cfg, state, facts)
	if !p.HasActions() {
		return a.preparePlan(ctx, cfg, p, ui.UI{})
	}

	tty, err := ui.OpenTTY()
	if err != nil {
		a.showPlan(p)
		return a.fatal(err)
	}
	defer tty.Close()
	if _, ok := a.Runner.(run.Exec); ok {
		a.Runner = run.Exec{In: tty, Out: a.Out, Err: a.Err}
	}
	terminal := ui.UI{In: tty, Out: tty}
	return a.preparePlan(ctx, cfg, p, terminal)
}

func (a Runtime) executePlan(ctx context.Context, p plan.Plan, terminal ui.UI) execution {
	a.presentation = &presentation{}
	a.showPlan(p)
	plannedProblems := planIssues(p)
	if !p.HasActions() {
		return execution{plan: p, ready: readyApplicationCount(p), git: p.GitStatus, ssh: p.SSHStatus, github: p.GitHubStatus, problems: plannedProblems}
	}
	confirmed, err := terminal.Confirm("Prepare this workstation?", true)
	if err != nil {
		return execution{status: a.fatal(err)}
	}
	if !confirmed {
		a.reportSkipped(p)
		return execution{status: Success, skipped: true}
	}

	privileged := needsPrivilege(p)
	var keeper *sudoops.Keeper
	if privileged {
		a.showProgress("sudo", actionConfigure, "privileged operations")
		a.showExternal("sudo", "password prompt")
		keeper, err = sudoops.Acquire(ctx, a.Runner)
		if err != nil {
			return execution{status: a.fatal(fmt.Errorf("sudo authorization failed: %w", err))}
		}
		defer keeper.Close()
	}

	archManager := arch.Manager{Runner: a.Runner}
	if p.EnableMultilib {
		a.showProgress("multilib", actionEnable, "pacman repository")
		if err := archManager.EnableMultilib(ctx); err != nil {
			return execution{status: a.coreFatal("multilib", err, "required repository configuration is unavailable")}
		}
	}
	if p.FullUpgrade {
		a.showProgress("full system upgrade", actionUpgrade, fullUpgradeDetail)
		a.showExternal("pacman -Syu", "pacman transaction decisions")
		if err := archManager.FullUpgrade(ctx); err != nil {
			return execution{status: a.coreFatal("Arch system upgrade", err, "package installation cannot continue safely")}
		}
	}
	if len(p.CorePackages) > 0 {
		rows := make([]ui.TableRow, 0, len(p.CorePackages))
		for _, pkg := range p.CorePackages {
			rows = append(rows, ui.TableRow{Item: pkg, Action: actionInstall, Detail: "pacman"})
		}
		a.showProgressRows(rows)
	}
	if err := archManager.Install(ctx, p.CorePackages, false); err != nil {
		return execution{status: a.coreFatal("core packages", err, "required workstation capabilities are unavailable")}
	}

	if err := a.verifyCore(ctx, p); err != nil {
		return execution{status: a.coreFatal("core verification", err, "the required core is incomplete")}
	}

	aurManager := aur.Manager{Runner: a.Runner, Review: func(name string, files map[string]string) error {
		a.showReview("AUR build files for "+name, []ui.Field{{Name: "notice", Value: "untrusted community instructions"}})
		names := make([]string, 0, len(files))
		for filename := range files {
			names = append(names, filename)
		}
		sort.Strings(names)
		for _, filename := range names {
			fmt.Fprintf(a.Out, "\n%s", ui.RenderReviewFile(filename, files[filename]))
		}
		ok, err := terminal.Confirm("Build and install this reviewed AUR package?", false)
		if err != nil {
			return err
		}
		if !ok {
			return errReviewDeclined
		}
		return nil
	}}
	flatpakManager := flatpak.Manager{Runner: a.Runner}
	if p.AddFlathub {
		a.showProgress("flathub", actionEnable, "Flatpak remote")
		if err := flatpakManager.AddFlathub(ctx); err != nil {
			return execution{status: a.coreFatal("flathub", err, "Flatpak application support is unavailable")}
		}
	}

	problems := plannedProblems
	readyApps := 0
	for _, application := range p.Applications {
		if application.State == "ready" {
			readyApps++
			continue
		}
		if application.State.Problem() {
			continue
		}
		if application.State == "configure" {
			if err := a.configureApplication(ctx, archManager, application); err != nil {
				problems = append(problems, issue{State: "Failed", Name: application.Declaration.Identifier, Source: string(application.Declaration.Source), Cause: err.Error(), Impact: "application install reason was not configured", Action: "resolve the package error and run ops again"})
				continue
			}
			readyApps++
			continue
		}
		if err := a.installApplication(ctx, archManager, aurManager, flatpakManager, application); err != nil {
			state := "Failed"
			if errors.Is(err, errReviewDeclined) {
				state = "Skipped"
			}
			problems = append(problems, issue{State: state, Name: application.Declaration.Identifier, Source: string(application.Declaration.Source), Cause: err.Error(), Impact: "application was not installed or configured", Action: "resolve the source error or review decision and run ops again"})
			continue
		}
		readyApps++
	}

	gitStatus := p.GitStatus
	if p.ConfigureGit {
		var gitIssue *issue
		gitStatus, gitIssue = a.configureGit(ctx, terminal)
		if gitIssue != nil {
			problems = append(problems, *gitIssue)
		}
	}

	sshStatus := p.SSHStatus
	var managed *sshops.Identity
	sshWork := p.CreateSSHIdentity || p.ReviewSSHIdentities || p.ReviewSSHAgent || p.LoadSSHAgent || p.ConfigureSSH
	githubWork := p.AuthenticateGitHub || p.RefreshGitHubSSHKeyScope || p.ReviewGitHubKeys || p.ConfigureGitHubKey
	if sshWork {
		var sshIssues []issue
		var fatalErr error
		sshStatus, managed, sshIssues, fatalErr = a.configureSSH(ctx, terminal, p)
		problems = append(problems, sshIssues...)
		if fatalErr != nil {
			return execution{status: a.fatal(fatalErr)}
		}
	} else if githubWork {
		var err error
		managed, err = a.managedSSHIdentity(ctx)
		if err != nil {
			return execution{status: a.fatal(fmt.Errorf("SSH state changed after planning: %w", err))}
		}
	}

	githubStatus := p.GitHubStatus
	if githubWork && sshStatus != "failed" {
		var githubIssues []issue
		githubStatus, githubIssues = a.configureGitHub(ctx, terminal, managed, p)
		problems = append(problems, githubIssues...)
	} else if githubWork {
		githubStatus = "skipped"
	} else if sshWork && sshStatus == "ready" && managed != nil {
		if err := (githubops.Manager{Runner: a.Runner}).VerifySSH(ctx); err != nil {
			githubStatus = "failed"
			problems = append(problems, *setupIssue("GitHub SSH verification", err))
		}
	}

	return execution{plan: p, applied: true, ready: readyApps, git: gitStatus, ssh: sshStatus, github: githubStatus, problems: problems}
}

func needsPrivilege(p plan.Plan) bool {
	if p.EnableMultilib || p.FullUpgrade || len(p.CorePackages) > 0 {
		return true
	}
	for _, app := range p.Applications {
		if (app.State == "install" || app.State == "configure") && (app.Declaration.Source == "pacman" || app.Declaration.Source == "aur" || len(app.Services) > 0 || len(app.AURPackages) > 0) {
			return true
		}
	}
	return false
}

func (a Runtime) verifyCore(ctx context.Context, p plan.Plan) error {
	packages := []string{"git", "openssh", "github-cli"}
	if p.Core["flatpak"] != "" && p.Core["flatpak"] != "not required" {
		packages = append(packages, "flatpak")
	}
	for _, pkg := range packages {
		if _, err := a.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Q", pkg}}); err != nil {
			return fmt.Errorf("verify prerequisite %s: %w", pkg, err)
		}
	}
	return nil
}

var errReviewDeclined = errors.New("AUR build intentionally skipped by user; build dependencies and artifacts were not installed")
