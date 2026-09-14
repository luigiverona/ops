// Package plan builds a deterministic reconciliation plan from declared intent and actual state.
package plan

import (
	"sort"

	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/flatpak"
)

var CorePackages = map[string]string{
	"git": "git", "ssh": "openssh", "github": "github-cli",
}

var CoreOrder = []string{"git", "ssh", "github", "flatpak", "flathub"}

// SSHHostKeyFreshness is the result of comparing recognized local host keys
// with successfully validated authoritative metadata.
type SSHHostKeyFreshness string

const (
	SSHHostKeyFreshnessUnknown     SSHHostKeyFreshness = "unknown"
	SSHHostKeyFreshnessCurrent     SSHHostKeyFreshness = "current"
	SSHHostKeyFreshnessStale       SSHHostKeyFreshness = "stale"
	SSHHostKeyFreshnessUnavailable SSHHostKeyFreshness = "unavailable"
)

// State is discovered from real package and user configuration state.
type State struct {
	Services                      map[string]bool // required services enabled and active
	Installed                     map[string]bool
	Explicit                      map[string]bool
	OfficialMatches               map[string]string // name -> repo/name; current metadata match, not historical origin
	Foreign                       map[string]bool
	Flatpaks                      map[string]string // application ID -> origin
	Flathub                       flatpak.Remote
	Multilib                      bool
	GitName                       string
	GitEmail                      string
	ManagedSSHFingerprint         string
	ManagedSSHIdentity            bool
	SSHConfigurationReady         bool // recognized, locally safe managed configuration
	SSHHostKeyFreshness           SSHHostKeyFreshness
	UnrelatedSSHIdentities        int
	SSHAgentAvailable             bool
	ManagedSSHAgentIdentity       bool
	UnrelatedSSHAgentIdentities   int
	GitHubAuth                    bool
	GitHubSSHKeyScopeInsufficient bool // gh authenticated, but user/keys requires admin:public_key
	GitHubKeysKnown               bool // remote list retrieval succeeded
	ManagedGitHubKeyKnown         bool // comparison was possible with a local fingerprint
	ManagedGitHubKey              bool // the exact managed fingerprint was found
	OtherGitHubKeys               int
}

// Package is the exact source identity and repository needed for planning.
type Package struct {
	Name        string
	Repository  string
	PackageBase string
}

// AURSource pins declarative metadata to the exact reviewed Git commit.
type AURSource struct {
	Commit   string
	Metadata aurmeta.Metadata
}

// OfficialDependency binds an AUR dependency expression to installed state or
// the exact repository package selected by pacman's native resolver.
type OfficialDependency struct {
	Requirement string
	Provider    string   // validated repo/name, including an already-satisfied provider
	Packages    []string // concrete repo/name closure, including satisfied dependencies
	Satisfied   bool
}

// BuildPackage is one concrete official package installed before building a
// pinned AUR source.
type BuildPackage struct {
	Name       string
	Repository string
	Purposes   []string
	Provides   []string
	AsExplicit bool
}

// Application records one requested application's planned outcome.
type Application struct {
	Package            Package
	EnableMultilib     bool
	Declaration        config.Application
	State              ApplicationState
	AURSource          AURSource
	AURDependencies    []OfficialDependency
	AURPackages        []BuildPackage
	AUROutputs         []string
	AURExplicitOutputs []string
	AURSigningKeys     []string // missing exact validpgpkeys planned for import
	Services           []string
	Cause              string
	Err                error
	ConfirmedAbsent    bool
}

// Plan is a complete, immutable plan presented before authorization.
type Plan struct {
	Core                     map[string]string
	Applications             []Application
	CorePackages             []string
	EnableMultilib           bool
	FullUpgrade              bool
	UpgradeTargets           []string // installed managed official targets protected during the system upgrade
	AddFlathub               bool
	EnableFlathub            bool
	GitStatus                string
	SSHStatus                string
	GitHubStatus             string
	ConfigureGit             bool
	CreateSSHIdentity        bool
	ReviewSSHIdentities      bool
	ReviewSSHAgent           bool
	LoadSSHAgent             bool
	ConfigureSSH             bool
	AuthenticateGitHub       bool
	RefreshGitHubSSHKeyScope bool
	ReviewGitHubKeys         bool
	ConfigureGitHubKey       bool
	GitHubKeyStateUnknown    bool
	GitHubKeyAfterIdentity   bool
	SSHHostKeyFreshness      SSHHostKeyFreshness
}

// Facts contains already-resolved application outcomes. Build performs no I/O.
type Facts map[config.Application]Application

// Build constructs a deterministic plan from validated intent and observed facts.
func Build(cfg config.Config, state State, facts Facts) Plan {
	p := Plan{Core: make(map[string]string)}
	for _, component := range CoreOrder {
		p.Core[component] = coreState(component, state)
	}
	for component, pkg := range CorePackages {
		if state.Installed[pkg] {
			p.UpgradeTargets = append(p.UpgradeTargets, archrepo.Prerequisite(pkg))
		}
		if p.Core[component] != "ready" {
			p.CorePackages = append(p.CorePackages, pkg)
		}
	}
	// Flatpak is managed only for declared Flatpak applications.
	wantsFlatpak := false
	for _, app := range cfg.Applications {
		wantsFlatpak = wantsFlatpak || app.Source == config.Flatpak
	}
	if wantsFlatpak {
		if state.Installed["flatpak"] {
			p.UpgradeTargets = append(p.UpgradeTargets, archrepo.Prerequisite("flatpak"))
		}
		p.Core["flatpak"] = "missing"
		if officialInstalled("flatpak", state) {
			p.Core["flatpak"] = "ready"
		} else {
			p.CorePackages = append(p.CorePackages, "flatpak")
		}
		p.Core["flathub"] = "missing"
		if state.Flathub.Ready() {
			p.Core["flathub"] = "ready"
		}
		p.AddFlathub = state.Flathub.Name == ""
		p.EnableFlathub = state.Flathub.Canonical() && !state.Flathub.Enabled
		if state.Flathub.Name != "" && !state.Flathub.Ready() {
			p.Core["flathub"] = "incompatible Flathub remote; inspect flatpak remotes --user --show-disabled"
			if p.EnableFlathub {
				p.Core["flathub"] = "disabled; enable required"
			}
		}
	} else {
		p.Core["flatpak"], p.Core["flathub"] = "not required", "not required"
	}
	p.ConfigureGit = state.GitName == "" || state.GitEmail == ""
	p.CreateSSHIdentity = !state.ManagedSSHIdentity
	p.SSHHostKeyFreshness = state.SSHHostKeyFreshness
	if p.SSHHostKeyFreshness == "" {
		p.SSHHostKeyFreshness = SSHHostKeyFreshnessUnknown
	}
	if state.SSHConfigurationReady && p.SSHHostKeyFreshness == SSHHostKeyFreshnessUnknown {
		p.SSHHostKeyFreshness = SSHHostKeyFreshnessUnavailable
	}
	p.ConfigureSSH = !state.SSHConfigurationReady || p.SSHHostKeyFreshness == SSHHostKeyFreshnessStale
	sshSetupRequired := p.CreateSSHIdentity || !state.SSHConfigurationReady
	p.ReviewSSHIdentities = sshSetupRequired && state.UnrelatedSSHIdentities > 0
	p.ReviewSSHAgent = sshSetupRequired && state.SSHAgentAvailable && state.UnrelatedSSHAgentIdentities > 0
	p.LoadSSHAgent = sshSetupRequired && state.SSHAgentAvailable && !state.ManagedSSHAgentIdentity
	p.AuthenticateGitHub = !state.GitHubAuth
	p.RefreshGitHubSSHKeyScope = state.GitHubAuth && state.GitHubSSHKeyScopeInsufficient
	p.GitHubKeyStateUnknown = !state.GitHubAuth || p.RefreshGitHubSSHKeyScope || !state.GitHubKeysKnown || !state.ManagedGitHubKeyKnown
	p.GitHubKeyAfterIdentity = !state.ManagedSSHIdentity
	p.ConfigureGitHubKey = p.GitHubKeyStateUnknown || !state.ManagedGitHubKey
	p.ReviewGitHubKeys = p.GitHubKeyStateUnknown || (p.ConfigureGitHubKey && state.OtherGitHubKeys > 0)
	p.GitStatus = pairStatus(!p.ConfigureGit)
	p.SSHStatus = pairStatus(!p.CreateSSHIdentity && !p.ReviewSSHIdentities && !p.ReviewSSHAgent && !p.LoadSSHAgent && !p.ConfigureSSH)
	if state.SSHConfigurationReady && p.SSHHostKeyFreshness == SSHHostKeyFreshnessUnavailable {
		p.SSHStatus = "unavailable"
	}
	p.GitHubStatus = pairStatus(!p.AuthenticateGitHub && !p.RefreshGitHubSSHKeyScope && !p.ReviewGitHubKeys && !p.ConfigureGitHubKey)

	for _, declaration := range cfg.Applications {
		app := Application{Declaration: declaration}
		if IsInstalled(declaration, state) {
			app.State = "ready"
			if declaration.Source == config.Pacman {
				repo, _, _ := archrepo.Split(state.OfficialMatches[declaration.Identifier])
				app.Package = Package{Name: declaration.Identifier, Repository: repo}
			}
			if declaration.Source != config.Flatpak && !state.Explicit[declaration.Identifier] {
				app.State = "configure"
			}
		} else if declaration.Source == config.Flatpak && state.Flatpaks[declaration.Identifier] == "flathub" && state.Flathub.Canonical() {
			app.State = Configure
			app.Cause = "enable the canonical user Flathub remote"
		} else if declaration.Source == config.Flatpak && (state.Flatpaks[declaration.Identifier] != "" || (state.Flathub.Name != "" && !state.Flathub.Canonical())) {
			app.State = Failed
			app.Cause = "incompatible Flatpak origin or flathub remote; inspect flatpak list --user --app --columns=application,origin and flatpak remotes --user --show-disabled; reconcile the source manually"
		} else if resolved, ok := facts[declaration]; ok {
			app = resolved
			app.Declaration = declaration
			if app.State != Install && app.State != Unresolved && app.State != Unavailable && app.State != Failed {
				app.State, app.Cause = Failed, "invalid resolution outcome; no application operation authorized"
			}
		} else {
			app.State, app.Cause = "unresolved", "required source metadata is unavailable; retry ops"
		}
		if service := RequiredService(declaration); service != "" && (app.State == "install" || app.State == "ready" || app.State == "configure") && !state.Services[service] {
			app.Services = []string{service}
			if app.State == "ready" {
				app.State = "configure"
			}
		}
		if declaration.Source == config.Pacman && state.Installed[declaration.Identifier] && archrepo.Official(app.Package.Repository) {
			p.UpgradeTargets = append(p.UpgradeTargets, app.Package.Repository+"/"+declaration.Identifier)
		}
		for _, dependency := range app.AURDependencies {
			if dependency.Satisfied {
				p.UpgradeTargets = append(p.UpgradeTargets, dependency.Provider)
			}
		}
		p.EnableMultilib = p.EnableMultilib || app.EnableMultilib
		p.Applications = append(p.Applications, app)
	}

	p.EnableMultilib = p.EnableMultilib && !state.Multilib
	p.UpgradeTargets = uniqueSorted(p.UpgradeTargets)
	p.CorePackages = uniqueSorted(p.CorePackages)
	p.FullUpgrade = len(p.CorePackages) > 0 || hasPackageInstall(p.Applications)
	return p
}

func coreState(component string, state State) string {
	switch component {
	case "flathub":
		if state.Flathub.Ready() {
			return "ready"
		}
	default:
		if officialInstalled(CorePackages[component], state) {
			return "ready"
		}
	}
	return "missing"
}

// IsInstalled checks the declared source as well as the package name.
func IsInstalled(app config.Application, state State) bool {
	switch app.Source {
	case "pacman":
		return officialInstalled(app.Identifier, state)
	case "aur":
		return state.Installed[app.Identifier] && state.Foreign[app.Identifier]
	case "flatpak":
		return state.Flathub.Ready() && state.Flatpaks[app.Identifier] == "flathub"
	}
	return false
}

func pairStatus(ready bool) string {
	if ready {
		return "ready"
	}
	return "configuration required"
}

func hasPackageInstall(apps []Application) bool {
	for _, app := range apps {
		if app.State == "install" && app.Declaration.Source != "flatpak" {
			return true
		}
	}
	return false
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

// RequiredService contains the small, objective service policy shared by
// inspection and planning. A Flatpak with a similar name never enables a host service.
func RequiredService(app config.Application) string {
	if app.Source != config.Flatpak && app.Identifier == "mullvad-vpn" {
		return "mullvad-daemon.service"
	}
	return ""
}

// HasActions is domain policy, independent of terminal rendering.
func (p Plan) HasActions() bool {
	if p.EnableMultilib || p.FullUpgrade || len(p.CorePackages) > 0 || p.AddFlathub || p.EnableFlathub ||
		p.ConfigureGit || p.CreateSSHIdentity || p.ReviewSSHIdentities || p.ReviewSSHAgent ||
		p.LoadSSHAgent || p.ConfigureSSH || p.AuthenticateGitHub || p.RefreshGitHubSSHKeyScope ||
		p.ReviewGitHubKeys || p.ConfigureGitHubKey {
		return true
	}
	for _, app := range p.Applications {
		if app.State == "install" || app.State == "configure" {
			return true
		}
	}
	return false
}

// ApplicationState keeps resolution diagnostics distinct from executable actions.
type ApplicationState string

const (
	Ready       ApplicationState = "ready"
	Install     ApplicationState = "install"
	Configure   ApplicationState = "configure"
	Unresolved  ApplicationState = "unresolved"
	Unavailable ApplicationState = "unavailable"
	Failed      ApplicationState = "failed"
)

func (s ApplicationState) Actionable() bool { return s == Install || s == Configure }
func (s ApplicationState) Problem() bool    { return s != Ready && !s.Actionable() }

func officialInstalled(name string, state State) bool {
	_, matched, err := archrepo.Split(state.OfficialMatches[name])
	return state.Installed[name] && err == nil && matched == name
}
