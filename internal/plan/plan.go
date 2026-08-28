// Package plan builds a deterministic reconciliation plan from declared intent and actual state.
package plan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/luigiverona/ops/internal/config"
)

var CorePackages = map[string]string{
	"git": "git", "ssh": "openssh", "github": "github-cli", "flatpak": "flatpak",
}

var CoreOrder = []string{"git", "ssh", "github", "aur", "paru", "flatpak", "flathub"}

// State is discovered from real package and user configuration state.
type State struct {
	Installed       map[string]bool
	Foreign         map[string]bool
	Flatpaks        map[string]bool
	Paru            bool
	Flathub         bool
	Multilib        bool
	GitName         string
	GitEmail        string
	SSHReady        bool
	GitHubAuth      bool
	GitHubSSHAccess bool
}

// Package is remote metadata required to plan optional functionality and repositories.
type Package struct {
	Name        string
	Repository  string
	Optional    []string
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
	Core             map[string]string
	Applications     []Application
	OfficialPackages []string
	EnableMultilib   bool
	FullUpgrade      bool
	BootstrapParu    bool
	AddFlathub       bool
	GitStatus        string
	SSHStatus        string
	GitHubStatus     string
}

// Build resolves only missing declarations and produces a deterministic plan.
func Build(ctx context.Context, cfg config.Config, state State, resolver Resolver) (Plan, error) {
	p := Plan{Core: make(map[string]string)}
	for _, component := range CoreOrder {
		p.Core[component] = coreState(component, state)
	}
	for component, pkg := range CorePackages {
		if p.Core[component] != "ready" {
			p.OfficialPackages = append(p.OfficialPackages, pkg)
		}
	}
	if !state.Installed["base-devel"] {
		p.OfficialPackages = append(p.OfficialPackages, "base-devel")
	}
	p.BootstrapParu = !state.Paru
	p.AddFlathub = !state.Flathub
	p.GitStatus = pairStatus(state.GitName != "" && state.GitEmail != "")
	p.SSHStatus = pairStatus(state.SSHReady)
	p.GitHubStatus = pairStatus(state.GitHubAuth && state.GitHubSSHAccess)

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
			return Plan{}, fmt.Errorf("resolve %s:%s: %w", declaration.Source, declaration.Identifier, err)
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
				return Plan{}, fmt.Errorf("resolve optional dependencies for %s: %w", declaration.Identifier, err)
			}
			app.Dependencies = optional
			p.EnableMultilib = p.EnableMultilib || multilib || metadata.Repository == "multilib"
		}
		if declaration.Source == "pacman" {
			p.OfficialPackages = append(p.OfficialPackages, declaration.Identifier)
		}
		if declaration.Identifier == "mullvad-vpn" {
			app.Services = append(app.Services, "mullvad-daemon.service")
		}
		p.Applications = append(p.Applications, app)
	}

	p.EnableMultilib = p.EnableMultilib && !state.Multilib
	p.OfficialPackages = uniqueSorted(p.OfficialPackages)
	p.FullUpgrade = len(p.OfficialPackages) > 0 || p.BootstrapParu || hasAURInstall(p.Applications)
	return p, nil
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
			continue // Do not silently cross ecosystems for optional dependencies.
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

func hasAURInstall(apps []Application) bool {
	for _, app := range apps {
		if app.State == "install" && app.Declaration.Source == "aur" {
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
