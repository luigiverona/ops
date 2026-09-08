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

func (a Runtime) executePlan(ctx context.Context, p plan.Plan, terminal ui.UI) (result execution) {
	a.showPlan(p)
	plannedProblems := planIssues(p)
	if !p.HasActions() {
		return execution{git: p.GitStatus, ssh: p.SSHStatus, github: p.GitHubStatus, problems: plannedProblems}
	}
	confirmed, err := terminal.Confirm("Continue?", true)
	if err != nil {
		return execution{status: a.fatal(err)}
	}
	if !confirmed {
		fmt.Fprintln(a.Out, "No changes made.")
		return execution{status: Success, skipped: true}
	}
	privileged := needsPrivilege(p)
	var keeper *sudoops.Keeper
	if privileged {
		keeper, err = sudoops.Acquire(ctx, a.Runner)
		if err != nil {
			return execution{status: a.fatal(fmt.Errorf("sudo authorization failed; no workstation changes made: %w", err))}
		}
		defer keeper.Close()
	}
	defer func() {
		if result.status == Fatal {
			fmt.Fprintln(a.Err, "Earlier changes may remain. Run ops doctor before retrying.")
		}
	}()

	archManager := arch.Manager{Runner: a.Runner}
	if p.EnableMultilib {
		a.progress("Preparing system...")
		if err := archManager.EnableMultilib(ctx); err != nil {
			return execution{status: a.coreFatal("multilib", err, "required repository configuration is unavailable")}
		}
	}
	if p.FullUpgrade {
		a.progress("Updating system...")
		if err := archManager.FullUpgrade(ctx); err != nil {
			return execution{status: a.coreFatal("Arch system upgrade", err, "package installation cannot continue safely")}
		}
	}
	if len(p.CorePackages) > 0 {
		a.progress("Installing packages...")
	}
	if err := archManager.Install(ctx, p.CorePackages, false); err != nil {
		return execution{status: a.coreFatal("core packages", err, "required workstation capabilities are unavailable")}
	}

	if err := a.verifyCore(ctx, p); err != nil {
		return execution{status: a.coreFatal("core verification", err, "the required core is incomplete")}
	}

	aurManager := aur.Manager{Runner: a.Runner, Review: func(name string, files map[string]string) error {
		return a.reviewAUR(ctx, terminal, name, files)
	}}
	flatpakManager := flatpak.Manager{Runner: a.Runner}
	if p.AddFlathub {
		a.progress("Preparing Flatpak applications...")
		if err := flatpakManager.AddFlathub(ctx); err != nil {
			return execution{status: a.coreFatal("flathub", err, "Flatpak application support is unavailable")}
		}
	}

	problems := plannedProblems
	for _, application := range p.Applications {
		if application.State == "ready" {
			continue
		}
		if application.State.Problem() {
			continue
		}
		if application.State == "configure" {
			a.progress("Configuring " + application.Declaration.Identifier + "...")
			if err := a.configureApplication(ctx, archManager, application); err != nil {
				problems = append(problems, issue{State: "Failed", Name: application.Declaration.Identifier, Source: string(application.Declaration.Source), Cause: err.Error(), Impact: "application configuration is incomplete", Action: "run ops doctor, resolve the error, then run ops again"})
				continue
			}
			continue
		}
		if err := a.installApplication(ctx, archManager, aurManager, flatpakManager, application); err != nil {
			if ctx.Err() != nil {
				return execution{status: a.fatal(fmt.Errorf("application setup interrupted: %w", err))}
			}
			state := "Failed"
			impact := "application setup is incomplete; installation or configuration may have partially succeeded"
			if errors.Is(err, errReviewDeclined) {
				state = "Skipped"
				impact = "this build's dependencies and artifacts were not installed"
			}
			problems = append(problems, issue{State: state, Name: application.Declaration.Identifier, Source: string(application.Declaration.Source), Cause: err.Error(), Impact: impact, Action: "run ops doctor, resolve the source error or review decision, then run ops again"})
			continue
		}
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

	return execution{applied: true, git: gitStatus, ssh: sshStatus, github: githubStatus, problems: problems}
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

// reviewAUR hides only declarative metadata from presentation. The AUR manager
// still validates it and compares every tracked file before executing the build.
func (a Runtime) reviewAUR(ctx context.Context, terminal ui.UI, name string, files map[string]string) error {
	if _, ok := files["PKGBUILD"]; !ok {
		return errors.New("AUR source does not track PKGBUILD; cannot review build instructions")
	}
	a.progress("Reviewing " + name + "...")
	names := []string{"PKGBUILD"}
	for filename := range files {
		if filename != "PKGBUILD" && filename != ".SRCINFO" {
			names = append(names, filename)
		}
	}
	sort.Strings(names[1:])
	review := make([]ui.ReviewFile, 0, len(names))
	for _, filename := range names {
		review = append(review, ui.ReviewFile{Name: filename, Contents: files[filename]})
	}
	if err := terminal.Review(ctx, review); err != nil {
		if errors.Is(err, ui.ErrReviewCancelled) {
			return errReviewDeclined
		}
		return fmt.Errorf("AUR review could not be completed; build not approved: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ok, err := terminal.Confirm("Install "+ui.PrintableASCII(name)+"?", false)
	if err != nil {
		return err
	}
	if !ok {
		return errReviewDeclined
	}
	return nil
}
