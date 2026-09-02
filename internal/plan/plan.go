// Package plan builds a deterministic reconciliation plan from declared intent and actual state.
package plan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/luigiverona/ops/internal/aurmeta"
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
	Installed                     map[string]bool
	Explicit                      map[string]bool
	Foreign                       map[string]bool
	Flatpaks                      map[string]bool
	Paru                          bool
	Flathub                       bool
	Multilib                      bool
	GitName                       string
	GitEmail                      string
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
	AURSource(context.Context, string) (AURSource, bool, error)
	OfficialDependency(context.Context, string) (OfficialDependency, error)
	UserPGPKey(context.Context, string) (bool, error)
	CompareVersions(context.Context, string, string) (int, error)
	Flatpak(context.Context, string) (bool, error)
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
	Provider    string
	Packages    []string
	Satisfied   bool
}

// BootstrapPackage is one concrete official package installed before building a
// pinned AUR source.
type BootstrapPackage struct {
	Name                  string
	Purposes              []string
	Provides              []string
	AsExplicit            bool
	RequiredByApplication bool
}

// Application records one requested application's planned outcome.
type Application struct {
	Declaration        config.Application
	State              string // ready, install, configure, unresolved, failed
	Dependencies       []Dependency
	AURSource          AURSource
	AURDependencies    []OfficialDependency
	AURPackages        []BootstrapPackage
	AUROutputs         []string
	AURExplicitOutputs []string
	AURSigningKeys     []string // missing exact validpgpkeys planned for import
	Services           []string
	Cause              string
	CoveredByBootstrap bool
}

// Dependency is a compatible direct optional dependency selected for normal functionality.
type Dependency struct {
	Source      string
	Identifier  string
	Requirement string // Arch dependency expression retained for deterministic resolution
}

// Plan is a complete, immutable plan presented before authorization.
type Plan struct {
	Core                     map[string]string
	Applications             []Application
	CorePackages             []string
	EnableMultilib           bool
	FullUpgrade              bool
	BootstrapParu            bool
	ParuSource               AURSource
	ParuDependencies         []OfficialDependency
	ParuPackages             []BootstrapPackage
	ParuOutputs              []string
	AddFlathub               bool
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

// Build resolves only missing declarations and produces a deterministic plan.
func Build(ctx context.Context, cfg config.Config, state State, resolver Resolver) (Plan, error) {
	p := Plan{Core: make(map[string]string)}
	declaredPacman := make(map[string]bool)
	declaredAUR := make(map[string]bool)
	for _, declaration := range cfg.Applications {
		switch declaration.Source {
		case "pacman":
			declaredPacman[declaration.Identifier] = true
		case "aur":
			declaredAUR[declaration.Identifier] = true
		}
	}
	for _, component := range CoreOrder {
		p.Core[component] = coreState(component, state)
	}
	for component, pkg := range CorePackages {
		if p.Core[component] != "ready" {
			p.CorePackages = append(p.CorePackages, pkg)
		}
	}
	p.BootstrapParu = !state.Paru
	if !state.Installed["base-devel"] && !p.BootstrapParu {
		p.CorePackages = append(p.CorePackages, "base-devel")
	}
	if p.BootstrapParu {
		source, found, err := resolver.AURSource(ctx, "paru")
		if err != nil {
			return Plan{}, fmt.Errorf("resolve paru source metadata: %w", err)
		}
		if !found || source.Commit == "" || source.Metadata.PackageBase != "paru" {
			return Plan{}, fmt.Errorf("resolve paru source metadata: exact AUR source is unavailable")
		}
		outputs, dependencies, packages, err := resolveAURBuild(ctx, resolver, source, "paru", nil, nil, state.Installed, state.Explicit, state.Foreign)
		if err != nil {
			return Plan{}, fmt.Errorf("resolve paru build: %w", err)
		}
		p.ParuSource, p.ParuOutputs, p.ParuDependencies, p.ParuPackages = source, outputs, dependencies, packages
	}
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
		if isReady(declaration, state) {
			app.State = "ready"
			if (declaration.Source == "pacman" || declaration.Source == "aur") && !state.Explicit[declaration.Identifier] {
				app.State = "configure"
			}
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
		if declaration.Source == "pacman" {
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
		if declaration.Source == "aur" {
			source, sourceFound, sourceErr := resolver.AURSource(ctx, metadata.PackageBase)
			if sourceErr != nil || !sourceFound || source.Commit == "" || source.Metadata.PackageBase != metadata.PackageBase {
				app.State = "failed"
				if sourceErr != nil {
					app.Cause = "pinned AUR source resolution failed: " + sourceErr.Error()
				} else {
					app.Cause = "pinned AUR source is unavailable"
				}
				p.Applications = append(p.Applications, app)
				continue
			}
			compareVersions := func(left, right string) (int, error) { return resolver.CompareVersions(ctx, left, right) }
			optionalRequirements, optionalErr := source.Metadata.OptionalRequirements(declaration.Identifier, compareVersions)
			if optionalErr != nil {
				app.State = "failed"
				app.Cause = "pinned AUR optional dependency resolution failed: " + optionalErr.Error()
				p.Applications = append(p.Applications, app)
				continue
			}
			optionalPackage := Package{Optional: make([]string, 0, len(optionalRequirements))}
			for _, requirement := range optionalRequirements {
				optionalPackage.Optional = append(optionalPackage.Optional, requirement.Expression)
			}
			optional, multilib, optionalErr := optionalDependencies(ctx, optionalPackage, state, resolver)
			if optionalErr != nil {
				app.State = "failed"
				app.Cause = "pinned AUR optional dependency resolution failed: " + optionalErr.Error()
				p.Applications = append(p.Applications, app)
				continue
			}
			app.Dependencies = optional
			p.EnableMultilib = p.EnableMultilib || multilib
			unsupportedOptional := ""
			for _, dependency := range app.Dependencies {
				if dependency.Source == "aur" {
					unsupportedOptional = dependency.Identifier
					break
				}
			}
			if unsupportedOptional != "" {
				app.State = "failed"
				app.Cause = "AUR optional dependency " + unsupportedOptional + " cannot be resolved deterministically"
				p.Applications = append(p.Applications, app)
				continue
			}
			extraRequirements := make([]aurmeta.Requirement, 0, len(app.Dependencies))
			for _, dependency := range app.Dependencies {
				extraRequirements = append(extraRequirements, aurmeta.Requirement{Expression: dependency.Requirement, Purpose: "optional"})
			}
			outputs, dependencies, packages, buildErr := resolveAURBuild(ctx, resolver, source, declaration.Identifier, extraRequirements, declaredPacman, state.Installed, state.Explicit, state.Foreign)
			if buildErr != nil {
				app.State = "failed"
				app.Cause = "AUR build dependency resolution failed: " + buildErr.Error()
				p.Applications = append(p.Applications, app)
				continue
			}
			for _, fingerprint := range source.Metadata.ValidPGPKeys {
				present, keyErr := resolver.UserPGPKey(ctx, fingerprint)
				if keyErr != nil {
					app.State = "failed"
					app.Cause = "AUR signing-key inspection failed: " + keyErr.Error()
					break
				}
				if !present {
					app.AURSigningKeys = append(app.AURSigningKeys, fingerprint)
				}
			}
			if app.State == "failed" {
				p.Applications = append(p.Applications, app)
				continue
			}
			for _, output := range outputs {
				if output == declaration.Identifier || declaredAUR[output] || (state.Installed[output] && state.Explicit[output] && state.Foreign[output]) {
					app.AURExplicitOutputs = append(app.AURExplicitOutputs, output)
				}
			}
			app.AURSource, app.AUROutputs, app.AURDependencies, app.AURPackages = source, outputs, dependencies, packages
		}
		if declaration.Identifier == "mullvad-vpn" {
			app.Services = append(app.Services, "mullvad-daemon.service")
		}
		p.Applications = append(p.Applications, app)
	}

	p.EnableMultilib = p.EnableMultilib && !state.Multilib
	p.CorePackages = uniqueSorted(p.CorePackages)
	finalizeParuPackages(&p)
	deduplicateApplicationActions(&p)
	p.FullUpgrade = len(p.CorePackages) > 0 || p.BootstrapParu || hasPackageInstall(p.Applications)
	return p, nil
}

func resolveAURBuild(ctx context.Context, resolver Resolver, source AURSource, target string, additional []aurmeta.Requirement, declared, installed, explicit, foreign map[string]bool) ([]string, []OfficialDependency, []BootstrapPackage, error) {
	compareVersions := func(left, right string) (int, error) { return resolver.CompareVersions(ctx, left, right) }
	outputs, err := source.Metadata.OutputClosure(target, compareVersions)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve outputs: %w", err)
	}
	requirements, err := source.Metadata.BuildRequirements(target, true, compareVersions)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve build requirements: %w", err)
	}
	// base-devel is makepkg's documented implicit build prerequisite and must
	// be materialized even when the AUR metadata does not declare it.
	requirements = append(requirements, aurmeta.Requirement{Expression: "base-devel", Purpose: "build"})
	requirements = append(requirements, additional...)
	sort.Slice(requirements, func(i, j int) bool {
		if requirements[i].Expression == requirements[j].Expression {
			return requirements[i].Purpose < requirements[j].Purpose
		}
		return requirements[i].Expression < requirements[j].Expression
	})
	resolved := make(map[string]OfficialDependency)
	packages := make(map[string]*BootstrapPackage)
	var bindings []OfficialDependency
	for _, requirement := range requirements {
		binding, ok := resolved[requirement.Expression]
		if !ok {
			binding, err = resolver.OfficialDependency(ctx, requirement.Expression)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("resolve dependency %q: %w", requirement.Expression, err)
			}
			binding.Packages = append([]string(nil), binding.Packages...)
			sort.Strings(binding.Packages)
			if err := validateOfficialDependency(binding, requirement.Expression); err != nil {
				return nil, nil, nil, fmt.Errorf("resolve dependency %q: resolver returned an invalid binding: %w", requirement.Expression, err)
			}
			resolved[requirement.Expression] = binding
			bindings = append(bindings, binding)
		}
		if binding.Satisfied {
			continue
		}
		for _, packageName := range binding.Packages {
			pkg := packages[packageName]
			if pkg == nil {
				pkg = &BootstrapPackage{Name: packageName, AsExplicit: declared[packageName] || (installed[packageName] && explicit[packageName] && !foreign[packageName])}
				packages[packageName] = pkg
			}
			pkg.Purposes = appendUnique(pkg.Purposes, requirement.Purpose)
			if packageName == binding.Provider && aurmeta.DependencyName(requirement.Expression) != binding.Provider {
				pkg.Provides = appendUnique(pkg.Provides, aurmeta.DependencyName(requirement.Expression))
			}
		}
	}
	planned := make([]BootstrapPackage, 0, len(packages))
	for _, pkg := range packages {
		sort.Strings(pkg.Purposes)
		sort.Strings(pkg.Provides)
		planned = append(planned, *pkg)
	}
	sort.Slice(planned, func(i, j int) bool { return planned[i].Name < planned[j].Name })
	return outputs, bindings, planned, nil
}

func validateOfficialDependency(binding OfficialDependency, requirement string) error {
	if binding.Requirement != requirement {
		return fmt.Errorf("requirement mismatch")
	}
	if binding.Satisfied {
		if binding.Provider != "" || len(binding.Packages) != 0 {
			return fmt.Errorf("satisfied dependency includes a repository transaction")
		}
		return nil
	}
	if binding.Provider == "" || len(binding.Packages) == 0 {
		return fmt.Errorf("missing provider transaction")
	}
	seen := make(map[string]bool, len(binding.Packages))
	providerFound := false
	for _, name := range binding.Packages {
		if name == "" || seen[name] {
			return fmt.Errorf("invalid transaction package")
		}
		seen[name] = true
		providerFound = providerFound || name == binding.Provider
	}
	if !providerFound {
		return fmt.Errorf("provider is absent from transaction")
	}
	return nil
}

func finalizeParuPackages(p *Plan) {
	core := make(map[string]bool, len(p.CorePackages))
	for _, name := range p.CorePackages {
		core[name] = true
	}
	for i := range p.Applications {
		app := &p.Applications[i]
		if app.State != "install" {
			continue
		}
		if app.Declaration.Source == "pacman" {
			for j := range p.ParuPackages {
				if p.ParuPackages[j].Name != app.Declaration.Identifier {
					continue
				}
				p.ParuPackages[j].AsExplicit = true
				app.CoveredByBootstrap = true
			}
		}
		for _, dependency := range app.Dependencies {
			if dependency.Source != "pacman" {
				continue
			}
			for j := range p.ParuPackages {
				if p.ParuPackages[j].Name == dependency.Identifier {
					p.ParuPackages[j].RequiredByApplication = true
				}
			}
		}
	}
	packages := p.ParuPackages[:0]
	for _, pkg := range p.ParuPackages {
		if !core[pkg.Name] {
			packages = append(packages, pkg)
		}
	}
	p.ParuPackages = packages
}

func deduplicateApplicationActions(p *Plan) {
	representedInstalls := make(map[string]bool)
	for _, pkg := range p.CorePackages {
		representedInstalls["pacman\x00"+pkg] = true
	}
	for _, application := range p.Applications {
		if application.State == "install" && !application.CoveredByBootstrap {
			representedInstalls[application.Declaration.Source+"\x00"+application.Declaration.Identifier] = true
		}
	}
	for _, pkg := range p.ParuPackages {
		representedInstalls["pacman\x00"+pkg.Name] = true
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

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
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
			candidates = append(candidates, candidate{Dependency{Source: "aur", Identifier: name, Requirement: dependencyExpression(raw)}, metadata})
			continue
		}
		candidates = append(candidates, candidate{Dependency{Source: "pacman", Identifier: name, Requirement: dependencyExpression(raw)}, metadata})
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
	value = dependencyExpression(value)
	if i := strings.IndexAny(value, "<>="); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}

func dependencyExpression(value string) string {
	expression, err := aurmeta.OptionalDependencyExpression(value)
	if err != nil {
		return ""
	}
	return expression
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
