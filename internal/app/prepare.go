package app

import (
	"context"
	"errors"
	"fmt"
	"io"
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
func (a Runtime) Prepare(ctx context.Context) (code int) {
	a, finish := a.withInterruption(ctx, "setup")
	defer finish(&code)
	if ctx.Err() != nil {
		return Fatal
	}
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
	facts := resolve.Applications(ctx, cfg, state, resolve.Resolver{Runner: a.Runner, Client: a.SourceHTTP})
	if ctx.Err() != nil {
		return Fatal
	}
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
	if guarded, ok := a.Runner.(cancellationRunner); ok {
		if _, ok := guarded.Runner.(run.Exec); ok {
			a.Runner = cancellationRunner{run.Exec{In: tty, Out: a.Out, Err: a.Err}}
		}
	}
	terminal := ui.UI{In: tty, Out: tty}
	return a.preparePlan(ctx, cfg, p, terminal)
}

func (a Runtime) executePlan(ctx context.Context, p plan.Plan, terminal ui.UI) (result execution) {
	a, finish := a.withInterruption(ctx, "setup")
	defer finish(&result.status)
	if ctx.Err() != nil {
		return execution{status: Fatal}
	}
	a.showPlan(p)
	plannedProblems := planIssues(p)
	if !p.HasActions() {
		return execution{git: p.GitStatus, ssh: p.SSHStatus, github: p.GitHubStatus, problems: plannedProblems}
	}
	confirmed, err := terminal.Confirm(ctx, "Continue?", true)
	if err != nil {
		return execution{status: a.fatal(err)}
	}
	if !confirmed {
		if !a.claimConclusion() {
			return execution{status: Fatal}
		}
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
	problems := plannedProblems
	stop := func(name, stage string, err error, impact string) execution {
		return execution{status: Fatal, stopInspection: errors.Is(err, io.EOF), problems: append(problems, issue{State: "Failed", Name: name, Stage: stage, Cause: err.Error(), Err: err, Impact: impact, Action: "Run ops doctor before retrying."})}
	}
	defer func() {
		if a.interruption.mutation && !a.interruption.concluded {
			result.applied = true
		}
	}()

	archManager := arch.Manager{Runner: a.Runner}
	if p.EnableMultilib {
		if err := a.beginMutation(ctx); err != nil {
			return execution{status: Fatal}
		}
		a.progress("Preparing system...")
		if err := archManager.EnableMultilib(ctx); err != nil {
			return stop("multilib", "core", err, "required repository configuration is unavailable")
		}
	}
	if p.FullUpgrade {
		if err := a.beginMutation(ctx); err != nil {
			return execution{status: Fatal}
		}
		a.progress("Updating system...")
		if err := archManager.FullUpgrade(ctx); err != nil {
			return stop("Arch system upgrade", "core", err, "package installation cannot continue safely")
		}
	}
	if len(p.CorePackages) > 0 {
		if err := a.beginMutation(ctx); err != nil {
			return execution{status: Fatal}
		}
		a.progress("Installing packages...")
	}
	if err := archManager.Install(ctx, p.CorePackages, false); err != nil {
		return stop("core packages", "core", err, "required workstation capabilities are unavailable")
	}

	if err := a.verifyCore(ctx, p); err != nil {
		return stop("core verification", "core", err, "the required core is incomplete")
	}

	aurManager := aur.Manager{Runner: a.Runner}
	flatpakManager := flatpak.Manager{Runner: a.Runner}
	if p.AddFlathub {
		if err := a.beginMutation(ctx); err != nil {
			return execution{status: Fatal}
		}
		a.progress("Preparing Flatpak applications...")
		if err := flatpakManager.AddFlathub(ctx); err != nil {
			return stop("flathub", "core", err, "Flatpak application support is unavailable")
		}
	}

	for _, application := range p.Applications {
		if ctx.Err() != nil {
			return execution{status: Fatal}
		}
		if application.State == "ready" {
			continue
		}
		if application.State.Problem() {
			continue
		}
		if application.State == "configure" {
			a.progress("Configuring " + application.Declaration.Identifier + "...")
			if err := a.configureApplication(ctx, archManager, application); err != nil {
				problems = append(problems, issue{State: "Failed", Name: application.Declaration.Identifier, Source: string(application.Declaration.Source), Cause: err.Error(), Err: err, Impact: "application configuration did not complete normally", Action: "Run ops doctor before retrying."})
				continue
			}
			continue
		}
		aurManager.Review = func(_ string, files map[string]string) error { return a.reviewAUR(ctx, terminal, application, files) }
		if err := a.installApplication(ctx, archManager, aurManager, flatpakManager, application); err != nil {
			if ctx.Err() != nil {
				return execution{status: a.fatal(fmt.Errorf("application setup interrupted: %w", err))}
			}
			if errors.Is(err, io.EOF) {
				return stop("application input", "setup", err, "no further work was approved")
			}
			state := "Failed"
			impact := "application setup did not complete normally; changes may have partially succeeded"
			if errors.Is(err, errReviewDeclined) {
				state = "Skipped"
				impact = "the declared application remains unmet"
			}
			action := "Run ops doctor before retrying."
			var queryErr *resolve.QueryError
			if errors.As(err, &queryErr) {
				action = "Retry later."
			}
			if state == "Skipped" {
				action = "Run ops again when ready to build the declared application."
			}
			problems = append(problems, issue{State: state, Name: application.Declaration.Identifier, Source: string(application.Declaration.Source), Cause: err.Error(), Err: err, Impact: impact, Action: action})
			continue
		}
	}

	if ctx.Err() != nil {
		return execution{status: Fatal}
	}
	gitStatus := p.GitStatus
	if p.ConfigureGit {
		var gitErr error
		gitStatus, gitErr = a.configureGit(ctx, terminal)
		if gitErr != nil {
			if errors.Is(gitErr, io.EOF) {
				return stop("Git input", "setup", gitErr, "no further work was approved")
			}
			problems = append(problems, *setupIssue("Git", gitErr))
		}
	}

	if ctx.Err() != nil {
		return execution{status: Fatal}
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
			return stop("SSH", "setup", fatalErr, "SSH configuration could not safely continue")
		}
	} else if githubWork {
		var err error
		managed, err = a.managedSSHIdentity(ctx)
		if err != nil {
			return stop("SSH", "setup", fmt.Errorf("SSH state changed after planning: %w", err), "GitHub setup could not safely continue")
		}
	}

	if ctx.Err() != nil {
		return execution{status: Fatal}
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
		if _, err := a.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "pacman", Args: []string{"-Q", pkg}}); err != nil {
			return fmt.Errorf("verify prerequisite %s: %w", pkg, err)
		}
	}
	return nil
}

var errReviewDeclined = errors.New("AUR build skipped by user")

// reviewAUR hides only declarative metadata from presentation. The AUR manager
// still validates it and compares every tracked file before executing the build.
func (a Runtime) reviewAUR(ctx context.Context, terminal ui.UI, application plan.Application, files map[string]string) error {
	name := application.Declaration.Identifier
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
	if err := terminal.Review(ctx, ui.ReviewSource{Package: name, PackageBase: application.AURSource.Metadata.PackageBase, Revision: application.AURSource.Commit}, review); err != nil {
		if errors.Is(err, ui.ErrReviewCancelled) {
			return errReviewDeclined
		}
		return fmt.Errorf("AUR review could not be completed; build not approved: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(terminal.Out, "Reviewed build instructions will run as your normal user and can access your files."); err != nil {
		return err
	}
	if len(application.AURSigningKeys) > 0 {
		if _, err := fmt.Fprintln(terminal.Out, "Import public signing keys into your GnuPG keyring:"); err != nil {
			return err
		}
		for _, key := range application.AURSigningKeys {
			if _, err := fmt.Fprintf(terminal.Out, "  %s\n", ui.PrintableASCII(key)); err != nil {
				return err
			}
		}
	}
	var additional []string
	for _, output := range application.AUROutputs {
		if output != name {
			additional = append(additional, output)
		}
	}
	if len(additional) > 0 {
		if _, err := fmt.Fprintln(terminal.Out, "Also install required outputs from this package base:"); err != nil {
			return err
		}
		for _, output := range additional {
			if _, err := fmt.Fprintf(terminal.Out, "  %s\n", ui.PrintableASCII(output)); err != nil {
				return err
			}
		}
	}
	ok, err := terminal.Confirm(ctx, "Build and install "+ui.PrintableASCII(name)+"?", false)
	if err != nil {
		return err
	}
	if !ok {
		return errReviewDeclined
	}
	return nil
}
