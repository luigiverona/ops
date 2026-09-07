package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/luigiverona/ops/internal/arch"
	"github.com/luigiverona/ops/internal/aur"
	"github.com/luigiverona/ops/internal/flatpak"
	"github.com/luigiverona/ops/internal/pgp"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/resolve"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
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
	if a.presentation == nil {
		a.presentation = &presentation{}
	}
	name := application.Declaration.Identifier
	a.showProgress(name, actionInstall, string(application.Declaration.Source))
	switch application.Declaration.Source {
	case "pacman":
		if err := am.Install(ctx, []string{name}, false); err != nil {
			return err
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
		if _, err := a.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Qn", name}}); err != nil {
			return err
		}
		return a.markApplicationExplicit(ctx, am, application)
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

func (a Runtime) markApplicationExplicit(ctx context.Context, am arch.Manager, application plan.Application) error {
	if application.Declaration.Source != "pacman" && application.Declaration.Source != "aur" {
		return nil
	}
	name := application.Declaration.Identifier
	var query string
	switch application.Declaration.Source {
	case "pacman":
		query = "-Qn"
	case "aur":
		query = "-Qm"
	}
	if _, err := a.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{query, name}}); err != nil {
		return fmt.Errorf("application source changed after planning; rerun ops: expected %s package: %w", application.Declaration.Source, err)
	}
	a.showProgress(name, actionConfigure, "pacman install reason")
	if err := am.MarkExplicit(ctx, []string{name}); err != nil {
		return fmt.Errorf("preserve explicit install reason: %w", err)
	}
	if _, err := a.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Qe", name}}); err != nil {
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
		keys := pgp.Manager{Runner: a.Runner}
		if len(application.AURSigningKeys) > 0 {
			rows := make([]ui.TableRow, 0, len(application.AURSigningKeys))
			for _, fingerprint := range application.AURSigningKeys {
				rows = append(rows, ui.TableRow{Item: application.Declaration.Identifier + " -> " + fingerprint, Action: actionConfigure, Detail: "AUR signing key"})
			}
			a.showProgressRows(rows)
			a.showExternal("gpg keyserver", "retrieve exact AUR signing keys")
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
			installable[pkg.Name] = true
		}
		for _, planned := range application.AURDependencies {
			current, err := resolver.OfficialDependency(ctx, planned.Requirement)
			if err != nil {
				return fmt.Errorf("revalidate AUR dependency %q: %w", planned.Requirement, err)
			}
			if current.Satisfied {
				continue
			}
			if planned.Satisfied || current.Provider != planned.Provider || !packageSubset(current.Packages, planned.Packages) {
				return errors.New("AUR dependency provider changed after planning; rerun ops")
			}
			for _, packageName := range current.Packages {
				if !installable[packageName] {
					return errors.New("AUR dependency transaction changed after planning; rerun ops")
				}
				missing[packageName] = true
			}
		}
		var packages, explicitPackages []string
		var progress []ui.TableRow
		for _, pkg := range application.AURPackages {
			if !missing[pkg.Name] {
				continue
			}
			packages = append(packages, pkg.Name)
			if pkg.AsExplicit {
				explicitPackages = append(explicitPackages, pkg.Name)
			}
			progress = append(progress, ui.TableRow{Item: application.Declaration.Identifier + " -> " + pkg.Name, Action: actionInstall, Detail: buildPackageDetail(pkg)})
		}
		transaction, err := resolver.OfficialTransaction(ctx, packages)
		if err != nil {
			return fmt.Errorf("revalidate concrete AUR dependency transaction: %w", err)
		}
		if !packageSubset(transaction, packages) {
			return errors.New("AUR dependency transaction changed after planning; rerun ops")
		}
		if len(progress) > 0 {
			a.showProgressRows(progress)
		}
		if err := am.Install(ctx, packages, true); err != nil {
			return fmt.Errorf("install AUR build dependencies: %w", err)
		}
		if len(explicitPackages) > 0 {
			if err := am.MarkExplicit(ctx, explicitPackages); err != nil {
				return fmt.Errorf("preserve explicit AUR dependency install reason: %w", err)
			}
			for _, name := range explicitPackages {
				if _, err := a.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Qe", name}}); err != nil {
					return fmt.Errorf("verify explicit AUR dependency install reason %q: %w", name, err)
				}
			}
		}
		for _, planned := range application.AURDependencies {
			current, err := resolver.OfficialDependency(ctx, planned.Requirement)
			if err != nil {
				return fmt.Errorf("verify AUR dependency %q: %w", planned.Requirement, err)
			}
			if !current.Satisfied {
				return fmt.Errorf("AUR dependency %q is not satisfied after the planned installation", planned.Requirement)
			}
		}
		a.showProgress(application.Declaration.Identifier, actionInstall, "AUR build")
		return nil
	}
	install := func(buildDir string, artifacts []string) error {
		a.showProgress(application.Declaration.Identifier, actionInstall, "local package")
		return am.InstallArtifacts(ctx, buildDir, artifacts, application.AUROutputs, application.AURExplicitOutputs)
	}
	return au.Build(ctx, application.AURSource, application.Declaration.Identifier, application.AUROutputs, afterReview, install)
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
	for _, service := range application.Services {
		a.showProgress(application.Declaration.Identifier+" -> "+service, actionEnable, "systemd")
		if _, err := a.Runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"-n", "systemctl", "enable", "--now", service}}); err != nil {
			return err
		}
		for _, check := range []struct{ arg, want string }{{"is-enabled", "enabled"}, {"is-active", "active"}} {
			result, err := a.Runner.Run(ctx, run.Spec{Name: "systemctl", Args: []string{check.arg, service}})
			if err != nil || strings.TrimSpace(result.Stdout) != check.want {
				return fmt.Errorf("required service %s is not %s after configuration", service, check.want)
			}
		}
	}
	return nil
}
