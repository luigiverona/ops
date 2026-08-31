// Package app orchestrates the complete ops lifecycle.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/luigiverona/ops/internal/arch"
	"github.com/luigiverona/ops/internal/aur"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/flatpak"
	gitops "github.com/luigiverona/ops/internal/git"
	githubops "github.com/luigiverona/ops/internal/github"
	"github.com/luigiverona/ops/internal/inspect"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/release"
	"github.com/luigiverona/ops/internal/resolve"
	"github.com/luigiverona/ops/internal/run"
	sshops "github.com/luigiverona/ops/internal/ssh"
	sudoops "github.com/luigiverona/ops/internal/sudo"
	"github.com/luigiverona/ops/internal/system"
	"github.com/luigiverona/ops/internal/ui"
	"github.com/luigiverona/ops/internal/version"
)

const (
	Success = 0
	Issues  = 1
	Fatal   = 2

	actionInstall      = "install"
	actionConfigure    = "configure"
	actionUpgrade      = "upgrade"
	actionEnable       = "enable"
	actionAuthenticate = "authenticate"
	actionReview       = "review"
)

// Runtime holds process-scoped dependencies.
type Runtime struct {
	Runner         run.Runner
	Out            io.Writer
	Err            io.Writer
	Home           string
	EUID           func() int
	OSRelease      string
	SSHHTTP        *http.Client
	SSHMetadataURL string
}

type issue struct {
	State, Name, Source, Stage, Cause, Impact, Action string
}

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
	state, err := a.inspectState(ctx)
	if err != nil {
		return a.fatal(fmt.Errorf("inspect workstation: %w", err))
	}
	p, err := plan.Build(ctx, cfg, state, resolve.Resolver{Runner: a.Runner})
	if err != nil {
		return a.fatal(err)
	}

	tty, err := ui.OpenTTY()
	if err != nil {
		return a.fatal(err)
	}
	defer tty.Close()
	if _, ok := a.Runner.(run.Exec); ok {
		a.Runner = run.Exec{In: tty, Out: a.Out, Err: a.Err}
	}
	terminal := ui.UI{In: tty, Out: tty}
	return a.preparePlan(ctx, p, terminal)
}

func (a Runtime) preparePlan(ctx context.Context, p plan.Plan, terminal ui.UI) int {
	a.showPlan(p)
	confirmed, err := terminal.Confirm("Prepare this workstation?", true)
	if err != nil {
		return a.fatal(err)
	}
	if !confirmed {
		fmt.Fprintln(a.Out, "\nFinal\n  system          skipped\n\nWorkstation prepared.")
		return Success
	}

	privileged := needsPrivilege(p)
	var keeper *sudoops.Keeper
	if privileged {
		keeper, err = sudoops.Acquire(ctx, a.Runner)
		if err != nil {
			return a.fatal(fmt.Errorf("sudo authorization failed: %w", err))
		}
		defer keeper.Close()
	}

	archManager := arch.Manager{Runner: a.Runner}
	if p.EnableMultilib {
		a.showProgress("multilib", actionEnable, "pacman repository")
		if err := archManager.EnableMultilib(ctx); err != nil {
			return a.coreFatal("multilib", err, "required repository configuration is unavailable")
		}
	}
	if p.FullUpgrade {
		a.showProgress("full system upgrade", actionUpgrade, "pacman")
		if err := archManager.FullUpgrade(ctx); err != nil {
			return a.coreFatal("Arch system upgrade", err, "package installation cannot continue safely")
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
		return a.coreFatal("core packages", err, "required workstation capabilities are unavailable")
	}

	aurManager := aur.Manager{Runner: a.Runner, Review: func(name string, files map[string]string) error {
		fmt.Fprintf(terminal.Out, "\nAUR build files for %s are untrusted community instructions.\n", name)
		names := make([]string, 0, len(files))
		for filename := range files {
			names = append(names, filename)
		}
		sort.Strings(names)
		for _, filename := range names {
			fmt.Fprintf(terminal.Out, "\nFile %s\n%s\n", filename, files[filename])
		}
		ok, err := terminal.Confirm("Build and install this reviewed AUR package?", false)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("AUR build intentionally skipped")
		}
		return nil
	}}
	if p.BootstrapParu {
		dependencyResolver := resolve.Resolver{Runner: a.Runner}
		afterReview := func() error {
			missing := make(map[string]bool)
			installable := make(map[string]bool, len(p.ParuPackages))
			for _, pkg := range p.ParuPackages {
				installable[pkg.Name] = true
				if pkg.AsExplicit || pkg.RequiredByApplication {
					missing[pkg.Name] = true
				}
			}
			for _, planned := range p.ParuDependencies {
				current, err := dependencyResolver.OfficialDependency(ctx, planned.Requirement)
				if err != nil {
					return fmt.Errorf("revalidate paru dependency %q: %w", planned.Requirement, err)
				}
				if current.Satisfied {
					continue
				}
				if planned.Satisfied || current.Provider != planned.Provider || !packageSubset(current.Packages, planned.Packages) {
					return fmt.Errorf("paru dependency provider changed after planning; rerun ops")
				}
				for _, packageName := range current.Packages {
					if !installable[packageName] {
						return fmt.Errorf("paru dependency transaction changed after planning; rerun ops")
					}
					missing[packageName] = true
				}
			}
			var packages, explicit []string
			var progressRows []ui.TableRow
			for _, pkg := range p.ParuPackages {
				if !missing[pkg.Name] {
					continue
				}
				progressRows = append(progressRows, ui.TableRow{Item: "paru -> " + pkg.Name, Action: actionInstall, Detail: bootstrapPackageDetail(pkg)})
				if pkg.AsExplicit {
					explicit = append(explicit, pkg.Name)
				}
				packages = append(packages, pkg.Name)
			}
			transaction, err := dependencyResolver.OfficialTransaction(ctx, packages)
			if err != nil {
				return fmt.Errorf("revalidate concrete paru dependency transaction: %w", err)
			}
			if !packageSubset(transaction, packages) {
				return fmt.Errorf("paru dependency transaction changed after planning; rerun ops")
			}
			if len(progressRows) > 0 {
				a.showProgressRows(progressRows)
			}
			if err := archManager.Install(ctx, packages, true); err != nil {
				return fmt.Errorf("install paru build dependencies: %w", err)
			}
			if err := archManager.MarkExplicit(ctx, explicit); err != nil {
				return fmt.Errorf("preserve explicit install reason for bootstrap applications: %w", err)
			}
			for _, packageName := range explicit {
				if _, err := a.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Qe", packageName}}); err != nil {
					return fmt.Errorf("verify explicit bootstrap application %q: %w", packageName, err)
				}
			}
			for _, planned := range p.ParuDependencies {
				current, err := dependencyResolver.OfficialDependency(ctx, planned.Requirement)
				if err != nil {
					return fmt.Errorf("verify paru dependency %q: %w", planned.Requirement, err)
				}
				if !current.Satisfied {
					return fmt.Errorf("paru dependency %q is not satisfied after the planned installation", planned.Requirement)
				}
			}
			a.showProgress("paru", actionInstall, "AUR build")
			return nil
		}
		install := func(buildDir string, artifacts []string) error {
			a.showProgress("paru", actionInstall, "local package")
			return archManager.InstallArtifacts(ctx, buildDir, artifacts, p.ParuOutputs)
		}
		if err := aurManager.BootstrapParu(ctx, p.ParuSource, p.ParuOutputs, afterReview, install); err != nil {
			return a.coreFatal("paru", err, "AUR support is unavailable")
		}
	}
	flatpakManager := flatpak.Manager{Runner: a.Runner}
	if p.AddFlathub {
		a.showProgress("flathub", actionEnable, "Flatpak remote")
		if err := flatpakManager.AddFlathub(ctx); err != nil {
			return a.coreFatal("flathub", err, "Flatpak application support is unavailable")
		}
	}
	if err := a.verifyCore(ctx); err != nil {
		return a.coreFatal("core verification", err, "the required core is incomplete")
	}

	var problems []issue
	readyApps := 0
	for _, application := range p.Applications {
		if application.State == "ready" || application.CoveredByBootstrap {
			readyApps++
			continue
		}
		if application.State == "unresolved" || application.State == "failed" {
			problems = append(problems, issue{State: titleState(application.State), Name: application.Declaration.Identifier, Source: application.Declaration.Source, Cause: application.Cause, Impact: "application was not installed", Action: "check the declared identifier and source, then run ops again"})
			continue
		}
		if err := a.installApplication(ctx, archManager, aurManager, flatpakManager, application); err != nil {
			problems = append(problems, issue{State: "Failed", Name: application.Declaration.Identifier, Source: application.Declaration.Source, Cause: err.Error(), Impact: "application was not installed or configured", Action: "resolve the source error and run ops again"})
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
	githubWork := p.AuthenticateGitHub || p.ReviewGitHubKeys || p.ConfigureGitHubKey
	if sshWork {
		var sshIssues []issue
		var fatalErr error
		sshStatus, managed, sshIssues, fatalErr = a.configureSSH(ctx, terminal, p)
		problems = append(problems, sshIssues...)
		if fatalErr != nil {
			return a.fatal(fatalErr)
		}
	} else if githubWork {
		var err error
		managed, err = a.managedSSHIdentity(ctx)
		if err != nil {
			return a.fatal(fmt.Errorf("SSH state changed after planning: %w", err))
		}
	}

	githubStatus := p.GitHubStatus
	if githubWork {
		var githubIssues []issue
		githubStatus, githubIssues = a.configureGitHub(ctx, terminal, managed, p)
		problems = append(problems, githubIssues...)
	} else if sshWork && sshStatus == "ready" && managed != nil {
		if err := (githubops.Manager{Runner: a.Runner}).VerifySSH(ctx); err != nil {
			githubStatus = "failed"
			problems = append(problems, *setupIssue("GitHub SSH verification", err))
		}
	}

	a.report(p, readyApps, gitStatus, sshStatus, githubStatus, problems)
	if len(problems) > 0 {
		return Issues
	}
	return Success
}

func packageSubset(current, planned []string) bool {
	approved := make(map[string]bool, len(planned))
	for _, name := range planned {
		approved[name] = true
	}
	for _, name := range current {
		if !approved[name] {
			return false
		}
	}
	return true
}

func (a Runtime) detect(ctx context.Context) error {
	return (system.Detector{EUID: a.EUID, OSRelease: a.OSRelease, Runner: a.Runner}).Detect(ctx)
}

func (a Runtime) inspectState(ctx context.Context) (plan.State, error) {
	return (inspect.Workstation{
		Runner: a.Runner, Home: a.Home,
		SSHHTTP: a.SSHHTTP, SSHMetadataURL: a.SSHMetadataURL,
	}).State(ctx)
}

// Doctor performs the same detection and planning inspections without mutation or sudo.
func (a Runtime) Doctor(ctx context.Context) int {
	if err := a.detect(ctx); err != nil {
		return a.fatal(fmt.Errorf("doctor could not inspect the system: %w", err))
	}
	cfg, err := config.Load(config.Path(a.Home))
	if err != nil {
		return a.fatal(fmt.Errorf("doctor could not inspect configuration: %w", err))
	}
	state, err := a.inspectState(ctx)
	if err != nil {
		return a.fatal(fmt.Errorf("doctor could not inspect workstation: %w", err))
	}
	if _, err := (sshops.Manager{Home: a.Home, Runner: a.Runner}).Discover(ctx); err != nil {
		return a.fatal(fmt.Errorf("doctor could not reliably inspect SSH identities: %w", err))
	}
	p, err := plan.Build(ctx, cfg, state, resolve.Resolver{Runner: a.Runner})
	if err != nil {
		return a.fatal(fmt.Errorf("doctor could not build diagnostics: %w", err))
	}
	actionable := false
	fmt.Fprintln(a.Out, "Doctor\n\nSystem\n  platform        ready\n  privilege       normal user")
	fmt.Fprintln(a.Out, "\nCore")
	for _, component := range plan.CoreOrder {
		fmt.Fprintf(a.Out, "  %-15s %s\n", component, p.Core[component])
		actionable = actionable || p.Core[component] != "ready"
	}
	fmt.Fprintln(a.Out, "\nApplications")
	if len(p.Applications) == 0 {
		fmt.Fprintln(a.Out, "  declared        none")
	}
	for _, application := range p.Applications {
		fmt.Fprintf(a.Out, "  %-15s %s\n", application.Declaration.Identifier, application.State)
		actionable = actionable || application.State != "ready"
	}
	fmt.Fprintf(a.Out, "\nConfiguration\n  git             %s\n  ssh             %s\n  github          %s\n", p.GitStatus, p.SSHStatus, p.GitHubStatus)
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

// Update installs a newer release only after signature and checksum verification.
func (a Runtime) Update(ctx context.Context) int {
	if err := a.detect(ctx); err != nil {
		return a.fatal(err)
	}
	if _, err := release.CompareVersions(version.Value, version.Value); err != nil {
		return a.fatal(fmt.Errorf("current version %q is not updateable; install a stable release", version.Value))
	}
	client := release.Client{Runner: a.Runner, Trust: release.DefaultTrust()}
	latest, err := client.Latest(ctx)
	if err != nil {
		return a.fatal(fmt.Errorf("resolve latest release: %w", err))
	}
	comparison, err := release.CompareVersions(version.Value, latest)
	if err != nil {
		return a.fatal(err)
	}
	if comparison >= 0 {
		fmt.Fprintf(a.Out, "Update\n  current         %s\n  latest          %s\n  status          up to date\n", version.Value, latest)
		return Success
	}
	tty, err := ui.OpenTTY()
	if err != nil {
		return a.fatal(err)
	}
	defer tty.Close()
	if _, ok := a.Runner.(run.Exec); ok {
		a.Runner = run.Exec{In: tty, Out: a.Out, Err: a.Err}
	}
	terminal := ui.UI{In: tty, Out: tty}
	fmt.Fprintf(a.Out, "Update\n  current         %s\n  latest          %s\n  target          /usr/local/bin/ops\n", version.Value, latest)
	ok, err := terminal.Confirm("Download and verify this update?", true)
	if err != nil {
		return a.fatal(err)
	}
	if !ok {
		fmt.Fprintln(a.Out, "Update skipped.")
		return Success
	}
	verified, err := client.DownloadVerified(ctx, latest)
	if err != nil {
		return a.fatal(err)
	}
	defer verified.Close()
	fmt.Fprintf(a.Out, "Verified\n  release         %s\n  signature       valid\n  sha256          valid\n", latest)
	keeper, err := sudoops.Acquire(ctx, a.Runner)
	if err != nil {
		return a.fatal(fmt.Errorf("sudo authorization failed: %w", err))
	}
	defer keeper.Close()
	if err := release.Replace(ctx, a.Runner, verified.Binary, "/usr/local/bin/ops", latest); err != nil {
		return a.fatal(err)
	}
	fmt.Fprintf(a.Out, "Updated ops to %s.\n", latest)
	return Success
}

func needsPrivilege(p plan.Plan) bool {
	if p.EnableMultilib || p.FullUpgrade || len(p.CorePackages) > 0 || p.BootstrapParu {
		return true
	}
	for _, app := range p.Applications {
		if app.State == "install" && (app.Declaration.Source == "pacman" || len(app.Services) > 0 || len(app.Dependencies) > 0) {
			return true
		}
	}
	return false
}

func (a Runtime) installApplication(ctx context.Context, am arch.Manager, au aur.Manager, fm flatpak.Manager, application plan.Application) error {
	var deps []string
	for _, dependency := range application.Dependencies {
		if dependency.Source == "pacman" {
			deps = append(deps, dependency.Identifier)
		}
	}
	if len(deps) > 0 {
		rows := make([]ui.TableRow, 0, len(deps))
		for _, dependency := range deps {
			rows = append(rows, ui.TableRow{Item: application.Declaration.Identifier + " -> " + dependency, Action: actionInstall, Detail: "pacman"})
		}
		a.showProgressRows(rows)
	}
	if err := am.Install(ctx, deps, true); err != nil {
		return fmt.Errorf("recommended dependency installation failed: %w", err)
	}
	for _, dependency := range application.Dependencies {
		if dependency.Source == "aur" {
			a.showProgress(application.Declaration.Identifier+" -> "+dependency.Identifier, actionInstall, "aur")
			if err := au.InstallDependency(ctx, dependency.Identifier); err != nil {
				return fmt.Errorf("recommended AUR dependency installation failed: %w", err)
			}
		}
	}
	name := application.Declaration.Identifier
	a.showProgress(name, actionInstall, application.Declaration.Source)
	switch application.Declaration.Source {
	case "pacman":
		if err := am.Install(ctx, []string{name}, false); err != nil {
			return err
		}
	case "aur":
		if err := au.Install(ctx, name); err != nil {
			return err
		}
	case "flatpak":
		if err := fm.Install(ctx, name); err != nil {
			return err
		}
	}
	for _, service := range application.Services {
		a.showProgress(application.Declaration.Identifier+" -> "+service, actionEnable, "systemd")
		if _, err := a.Runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"-n", "systemctl", "enable", "--now", service}}); err != nil {
			return err
		}
	}
	switch application.Declaration.Source {
	case "pacman":
		_, err := a.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Qn", name}})
		return err
	case "aur":
		_, err := a.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Qm", name}})
		return err
	case "flatpak":
		if !fm.Ready(ctx, name) {
			return errors.New("Flatpak postcondition verification failed")
		}
	}
	return nil
}

func (a Runtime) verifyCore(ctx context.Context) error {
	for _, pkg := range []string{"git", "openssh", "github-cli", "base-devel", "flatpak"} {
		if _, err := a.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Q", pkg}}); err != nil {
			return fmt.Errorf("%s is not installed: %w", pkg, err)
		}
	}
	if _, err := a.Runner.Run(ctx, run.Spec{Name: "paru", Args: []string{"--version"}}); err != nil {
		return err
	}
	result, err := a.Runner.Run(ctx, run.Spec{Name: "flatpak", Args: []string{"remotes", "--user", "--columns=name"}})
	if err != nil || !linePresent(result.Stdout, "flathub") {
		return errors.New("user Flathub remote is not ready")
	}
	return nil
}

func (a Runtime) configureGit(ctx context.Context, terminal ui.UI) (string, *issue) {
	m := gitops.Manager{Runner: a.Runner}
	current := m.Inspect(ctx)
	if gitops.ValidName(current.Name) && gitops.ValidEmail(current.Email) {
		return "ready", nil
	}
	name, email := current.Name, current.Email
	var err error
	if !gitops.ValidName(name) {
		name, err = terminal.Ask("Git user.name:")
		if err != nil {
			return "failed", setupIssue("Git", err)
		}
	}
	if !gitops.ValidEmail(email) {
		email, err = terminal.Ask("Git user.email:")
		if err != nil {
			return "failed", setupIssue("Git", err)
		}
	}
	a.showProgress("git", actionConfigure, "user identity")
	if err := m.SetMissing(ctx, current, name, email); err != nil {
		return "failed", setupIssue("Git", err)
	}
	return "ready", nil
}

func (a Runtime) configureSSH(ctx context.Context, terminal ui.UI, p plan.Plan) (string, *sshops.Identity, []issue, error) {
	m := sshops.Manager{Home: a.Home, Runner: a.Runner, HTTP: a.SSHHTTP, MetadataURL: a.SSHMetadataURL}
	identities, err := m.Discover(ctx)
	if err != nil {
		return "failed", nil, nil, fmt.Errorf("unsafe SSH state: %w", err)
	}
	var managed *sshops.Identity
	for _, identity := range identities {
		if identity.PrivatePath == filepath.Join(a.Home, ".ssh", "ops") {
			if identity.PublicPath == filepath.Join(a.Home, ".ssh", "ops.pub") {
				copy := identity
				managed = &copy
			}
		}
	}
	if p.ReviewSSHIdentities {
		var unrelated []sshops.Identity
		for _, identity := range identities {
			if identity.PrivatePath == filepath.Join(a.Home, ".ssh", "ops") {
				continue
			}
			unrelated = append(unrelated, identity)
		}
		if len(unrelated) > 0 {
			a.showProgress("SSH identities", actionReview, "unrelated local keys")
		}
		for i, identity := range unrelated {
			fmt.Fprintf(a.Out, "\nIdentity %d/%d\n  path            %s\n  fingerprint     %s\n", i+1, len(unrelated), identity.PrivatePath, identity.Fingerprint)
			keep, err := terminal.Confirm("Keep this identity?", true)
			if err != nil {
				return "failed", managed, nil, err
			}
			if keep {
				continue
			}
			fmt.Fprintln(a.Out, "Files selected for permanent deletion:")
			fmt.Fprintf(a.Out, "  %s\n", identity.PrivatePath)
			if identity.PublicPath != "" {
				fmt.Fprintf(a.Out, "  %s\n", identity.PublicPath)
			}
			remove, err := terminal.Confirm("Permanently delete this identity?", false)
			if err != nil {
				return "failed", managed, nil, err
			}
			if remove {
				if err := m.Delete(ctx, identity); err != nil {
					return "failed", managed, []issue{*setupIssue("SSH identity deletion", err)}, nil
				}
			}
		}
	}
	if p.CreateSSHIdentity && managed == nil {
		a.showProgress("SSH identity", actionConfigure, "managed Ed25519 key")
		identity, err := m.EnsureIdentity(ctx)
		if err != nil {
			return "failed", nil, []issue{*setupIssue("SSH", err)}, nil
		}
		managed = &identity
	}
	if managed == nil {
		return "failed", nil, nil, errors.New("managed SSH identity changed after planning")
	}
	if p.ReviewSSHAgent || p.LoadSSHAgent {
		agentKeys, available, err := m.AgentIdentities(ctx)
		if err != nil {
			return "failed", managed, []issue{*setupIssue("ssh-agent", err)}, nil
		}
		loaded := false
		if available {
			unrelated := make([]sshops.AgentIdentity, 0, len(agentKeys))
			for _, key := range agentKeys {
				if key.Fingerprint == managed.Fingerprint {
					loaded = true
					continue
				}
				unrelated = append(unrelated, key)
			}
			if p.ReviewSSHAgent && len(unrelated) > 0 {
				a.showProgress("ssh-agent identities", actionReview, "unrelated loaded keys")
			}
			for i, key := range unrelated {
				if !p.ReviewSSHAgent {
					break
				}
				fmt.Fprintf(a.Out, "\nAgent identity %d/%d\n  fingerprint     %s\n", i+1, len(unrelated), key.Fingerprint)
				keep, err := terminal.Confirm("Keep this identity loaded in ssh-agent?", true)
				if err != nil {
					return "failed", managed, nil, err
				}
				if !keep {
					if err := m.Unload(ctx, key); err != nil {
						return "failed", managed, []issue{*setupIssue("ssh-agent", err)}, nil
					}
				}
			}
			if p.LoadSSHAgent && !loaded {
				a.showProgress("ssh-agent managed key", actionConfigure, "load identity")
				_ = m.Load(ctx, managed.PrivatePath)
			}
		}
	}
	if p.ConfigureSSH {
		a.showProgress("github.com SSH configuration", actionConfigure, "managed identity and host trust")
		if err := m.ConfigureGitHub(ctx); err != nil {
			return "failed", managed, nil, fmt.Errorf("unsafe required SSH configuration: %w", err)
		}
	}
	return "ready", managed, nil, nil
}

func (a Runtime) managedSSHIdentity(ctx context.Context) (*sshops.Identity, error) {
	identities, err := (sshops.Manager{Home: a.Home, Runner: a.Runner}).Discover(ctx)
	if err != nil {
		return nil, err
	}
	privatePath := filepath.Join(a.Home, ".ssh", "ops")
	for _, identity := range identities {
		if identity.PrivatePath == privatePath && identity.PublicPath == privatePath+".pub" {
			copy := identity
			return &copy, nil
		}
	}
	return nil, errors.New("managed SSH identity is unavailable")
}

func (a Runtime) configureGitHub(ctx context.Context, terminal ui.UI, managed *sshops.Identity, p plan.Plan) (string, []issue) {
	if managed == nil {
		return "skipped", nil
	}
	m := githubops.Manager{Runner: a.Runner}
	if p.AuthenticateGitHub && !m.Authenticated(ctx) {
		a.showProgress("github", actionAuthenticate, "CLI login")
		if err := m.Login(ctx); err != nil {
			return "failed", []issue{*setupIssue("GitHub authentication", err)}
		}
	}
	keys, err := m.Keys(ctx)
	if err != nil {
		return "failed", []issue{*setupIssue("GitHub SSH keys", err)}
	}
	registered := false
	var unrelated []githubops.Key
	for _, key := range keys {
		if key.Fingerprint == managed.Fingerprint {
			registered = true
			continue
		}
		unrelated = append(unrelated, key)
	}
	if p.ReviewGitHubKeys && len(unrelated) > 0 {
		a.showProgress("GitHub SSH keys", actionReview, "account keys")
		for i, key := range unrelated {
			fmt.Fprintf(a.Out, "\nGitHub key %d/%d\n  title           %s\n  fingerprint     %s\n", i+1, len(unrelated), key.Title, key.Fingerprint)
			keep, err := terminal.Confirm("Keep this key?", true)
			if err != nil {
				return "failed", []issue{*setupIssue("GitHub SSH keys", err)}
			}
			if keep {
				continue
			}
			remove, err := terminal.Confirm("Remove this key from GitHub?", false)
			if err != nil {
				return "failed", []issue{*setupIssue("GitHub SSH keys", err)}
			}
			if remove {
				if err := m.Delete(ctx, key); err != nil {
					return "failed", []issue{*setupIssue("GitHub SSH key deletion", err)}
				}
			}
		}
	}
	if p.ConfigureGitHubKey && !registered {
		a.showProgress("GitHub SSH key", actionConfigure, "managed key")
		if _, err := m.AddManaged(ctx, managed.PublicPath); err != nil {
			return "failed", []issue{*setupIssue("GitHub SSH key", err)}
		}
	}
	if err := m.VerifySSH(ctx); err != nil {
		return "failed", []issue{*setupIssue("GitHub SSH verification", err)}
	}
	return "ready", nil
}

func (a Runtime) showPlan(p plan.Plan) {
	fmt.Fprintln(a.Out, "Plan")
	sections := planSections(p)
	hasActions := false
	for _, section := range sections {
		hasActions = hasActions || !section.Diagnostic
	}
	if !hasActions && p.SSHHostKeyFreshness == plan.SSHHostKeyFreshnessUnavailable {
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
		systemRows = append(systemRows, ui.TableRow{Item: "full system upgrade", Action: actionUpgrade, Detail: "pacman"})
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
	for _, pkg := range p.ParuPackages {
		coreRows = append(coreRows, ui.TableRow{Item: "paru -> " + pkg.Name, Action: actionInstall, Detail: bootstrapPackageDetail(pkg)})
	}
	if p.BootstrapParu {
		coreRows = append(coreRows, ui.TableRow{Item: "paru", Action: actionInstall, Detail: "AUR bootstrap; review required"})
	}
	if p.AddFlathub {
		coreRows = append(coreRows, ui.TableRow{Item: "flathub", Action: actionEnable, Detail: "Flatpak remote"})
	}
	if len(coreRows) > 0 {
		sections = append(sections, outputSection{Name: "Core", Rows: coreRows})
	}

	var applicationRows []ui.TableRow
	for _, application := range p.Applications {
		if application.State == "ready" || application.CoveredByBootstrap {
			continue
		}
		if application.State != "install" {
			detail := application.Declaration.Source
			if application.Cause != "" {
				detail += "; " + application.Cause
			}
			applicationRows = append(applicationRows, ui.TableRow{Item: application.Declaration.Identifier, Action: actionReview, Detail: detail})
			continue
		}
		dependencies := append([]plan.Dependency(nil), application.Dependencies...)
		sort.Slice(dependencies, func(i, j int) bool {
			if dependencies[i].Identifier == dependencies[j].Identifier {
				return dependencies[i].Source < dependencies[j].Source
			}
			return dependencies[i].Identifier < dependencies[j].Identifier
		})
		for _, dependency := range dependencies {
			applicationRows = append(applicationRows, ui.TableRow{Item: application.Declaration.Identifier + " -> " + dependency.Identifier, Action: actionInstall, Detail: dependency.Source})
		}
		detail := application.Declaration.Source
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
		accessRows = append(accessRows, ui.TableRow{Item: "github", Action: actionAuthenticate, Detail: "CLI login"})
	}
	if p.ReviewGitHubKeys {
		detail := "account keys"
		if p.GitHubKeyAfterIdentity {
			detail = "reconcile after identity creation"
		} else if p.GitHubKeyStateUnknown {
			detail = "existing keys after login, if present"
		}
		accessRows = append(accessRows, ui.TableRow{Item: "GitHub SSH keys", Action: actionReview, Detail: detail})
	}
	if p.ConfigureGitHubKey {
		detail := "managed key"
		if p.GitHubKeyAfterIdentity {
			detail = "register after identity creation, if missing"
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

func bootstrapPackageDetail(pkg plan.BootstrapPackage) string {
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
	fmt.Fprintf(a.Out, "\nProgress\n%s", ui.RenderTable(rows))
}

func (a Runtime) report(p plan.Plan, ready int, gitStatus, sshStatus, githubStatus string, problems []issue) {
	for _, problem := range problems {
		fmt.Fprintf(a.Out, "\n%s\n\n%s\n", problem.State, problem.Name)
		if problem.Source != "" {
			fmt.Fprintf(a.Out, "  source          %s\n", problem.Source)
		}
		if problem.Stage != "" {
			fmt.Fprintf(a.Out, "  stage           %s\n", problem.Stage)
		}
		fmt.Fprintf(a.Out, "  cause           %s\n  impact          %s\n  action          %s\n", problem.Cause, problem.Impact, problem.Action)
	}
	fmt.Fprintf(a.Out, "\nFinal\n  system          ready\n  core            7/7\n  apps            %d/%d\n  git             %s\n  ssh             %s\n  github          %s\n\n", ready, len(p.Applications), gitStatus, sshStatus, githubStatus)
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

func (a Runtime) fatal(err error) int {
	fmt.Fprintf(a.Err, "Failed\n\nops\n  cause           %s\n  impact          workstation preparation could not safely continue\n  action          resolve the error and run ops again\n\nWorkstation preparation stopped.\n", err)
	return Fatal
}

func (a Runtime) coreFatal(name string, err error, impact string) int {
	fmt.Fprintf(a.Err, "Failed\n\n%s\n  stage           core\n  cause           %s\n  impact          %s\n  action          resolve the package error and run ops again\n\nWorkstation preparation stopped.\n", name, err, impact)
	return Fatal
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
func DefaultRuntime() Runtime {
	home, _ := os.UserHomeDir()
	return Runtime{Runner: run.Exec{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}, Out: os.Stdout, Err: os.Stderr, Home: home, EUID: os.Geteuid}
}
