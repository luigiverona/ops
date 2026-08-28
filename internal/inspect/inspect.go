// Package inspect discovers actual workstation state without mutation.
package inspect

import (
	"context"
	"os"
	"strings"

	"github.com/luigiverona/ops/internal/arch"
	gitops "github.com/luigiverona/ops/internal/git"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	sshops "github.com/luigiverona/ops/internal/ssh"
)

// Workstation performs read-only command and file inspection.
type Workstation struct {
	Runner     run.Runner
	PacmanConf string
	Home       string
}

// State returns the observable state. Optional tools being absent is state, not an inspection failure.
func (w Workstation) State(ctx context.Context) (plan.State, error) {
	state := plan.State{Installed: map[string]bool{}, Foreign: map[string]bool{}, Flatpaks: map[string]bool{}}
	if result, err := w.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Qq"}}); err == nil {
		addLines(state.Installed, result.Stdout)
	} else {
		return state, err
	}
	if result, err := w.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Qqm"}}); err == nil {
		addLines(state.Foreign, result.Stdout)
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
	sshManager := sshops.Manager{Home: w.Home, Runner: w.Runner}
	if identities, err := sshManager.Discover(ctx); err == nil {
		for _, identity := range identities {
			if identity.PrivatePath == w.Home+"/.ssh/ops" && identity.PublicPath == w.Home+"/.ssh/ops.pub" {
				state.SSHReady = sshManager.GitHubConfigured(ctx)
			}
		}
	}
	if _, err := w.Runner.Run(ctx, run.Spec{Name: "gh", Args: []string{"auth", "status", "--hostname", "github.com", "--active"}}); err == nil {
		state.GitHubAuth = true
	}
	state.GitHubSSHAccess = state.SSHReady
	return state, nil
}

func addLines(target map[string]bool, output string) {
	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			target[line] = true
		}
	}
}
