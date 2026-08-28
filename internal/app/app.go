// Package app orchestrates the complete ops lifecycle.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
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
)

// Runtime holds process-scoped dependencies.
type Runtime struct {
	Runner    run.Runner
	Out       io.Writer
	Err       io.Writer
	Home      string
	EUID      func() int
	OSRelease string
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
	state, err := (inspect.Workstation{Runner: a.Runner, Home: a.Home}).State(ctx)
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
		if err := archManager.EnableMultilib(ctx); err != nil {
			return a.coreFatal("multilib", err, "required repository configuration is unavailable")
		}
	}
	if p.FullUpgrade {
		fmt.Fprintln(a.Out, "\nPreparing\n  stage           system upgrade")
		if err := archManager.FullUpgrade(ctx); err != nil {
			return a.coreFatal("Arch system upgrade", err, "package installation cannot continue safely")
		}
	}
	if err := archManager.Install(ctx, p.CorePackages, false); err != nil {
		return a.coreFatal("core packages", err, "required workstation capabilities are unavailable")
	}

	aurManager := aur.Manager{Runner: a.Runner, Review: func(name string, files map[string]string) error {
		fmt.Fprintf(tty, "\nAUR build files for %s are untrusted community instructions.\n", name)
		names := make([]string, 0, len(files))
		for filename := range files {
			names = append(names, filename)
		}
		sort.Strings(names)
		for _, filename := range names {
			fmt.Fprintf(tty, "\nFile %s\n%s\n", filename, files[filename])
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
		if err := aurManager.BootstrapParu(ctx); err != nil {
			return a.coreFatal("paru", err, "AUR support is unavailable")
		}
	}
	flatpakManager := flatpak.Manager{Runner: a.Runner}
	if p.AddFlathub {
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
		if application.State == "ready" {
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

	gitStatus, gitIssue := a.configureGit(ctx, terminal)
	if gitIssue != nil {
		problems = append(problems, *gitIssue)
	}
	sshStatus, managed, sshIssues, fatalErr := a.configureSSH(ctx, terminal)
	problems = append(problems, sshIssues...)
	if fatalErr != nil {
		return a.fatal(fatalErr)
	}
	githubStatus, githubIssues := a.configureGitHub(ctx, terminal, managed)
	problems = append(problems, githubIssues...)

	a.report(p, readyApps, gitStatus, sshStatus, githubStatus, problems)
	if len(problems) > 0 {
		return Issues
	}
	return Success
}

func (a Runtime) detect(ctx context.Context) error {
	return (system.Detector{EUID: a.EUID, OSRelease: a.OSRelease, Runner: a.Runner}).Detect(ctx)
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
	state, err := (inspect.Workstation{Runner: a.Runner, Home: a.Home}).State(ctx)
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
	if p.EnableMultilib || p.FullUpgrade || len(p.CorePackages) > 0 {
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
	if err := am.Install(ctx, deps, true); err != nil {
		return fmt.Errorf("recommended dependency installation failed: %w", err)
	}
	for _, dependency := range application.Dependencies {
		if dependency.Source == "aur" {
			if err := au.InstallDependency(ctx, dependency.Identifier); err != nil {
				return fmt.Errorf("recommended AUR dependency installation failed: %w", err)
			}
		}
	}
	name := application.Declaration.Identifier
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
	ok, err := terminal.Confirm("Configure missing Git identity values?", true)
	if err != nil || !ok {
		return "skipped", nil
	}
	name, email := current.Name, current.Email
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
	if err := m.SetMissing(ctx, current, name, email); err != nil {
		return "failed", setupIssue("Git", err)
	}
	return "ready", nil
}

func (a Runtime) configureSSH(ctx context.Context, terminal ui.UI) (string, *sshops.Identity, []issue, error) {
	m := sshops.Manager{Home: a.Home, Runner: a.Runner}
	identities, err := m.Discover(ctx)
	if err != nil {
		return "failed", nil, nil, fmt.Errorf("unsafe SSH state: %w", err)
	}
	var managed *sshops.Identity
	for i, identity := range identities {
		if identity.PrivatePath == filepath.Join(a.Home, ".ssh", "ops") {
			copy := identity
			managed = &copy
			continue
		}
		fmt.Fprintf(a.Out, "\nIdentity %d/%d\n  path            %s\n  fingerprint     %s\n", i+1, len(identities), identity.PrivatePath, identity.Fingerprint)
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
	if managed == nil {
		create, err := terminal.Confirm("Create the managed ~/.ssh/ops Ed25519 identity?", true)
		if err != nil {
			return "failed", nil, nil, err
		}
		if !create {
			return "skipped", nil, nil, nil
		}
		identity, err := m.EnsureIdentity(ctx)
		if err != nil {
			return "failed", nil, []issue{*setupIssue("SSH", err)}, nil
		}
		managed = &identity
	}
	agentKeys, available, err := m.AgentIdentities(ctx)
	if err != nil {
		return "failed", managed, []issue{*setupIssue("ssh-agent", err)}, nil
	}
	loaded := false
	if available {
		for i, key := range agentKeys {
			if key.Fingerprint == managed.Fingerprint {
				loaded = true
				continue
			}
			fmt.Fprintf(a.Out, "\nAgent identity %d/%d\n  fingerprint     %s\n", i+1, len(agentKeys), key.Fingerprint)
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
		if !loaded {
			_ = m.Load(ctx, managed.PrivatePath)
		}
	}
	if err := m.ConfigureGitHub(ctx); err != nil {
		return "failed", managed, nil, fmt.Errorf("unsafe required SSH configuration: %w", err)
	}
	return "ready", managed, nil, nil
}

func (a Runtime) configureGitHub(ctx context.Context, terminal ui.UI, managed *sshops.Identity) (string, []issue) {
	if managed == nil {
		return "skipped", nil
	}
	m := githubops.Manager{Runner: a.Runner}
	if !m.Authenticated(ctx) {
		login, err := terminal.Confirm("Authenticate GitHub CLI?", true)
		if err != nil || !login {
			return "skipped", nil
		}
		if err := m.Login(ctx); err != nil {
			return "failed", []issue{*setupIssue("GitHub authentication", err)}
		}
	}
	keys, err := m.Keys(ctx)
	if err != nil {
		return "failed", []issue{*setupIssue("GitHub SSH keys", err)}
	}
	registered := false
	for i, key := range keys {
		if key.Fingerprint == managed.Fingerprint {
			registered = true
		}
		fmt.Fprintf(a.Out, "\nGitHub key %d/%d\n  title           %s\n  fingerprint     %s\n", i+1, len(keys), key.Title, key.Fingerprint)
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
			if key.Fingerprint == managed.Fingerprint {
				registered = false
			}
		}
	}
	if !registered {
		add, err := terminal.Confirm("Register ~/.ssh/ops.pub with GitHub?", true)
		if err != nil {
			return "failed", []issue{*setupIssue("GitHub SSH key", err)}
		}
		if !add {
			return "skipped", nil
		}
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
	fmt.Fprintln(a.Out, "Plan\n\nCore")
	for _, name := range plan.CoreOrder {
		fmt.Fprintf(a.Out, "  %-15s %s\n", name, p.Core[name])
	}
	fmt.Fprintln(a.Out, "\nApplications")
	if len(p.Applications) == 0 {
		fmt.Fprintln(a.Out, "  declared        none")
	}
	for _, app := range p.Applications {
		fmt.Fprintf(a.Out, "  %-15s %s (%s)\n", app.Declaration.Identifier, app.State, app.Declaration.Source)
	}
	fmt.Fprintf(a.Out, "\nConfiguration\n  git             %s\n  ssh             %s\n  github          %s\n", p.GitStatus, p.SSHStatus, p.GitHubStatus)
	if p.EnableMultilib {
		fmt.Fprintln(a.Out, "\nSystem\n  multilib        enable")
	}
	if p.FullUpgrade {
		fmt.Fprintln(a.Out, "  full upgrade    required")
	}
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
