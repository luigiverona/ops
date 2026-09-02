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
	gitops "github.com/luigiverona/ops/internal/git"
	githubops "github.com/luigiverona/ops/internal/github"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	sshops "github.com/luigiverona/ops/internal/ssh"
)

// Workstation performs read-only command and file inspection.
type Workstation struct {
	Runner         run.Runner
	PacmanConf     string
	Home           string
	SSHHTTP        *http.Client
	SSHMetadataURL string
}

// State returns the observable state. Optional tools being absent is state, not an inspection failure.
func (w Workstation) State(ctx context.Context) (plan.State, error) {
	state := plan.State{
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
	if _, err := w.Runner.Run(ctx, run.Spec{Name: "paru", Args: []string{"--version"}}); err == nil {
		state.Paru = true
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
		state.Multilib, _ = arch.MultilibEnabled(data)
	}
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
	var managedFingerprint string
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
				managedFingerprint = identity.Fingerprint
			}
			continue
		}
		state.UnrelatedSSHIdentities++
	}
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
		err = inspectErr
		if err != nil {
			return state, fmt.Errorf("inspect GitHub SSH configuration: %w", err)
		}
	}
	if state.Installed["openssh"] && (!state.ManagedSSHIdentity || !state.SSHConfigurationReady) {
		agentIdentities, available, err := sshManager.AgentIdentities(ctx)
		if err != nil {
			return state, fmt.Errorf("inspect ssh-agent identities: %w", err)
		}
		state.SSHAgentAvailable = available
		for _, identity := range agentIdentities {
			if managedFingerprint != "" && identity.Fingerprint == managedFingerprint {
				state.ManagedSSHAgentIdentity = true
				continue
			}
			state.UnrelatedSSHAgentIdentities++
		}
	}

	githubManager := githubops.Manager{Runner: w.Runner}
	state.GitHubAuth = githubManager.Authenticated(ctx)
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
		if managedFingerprint != "" {
			state.ManagedGitHubKeyKnown = true
			for _, key := range keys {
				if key.Fingerprint == managedFingerprint {
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
