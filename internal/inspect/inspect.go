// Package inspect discovers actual workstation state without mutation.
package inspect

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/luigiverona/ops/internal/arch"
	"github.com/luigiverona/ops/internal/config"
	gitops "github.com/luigiverona/ops/internal/git"
	githubops "github.com/luigiverona/ops/internal/github"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	sshops "github.com/luigiverona/ops/internal/ssh"
)

// Workstation performs read-only command and file inspection.
type Workstation struct {
	Applications   []config.Application
	Runner         run.Runner
	PacmanConf     string
	Home           string
	SSHHTTP        *http.Client
	SSHMetadataURL string
}

// Local inspects package and user state without network calls or mutations.
// Optional tools being absent is state, not an inspection failure.
func (w Workstation) Local(ctx context.Context) (plan.State, error) {
	state := plan.State{
		Services:  map[string]bool{},
		Installed: map[string]bool{}, Explicit: map[string]bool{}, Foreign: map[string]bool{}, Flatpaks: map[string]bool{},
		SSHHostKeyFreshness: plan.SSHHostKeyFreshnessUnknown,
	}
	if result, err := w.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Qq"}}); err == nil {
		addLines(state.Installed, result.Stdout)
	} else {
		return state, err
	}
	if result, err := w.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Qqm"}}); err == nil {
		addLines(state.Foreign, result.Stdout)
	}
	if result, err := w.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Qeq"}}); err == nil {
		addLines(state.Explicit, result.Stdout)
	} else {
		return state, err
	}
	if state.Installed["flatpak"] {
		if result, err := w.Runner.Run(ctx, run.Spec{Name: "flatpak", Args: []string{"list", "--user", "--app", "--columns=application"}}); err == nil {
			addLines(state.Flatpaks, result.Stdout)
		}
		if result, err := w.Runner.Run(ctx, run.Spec{Name: "flatpak", Args: []string{"remotes", "--user", "--columns=name"}}); err == nil {
			for _, line := range strings.Fields(result.Stdout) {
				state.Flathub = state.Flathub || line == "flathub"
			}
		}
	}
	path := w.PacmanConf
	if path == "" {
		path = "/etc/pacman.conf"
	}
	if data, err := os.ReadFile(path); err == nil {
		if err := arch.ValidatePacmanConf(data); err != nil {
			return state, err
		}
		state.Multilib, _ = arch.MultilibEnabled(data)
	} else {
		return state, fmt.Errorf("read pacman configuration: %w", err)
	}
	if state.Installed["git"] {
		if result, err := w.Runner.Run(ctx, run.Spec{Name: "git", Args: []string{"config", "--global", "--get", "user.name"}}); err == nil {
			state.GitName = strings.TrimSpace(result.Stdout)
		}
		if result, err := w.Runner.Run(ctx, run.Spec{Name: "git", Args: []string{"config", "--global", "--get", "user.email"}}); err == nil {
			state.GitEmail = strings.TrimSpace(result.Stdout)
		}
		if !gitops.ValidName(state.GitName) {
			state.GitName = ""
		}
		if !gitops.ValidEmail(state.GitEmail) {
			state.GitEmail = ""
		}
	}
	sshManager := sshops.Manager{Home: w.Home, Runner: w.Runner, HTTP: w.SSHHTTP, MetadataURL: w.SSHMetadataURL}
	identities, err := sshManager.Discover(ctx)
	if err != nil {
		return state, fmt.Errorf("inspect SSH identities: %w", err)
	}
	managedPrivate := filepath.Join(w.Home, ".ssh", "ops")
	managedPublic := managedPrivate + ".pub"
	for _, identity := range identities {
		if identity.PrivatePath == managedPrivate {
			if identity.PublicPath == managedPublic {
				state.ManagedSSHIdentity = true
				state.ManagedSSHFingerprint = identity.Fingerprint
			}
			continue
		}
		state.UnrelatedSSHIdentities++
	}
	state.SSHConfigurationReady = state.ManagedSSHIdentity && sshManager.GitHubConfigured(ctx)
	if state.Installed["openssh"] && (!state.ManagedSSHIdentity || !state.SSHConfigurationReady) {
		agentIdentities, available, err := sshManager.AgentIdentities(ctx)
		if err != nil {
			return state, fmt.Errorf("inspect ssh-agent identities: %w", err)
		}
		state.SSHAgentAvailable = available
		for _, identity := range agentIdentities {
			if state.ManagedSSHFingerprint != "" && identity.Fingerprint == state.ManagedSSHFingerprint {
				state.ManagedSSHAgentIdentity = true
				continue
			}
			state.UnrelatedSSHAgentIdentities++
		}
	}

	for _, app := range w.Applications {
		service := plan.RequiredService(app)
		if service == "" || !plan.IsInstalled(app, state) {
			continue
		}
		enabled, e1 := w.Runner.Run(ctx, run.Spec{Name: "systemctl", Args: []string{"is-enabled", service}})
		active, e2 := w.Runner.Run(ctx, run.Spec{Name: "systemctl", Args: []string{"is-active", service}})
		state.Services[service] = e1 == nil && e2 == nil && strings.TrimSpace(enabled.Stdout) == "enabled" && strings.TrimSpace(active.Stdout) == "active"
	}

	return state, nil
}

// External resolves authenticated account state and host-key freshness read-only.
// It never logs in, authorizes sudo, or writes user files.
func (w Workstation) External(ctx context.Context, state plan.State) (plan.State, error) {
	sshManager := sshops.Manager{Home: w.Home, Runner: w.Runner, HTTP: w.SSHHTTP, MetadataURL: w.SSHMetadataURL}
	if state.ManagedSSHIdentity {
		configuration, inspectErr := sshManager.InspectGitHubConfiguration(ctx)
		state.SSHConfigurationReady = configuration.LocalReady
		switch configuration.Freshness {
		case sshops.HostKeyFreshnessUnknown:
			state.SSHHostKeyFreshness = plan.SSHHostKeyFreshnessUnknown
		case sshops.HostKeyFreshnessCurrent:
			state.SSHHostKeyFreshness = plan.SSHHostKeyFreshnessCurrent
		case sshops.HostKeyFreshnessStale:
			state.SSHHostKeyFreshness = plan.SSHHostKeyFreshnessStale
		case sshops.HostKeyFreshnessUnavailable:
			state.SSHHostKeyFreshness = plan.SSHHostKeyFreshnessUnavailable
		}
		if inspectErr != nil {
			return state, fmt.Errorf("inspect GitHub SSH configuration: %w", inspectErr)
		}
	}
	githubManager := githubops.Manager{Runner: w.Runner}
	state.GitHubAuth = state.Installed["github-cli"] && githubManager.Authenticated(ctx)
	if state.GitHubAuth {
		keys, err := githubManager.Keys(ctx)
		if err != nil {
			if githubops.IsSSHKeyScopeError(err) {
				state.GitHubSSHKeyScopeInsufficient = true
				return state, nil
			}
			return state, fmt.Errorf("inspect GitHub SSH keys: %w", err)
		}
		state.GitHubKeysKnown = true
		if state.ManagedSSHFingerprint != "" {
			state.ManagedGitHubKeyKnown = true
			for _, key := range keys {
				if key.Fingerprint == state.ManagedSSHFingerprint {
					state.ManagedGitHubKey = true
					continue
				}
				state.OtherGitHubKeys++
			}
		}
	}
	return state, nil
}

func addLines(target map[string]bool, output string) {
	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			target[line] = true
		}
	}
}

// State runs both read-only observation phases in canonical order.
func (w Workstation) State(ctx context.Context) (plan.State, error) {
	state, err := w.Local(ctx)
	if err != nil {
		return state, err
	}
	return w.External(ctx, state)
}
