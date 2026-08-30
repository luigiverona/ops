// Package plan builds a deterministic reconciliation plan from declared intent and actual state.
package plan

import (
	"context"
	"sort"
	"strings"

	"github.com/luigiverona/ops/internal/config"
)

var CorePackages = map[string]string{
	"git": "git", "ssh": "openssh", "github": "github-cli", "flatpak": "flatpak",
}

var CoreOrder = []string{"git", "ssh", "github", "aur", "paru", "flatpak", "flathub"}

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
	Installed                   map[string]bool
	Foreign                     map[string]bool
	Flatpaks                    map[string]bool
	Paru                        bool
	Flathub                     bool
	Multilib                    bool
	GitName                     string
	GitEmail                    string
	ManagedSSHIdentity          bool
	SSHConfigurationReady       bool // recognized, locally safe managed configuration
	SSHHostKeyFreshness         SSHHostKeyFreshness
	UnrelatedSSHIdentities      int
	SSHAgentAvailable           bool
	ManagedSSHAgentIdentity     bool
	UnrelatedSSHAgentIdentities int
	GitHubAuth                  bool
	GitHubKeysKnown             bool // remote list retrieval succeeded
	ManagedGitHubKeyKnown       bool // comparison was possible with a local fingerprint
	ManagedGitHubKey            bool // the exact managed fingerprint was found
	OtherGitHubKeys             int
}

// Package is remote metadata required to plan optional functionality and repositories.
type Package struct {
	Name        string
	Repository  string
	Optional    []string
	Required    []string
	Conflicts   []string
	PackageBase string
}

// Resolver performs read-only exact-source resolution.
type Resolver interface {
	Pacman(context.Context, string) (Package, bool, error)
	AUR(context.Context, string) (Package, bool, error)
	Flatpak(context.Context, string) (bool, error)
}

// Application records one requested application's planned outcome.
type Application struct {
	Declaration  config.Application
	State        string // ready, install, unresolved
	Dependencies []Dependency
	Services     []string
	Cause        string
}

// Dependency is a compatible direct optional dependency selected for normal functionality.
type Dependency struct {
	Source     string
	Identifier string
}

// Plan is a complete, immutable plan presented before authorization.
type Plan struct {
	Core                   map[string]string
	Applications           []Application
	CorePackages           []string
	EnableMultilib         bool
	FullUpgrade            bool
	BootstrapParu          bool
	AddFlathub             bool
	GitStatus              string
	SSHStatus              string
	GitHubStatus           string
	ConfigureGit           bool
	CreateSSHIdentity      bool
	ReviewSSHIdentities    bool
	ReviewSSHAgent         bool
	LoadSSHAgent           bool
	ConfigureSSH           bool
	AuthenticateGitHub     bool
	ReviewGitHubKeys       bool
	ConfigureGitHubKey     bool
	GitHubKeyStateUnknown  bool
	GitHubKeyAfterIdentity bool
	SSHHostKeyFreshness    SSHHostKeyFreshness
}

// Build resolves only missing declarations and produces a deterministic plan.
func Build(ctx context.Context, cfg config.Config, state State, resolver Resolver) (Plan, error) {
	p := Plan{Core: make(map[string]string)}
	for _, component := range CoreOrder {
		p.Core[component] = coreState(component, state)
	}
	for component, pkg := range CorePackages {
		if p.Core[component] != "ready" {
			p.CorePackages = append(p.CorePackages, pkg)
		}
	}
	if !state.Installed["base-devel"] {
		p.CorePackages = append(p.CorePackages, "base-devel")
	}
	p.BootstrapParu = !state.Paru
	p.AddFlathub = !state.Flathub
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
	p.GitHubKeyStateUnknown = !state.GitHubAuth || !state.GitHubKeysKnown || !state.ManagedGitHubKeyKnown
	p.GitHubKeyAfterIdentity = !state.ManagedSSHIdentity
	p.ConfigureGitHubKey = p.GitHubKeyStateUnknown || !state.ManagedGitHubKey
	p.ReviewGitHubKeys = p.GitHubKeyStateUnknown || (p.ConfigureGitHubKey && state.OtherGitHubKeys > 0)
	p.GitStatus = pairStatus(!p.ConfigureGit)
	p.SSHStatus = pairStatus(!p.CreateSSHIdentity && !p.ReviewSSHIdentities && !p.ReviewSSHAgent && !p.LoadSSHAgent && !p.ConfigureSSH)
	if state.SSHConfigurationReady && p.SSHHostKeyFreshness == SSHHostKeyFreshnessUnavailable {
		p.SSHStatus = "unavailable"
	}
	p.GitHubStatus = pairStatus(!p.AuthenticateGitHub && !p.ReviewGitHubKeys && !p.ConfigureGitHubKey)

	for _, declaration := range cfg.Applications {
		app := Application{Declaration: declaration}
		if isReady(declaration, state) {
			app.State = "ready"
			p.Applications = append(p.Applications, app)
			continue
		}
		var metadata Package
		var found bool
		var err error
		switch declaration.Source {
		case "pacman":
			metadata, found, err = resolver.Pacman(ctx, declaration.Identifier)
		case "aur":
			metadata, found, err = resolver.AUR(ctx, declaration.Identifier)
		case "flatpak":
			found, err = resolver.Flatpak(ctx, declaration.Identifier)
		}
		if err != nil {
			app.State = "failed"
			app.Cause = "source resolution failed: " + err.Error()
			p.Applications = append(p.Applications, app)
			continue
		}
		if !found {
			app.State = "unresolved"
			app.Cause = "exact identifier was not found in the declared source"
			p.Applications = append(p.Applications, app)
			continue
		}
		app.State = "install"
		if declaration.Source != "flatpak" {
			optional, multilib, err := optionalDependencies(ctx, metadata, state, resolver)
			if err != nil {
				app.State = "failed"
				app.Cause = "optional dependency resolution failed: " + err.Error()
				p.Applications = append(p.Applications, app)
				continue
			}
			app.Dependencies = optional
			p.EnableMultilib = p.EnableMultilib || multilib || metadata.Repository == "multilib"
			for _, raw := range metadata.Required {
				name := dependencyName(raw)
				if dependency, found, resolveErr := resolver.Pacman(ctx, name); resolveErr == nil && found && dependency.Repository == "multilib" {
					p.EnableMultilib = true
				}
			}
		}
		if declaration.Identifier == "mullvad-vpn" {
			app.Services = append(app.Services, "mullvad-daemon.service")
		}
		p.Applications = append(p.Applications, app)
	}

	p.EnableMultilib = p.EnableMultilib && !state.Multilib
	p.CorePackages = uniqueSorted(p.CorePackages)
	deduplicateApplicationActions(&p)
	p.FullUpgrade = len(p.CorePackages) > 0 || p.BootstrapParu || hasPackageInstall(p.Applications)
	return p, nil
}

func deduplicateApplicationActions(p *Plan) {
	representedInstalls := make(map[string]bool)
	for _, pkg := range p.CorePackages {
		representedInstalls["pacman\x00"+pkg] = true
	}
	for _, application := range p.Applications {
		if application.State == "install" {
			representedInstalls[application.Declaration.Source+"\x00"+application.Declaration.Identifier] = true
		}
	}
	seenDependencies := make(map[string]bool)
	seenServices := make(map[string]bool)
	for i := range p.Applications {
		dependencies := p.Applications[i].Dependencies[:0]
		for _, dependency := range p.Applications[i].Dependencies {
			key := dependency.Source + "\x00" + dependency.Identifier
			if representedInstalls[key] || seenDependencies[key] {
				continue
			}
			seenDependencies[key] = true
			dependencies = append(dependencies, dependency)
		}
		p.Applications[i].Dependencies = dependencies

		services := p.Applications[i].Services[:0]
		for _, service := range p.Applications[i].Services {
			if seenServices[service] {
				continue
			}
			seenServices[service] = true
			services = append(services, service)
		}
		p.Applications[i].Services = services
	}
}

func coreState(component string, state State) string {
	switch component {
	case "aur":
		if state.Installed["git"] && state.Installed["base-devel"] && state.Paru {
			return "ready"
		}
	case "paru":
		if state.Paru {
			return "ready"
		}
	case "flathub":
		if state.Flathub {
			return "ready"
		}
	default:
		if state.Installed[CorePackages[component]] {
			return "ready"
		}
	}
	return "required"
}

func isReady(app config.Application, state State) bool {
	switch app.Source {
	case "pacman":
		return state.Installed[app.Identifier] && !state.Foreign[app.Identifier]
	case "aur":
		return state.Installed[app.Identifier] && state.Foreign[app.Identifier]
	case "flatpak":
		return state.Flatpaks[app.Identifier]
	}
	return false
}

func optionalDependencies(ctx context.Context, pkg Package, state State, resolver Resolver) ([]Dependency, bool, error) {
	type candidate struct {
		dependency Dependency
		metadata   Package
	}
	var candidates []candidate
	multilib := false
	for _, raw := range pkg.Optional {
		name := dependencyName(raw)
		if name == "" || state.Installed[name] {
			continue
		}
		metadata, found, err := resolver.Pacman(ctx, name)
		if err != nil {
			return nil, false, err
		}
		if !found {
			metadata, found, err = resolver.AUR(ctx, name)
			if err != nil {
				return nil, false, err
			}
			if !found {
				continue
			}
			candidates = append(candidates, candidate{Dependency{"aur", name}, metadata})
			continue
		}
		candidates = append(candidates, candidate{Dependency{"pacman", name}, metadata})
		multilib = multilib || metadata.Repository == "multilib"
	}
	conflicting := make(map[string]bool)
	for i := range candidates {
		for j := i + 1; j < len(candidates); j++ {
			if conflicts(candidates[i].metadata, candidates[j].metadata) {
				conflicting[candidates[i].dependency.Identifier] = true
				conflicting[candidates[j].dependency.Identifier] = true
			}
		}
	}
	var selected []Dependency
	for _, candidate := range candidates {
		if !conflicting[candidate.dependency.Identifier] {
			selected = append(selected, candidate.dependency)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Identifier < selected[j].Identifier })
	return selected, multilib, nil
}

func dependencyName(value string) string {
	value, _, _ = strings.Cut(value, ":")
	value = strings.TrimSpace(value)
	if i := strings.IndexAny(value, "<>="); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}

func conflicts(a, b Package) bool {
	for _, value := range a.Conflicts {
		if dependencyName(value) == b.Name {
			return true
		}
	}
	for _, value := range b.Conflicts {
		if dependencyName(value) == a.Name {
			return true
		}
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
