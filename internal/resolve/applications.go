package resolve

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
)

// MetadataResolver performs read-only exact-source resolution.
type MetadataResolver interface {
	Pacman(context.Context, string) (plan.Package, bool, error)
	AUR(context.Context, string) (plan.Package, bool, error)
	AURSource(context.Context, string) (plan.AURSource, bool, error)
	OfficialDependency(context.Context, string) (plan.OfficialDependency, error)
	UserPGPKey(context.Context, string) (bool, error)
	CompareVersions(context.Context, string, string) (int, error)
	Flatpak(context.Context, string) (bool, error)
}

// Applications resolves only missing declarations, without installing prerequisites.
// pacman/vercmp are supplied by the supported base system; AUR and Flatpak
// metadata use HTTPS and never need Git, makepkg, or flatpak executables.
func Applications(ctx context.Context, cfg config.Config, state plan.State, resolver MetadataResolver) plan.Facts {
	facts := make(plan.Facts)
	declaredPacman := make(map[string]bool)
	declaredAUR := make(map[string]bool)
	type sourceResult struct {
		source plan.AURSource
		found  bool
		err    error
	}
	sources := make(map[string]sourceResult)
	for _, declaration := range cfg.Applications {
		switch declaration.Source {
		case "pacman":
			declaredPacman[declaration.Identifier] = true
		case "aur":
			declaredAUR[declaration.Identifier] = true
		}
	}

	for _, declaration := range cfg.Applications {
		app := plan.Application{Declaration: declaration}
		if plan.IsInstalled(declaration, state) {
			continue
		}
		var metadata plan.Package
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
			app.State = plan.Unavailable
			app.Err = err
			app.Cause = "could not query the declared source: " + err.Error()
			facts[declaration] = app
			continue
		}
		if !found {
			app.State = "unresolved"
			app.ConfirmedAbsent = true
			app.Cause = "exact identifier was not found in the declared source"
			facts[declaration] = app
			continue
		}
		app.State = "install"
		if declaration.Source == "pacman" {
			app.EnableMultilib = metadata.Repository == "multilib"
		}
		if declaration.Source == "aur" {
			pinned, cached := sources[metadata.PackageBase]
			if !cached {
				pinned.source, pinned.found, pinned.err = resolver.AURSource(ctx, metadata.PackageBase)
				sources[metadata.PackageBase] = pinned
			}
			source, sourceFound, sourceErr := pinned.source, pinned.found, pinned.err
			if sourceErr != nil || !sourceFound || source.Commit == "" || source.Metadata.PackageBase != metadata.PackageBase {
				app.State = plan.Unavailable
				app.Err = sourceErr
				if sourceErr != nil {
					app.Cause = "pinned AUR source resolution failed: " + sourceErr.Error()
				} else {
					app.Cause = "pinned AUR source is unavailable"
				}
				facts[declaration] = app
				continue
			}
			outputs, dependencies, packages, buildErr := resolveAURBuild(ctx, resolver, source, declaration.Identifier, declaredPacman, state.Installed, state.Explicit, state.Foreign)
			if buildErr != nil {
				app.State = "failed"
				var queryErr *QueryError
				if errors.As(buildErr, &queryErr) {
					app.State = plan.Unavailable
				}
				app.Err = buildErr
				app.Cause = "AUR build dependency resolution failed: " + buildErr.Error()
				facts[declaration] = app
				continue
			}
			for _, fingerprint := range source.Metadata.ValidPGPKeys {
				present, keyErr := resolver.UserPGPKey(ctx, fingerprint)
				if keyErr != nil {
					app.State = "failed"
					app.Err = keyErr
					app.Cause = "AUR signing-key inspection failed: " + keyErr.Error()
					break
				}
				if !present {
					app.AURSigningKeys = append(app.AURSigningKeys, fingerprint)
				}
			}
			if app.State == "failed" {
				facts[declaration] = app
				continue
			}
			for _, output := range outputs {
				if output == declaration.Identifier || declaredAUR[output] || (state.Installed[output] && state.Explicit[output] && state.Foreign[output]) {
					app.AURExplicitOutputs = append(app.AURExplicitOutputs, output)
				}
			}
			app.AURSource, app.AUROutputs, app.AURDependencies, app.AURPackages = source, outputs, dependencies, packages
		}

		facts[declaration] = app
	}

	return facts
}
func resolveAURBuild(ctx context.Context, resolver MetadataResolver, source plan.AURSource, target string, declared, installed, explicit, foreign map[string]bool) ([]string, []plan.OfficialDependency, []plan.BuildPackage, error) {
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
	sort.Slice(requirements, func(i, j int) bool {
		if requirements[i].Expression == requirements[j].Expression {
			return requirements[i].Purpose < requirements[j].Purpose
		}
		return requirements[i].Expression < requirements[j].Expression
	})
	resolved := make(map[string]plan.OfficialDependency)
	packages := make(map[string]*plan.BuildPackage)
	var bindings []plan.OfficialDependency
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
				pkg = &plan.BuildPackage{Name: packageName, AsExplicit: declared[packageName] || (installed[packageName] && explicit[packageName] && !foreign[packageName])}
				packages[packageName] = pkg
			}
			pkg.Purposes = appendUnique(pkg.Purposes, requirement.Purpose)
			if packageName == binding.Provider && aurmeta.DependencyName(requirement.Expression) != binding.Provider {
				pkg.Provides = appendUnique(pkg.Provides, aurmeta.DependencyName(requirement.Expression))
			}
		}
	}
	planned := make([]plan.BuildPackage, 0, len(packages))
	for _, pkg := range packages {
		sort.Strings(pkg.Purposes)
		sort.Strings(pkg.Provides)
		planned = append(planned, *pkg)
	}
	sort.Slice(planned, func(i, j int) bool { return planned[i].Name < planned[j].Name })
	return outputs, bindings, planned, nil
}

func validateOfficialDependency(binding plan.OfficialDependency, requirement string) error {
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
		if !aurmeta.ValidPackageName(name) || seen[name] {
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

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
