package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/luigiverona/ops/internal/arch"
	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/aur"
	"github.com/luigiverona/ops/internal/flatpak"
	"github.com/luigiverona/ops/internal/pgp"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/resolve"
	"github.com/luigiverona/ops/internal/run"
)

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

func (a Runtime) installApplication(ctx context.Context, am arch.Manager, au aur.Manager, fm flatpak.Manager, application plan.Application) error {
	name := application.Declaration.Identifier
	if application.Declaration.Source != "aur" {
		if err := a.beginMutation(ctx); err != nil {
			return err
		}
		if application.OfficialState != nil && application.OfficialState.Action() == archrepo.Update {
			a.progress("Verifying updated " + name + "...")
		} else {
			a.progress("Installing " + name + "...")
		}
	}
	switch application.Declaration.Source {
	case "pacman":
		install := true
		if application.OfficialState != nil {
			evidence, err := archrepo.InspectInstalled(ctx, a.Runner, application.Package.Repository+"/"+name)
			if err != nil {
				return err
			}
			install = !evidence.Ready()
			if install && (application.OfficialState.Action() != archrepo.Repair || evidence.Action() != archrepo.Repair) {
				return fmt.Errorf("%s: %s; reconcile full upgrade policy and rerun ops", name, evidence.Description())
			}
		}
		if install {
			if err := am.Install(ctx, []string{application.Package.Repository + "/" + name}, false); err != nil {
				return err
			}
		}
	case "aur":
		if err := a.installAURApplication(ctx, am, au, application); err != nil {
			return err
		}
	case "flatpak":
		if err := fm.Install(ctx, name); err != nil {
			return err
		}
	}
	if err := a.configureServices(ctx, application); err != nil {
		return err
	}

	switch application.Declaration.Source {
	case "pacman":
		return a.markApplicationExplicit(ctx, am, application)
	case "aur":
		_, err := a.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "pacman", Args: []string{"-Qm", name}})
		return err
	case "flatpak":
		if err := fm.Verify(ctx, name); err != nil {
			return fmt.Errorf("verify Flatpak application: %w", err)
		}
	}
	return nil
}

func (a Runtime) markApplicationExplicit(ctx context.Context, am arch.Manager, application plan.Application) error {
	if application.Declaration.Source != "pacman" && application.Declaration.Source != "aur" {
		return nil
	}
	name := application.Declaration.Identifier
	var query string
	switch application.Declaration.Source {
	case "pacman":
		match, err := archrepo.InstalledMatch(ctx, a.Runner, application.Package.Repository+"/"+name)
		if err != nil {
			return fmt.Errorf("application source changed after planning; rerun ops: %w", err)
		}
		if !match {
			return errors.New("application no longer matches authenticated official contents; rerun ops")
		}
	case "aur":
		query = "-Qm"
	}
	if query != "" {
		if _, err := a.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "pacman", Args: []string{query, name}}); err != nil {
			return fmt.Errorf("application source changed after planning; rerun ops: expected %s package: %w", application.Declaration.Source, err)
		}
	}
	if err := a.beginMutation(ctx); err != nil {
		return err
	}
	if err := am.MarkExplicit(ctx, []string{name}); err != nil {
		return fmt.Errorf("preserve explicit install reason: %w", err)
	}
	if _, err := a.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "pacman", Args: []string{"-Qe", name}}); err != nil {
		return fmt.Errorf("verify explicit install reason: %w", err)
	}
	return nil
}

func (a Runtime) installAURApplication(ctx context.Context, am arch.Manager, au aur.Manager, application plan.Application) error {
	if application.AURSource.Commit == "" || len(application.AUROutputs) == 0 {
		return errors.New("AUR application was not resolved to a pinned build plan")
	}
	if !stringPresent(application.AURExplicitOutputs, application.Declaration.Identifier) {
		return errors.New("AUR application target was not planned as explicit")
	}
	resolver := resolve.Resolver{Runner: a.Runner}
	afterReview := func() error {
		if err := a.beginMutation(ctx); err != nil {
			return err
		}
		keys := pgp.Manager{Runner: a.Runner}
		if len(application.AURSigningKeys) > 0 {
			for _, fingerprint := range application.AURSigningKeys {
				if err := keys.Import(ctx, fingerprint); err != nil {
					return fmt.Errorf("prepare AUR signing key %s: %w", fingerprint, err)
				}
			}
		}
		for _, fingerprint := range application.AURSource.Metadata.ValidPGPKeys {
			present, err := keys.Has(ctx, fingerprint)
			if err != nil {
				return fmt.Errorf("verify AUR signing key %s: %w", fingerprint, err)
			}
			if !present {
				return fmt.Errorf("AUR signing key %s is no longer available; rerun ops", fingerprint)
			}
		}
		missing := make(map[string]bool)
		installable := make(map[string]bool, len(application.AURPackages))
		for _, pkg := range application.AURPackages {
			installable[pkg.Repository+"/"+pkg.Name] = true
		}
		for _, planned := range application.AURDependencies {
			current, err := resolver.OfficialDependency(ctx, planned.Requirement)
			if err != nil {
				return fmt.Errorf("revalidate AUR dependency %q: %w", planned.Requirement, err)
			}
			if err := a.revalidateOfficialBinding(ctx, current, planned); err != nil {
				return err
			}
			if current.Satisfied {
				continue
			}
			if planned.Satisfied {
				return errors.New("AUR installed dependency changed after planning; rerun ops")
			}
			for _, packageName := range current.Packages {
				if !installable[packageName] {
					return errors.New("AUR dependency transaction changed after planning; rerun ops")
				}
				missing[packageName] = true
			}
		}
		var packages, explicitPackages []string
		for _, pkg := range application.AURPackages {
			if !missing[pkg.Repository+"/"+pkg.Name] {
				continue
			}
			if pkg.Update || pkg.Repair {
				evidence, err := archrepo.InspectInstalled(ctx, a.Runner, pkg.Repository+"/"+pkg.Name)
				if err != nil {
					return err
				}
				if evidence.Ready() {
					continue
				}
				if !pkg.Repair || evidence.Action() != archrepo.Repair {
					return fmt.Errorf("%s: %s; reconcile full upgrade policy", pkg.Name, evidence.Description())
				}
			}
			packages = append(packages, pkg.Repository+"/"+pkg.Name)

			if pkg.AsExplicit {
				explicitPackages = append(explicitPackages, pkg.Name)
			}
		}
		transaction, err := resolver.OfficialTransaction(ctx, packages)
		if err != nil {
			return fmt.Errorf("revalidate concrete AUR dependency transaction: %w", err)
		}
		if !packageSubset(transaction, packages) {
			return errors.New("AUR dependency transaction changed after planning; rerun ops")
		}
		if len(packages) > 0 {
			a.progress("Installing build dependencies...")
		}
		if err := am.Install(ctx, packages, true); err != nil {
			return fmt.Errorf("install AUR build dependencies: %w", err)
		}
		if len(explicitPackages) > 0 {
			if err := am.MarkExplicit(ctx, explicitPackages); err != nil {
				return fmt.Errorf("preserve explicit AUR dependency install reason: %w", err)
			}
			for _, name := range explicitPackages {
				if _, err := a.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "pacman", Args: []string{"-Qe", name}}); err != nil {
					return fmt.Errorf("verify explicit AUR dependency install reason %q: %w", name, err)
				}
			}
		}
		for _, planned := range application.AURDependencies {
			current, err := resolver.OfficialDependency(ctx, planned.Requirement)
			if err != nil {
				return fmt.Errorf("verify AUR dependency %q: %w", planned.Requirement, err)
			}
			if !current.Satisfied || current.Provider != planned.Provider {
				return fmt.Errorf("AUR dependency %q is not satisfied after the planned installation", planned.Requirement)
			}
			if err := a.revalidateOfficialBinding(ctx, current, planned); err != nil {
				return err
			}
		}
		a.progress("Building " + application.Declaration.Identifier + "...")
		return nil
	}
	install := func(buildDir string, artifacts []string) error {
		a.progress("Installing " + application.Declaration.Identifier + "...")
		return am.InstallArtifacts(ctx, buildDir, artifacts, application.AUROutputs, application.AURExplicitOutputs)
	}
	return au.Build(ctx, application.AURSource, application.Declaration.Identifier, application.AUROutputs, afterReview, install)
}

// Pacman's print transaction shrinks as earlier approved work installs shared
// dependencies. Every omitted member must still match its planned repository;
// additions and provider/repository substitutions require a new plan.
func (a Runtime) revalidateOfficialBinding(ctx context.Context, current, planned plan.OfficialDependency) error {
	if current.Provider != planned.Provider || !packageSubset(current.Packages, planned.Packages) {
		return errors.New("AUR dependency provider or repository changed after planning; rerun ops")
	}
	for _, target := range planned.Packages {
		if stringPresent(current.Packages, target) {
			continue
		}
		match, err := archrepo.InstalledMatch(ctx, a.Runner, target)
		if err != nil {
			return fmt.Errorf("revalidate satisfied AUR transaction member %s: %w", target, err)
		}
		if !match {
			return fmt.Errorf("AUR transaction member %s no longer matches authenticated official contents; rerun ops", target)
		}
	}
	return nil
}

func stringPresent(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (a Runtime) configureApplication(ctx context.Context, am arch.Manager, application plan.Application) error {
	if err := a.markApplicationExplicit(ctx, am, application); err != nil {
		return err
	}
	return a.configureServices(ctx, application)
}

func (a Runtime) configureServices(ctx context.Context, application plan.Application) error {
	if len(application.Services) > 0 {
		a.progress("Configuring services for " + application.Declaration.Identifier + "...")
	}
	for _, service := range application.Services {
		if err := a.beginMutation(ctx); err != nil {
			return err
		}
		if _, err := a.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: []string{"-n", "systemctl", "enable", "--now", service}}); err != nil {
			return err
		}
		for _, check := range []struct{ arg, want string }{{"is-enabled", "enabled"}, {"is-active", "active"}} {
			result, err := a.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "systemctl", Args: []string{check.arg, service}})
			if err != nil {
				return fmt.Errorf("verify required service %s: %w", service, err)
			}
			if strings.TrimSpace(result.Stdout) != check.want {
				return fmt.Errorf("required service %s is not %s after configuration", service, check.want)
			}
		}
	}
	return nil
}
