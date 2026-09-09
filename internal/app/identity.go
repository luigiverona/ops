package app

import (
	"context"
	"errors"
	"fmt"
	gitops "github.com/luigiverona/ops/internal/git"
	githubops "github.com/luigiverona/ops/internal/github"
	"github.com/luigiverona/ops/internal/plan"
	sshops "github.com/luigiverona/ops/internal/ssh"
	"github.com/luigiverona/ops/internal/ui"
	"path/filepath"
)

func (a Runtime) configureGit(ctx context.Context, terminal ui.UI) (string, error) {
	m := gitops.Manager{Runner: a.Runner}
	current := m.Inspect(ctx)
	if gitops.ValidName(current.Name) && gitops.ValidEmail(current.Email) {
		return "ready", nil
	}
	name, email := current.Name, current.Email
	a.progress("Git identity")
	var err error
	if !gitops.ValidName(name) {
		name, err = terminal.Ask(ctx, "Git name:")
		if err != nil {
			return "failed", err
		}
	}
	if !gitops.ValidEmail(email) {
		email, err = terminal.Ask(ctx, "Git email:")
		if err != nil {
			return "failed", err
		}
	}
	if err := a.beginMutation(ctx); err != nil {
		return "failed", err
	}
	if err := m.SetMissing(ctx, current, name, email); err != nil {
		return "failed", err
	}
	return "ready", nil
}

func (a Runtime) configureSSH(ctx context.Context, _ ui.UI, p plan.Plan) (string, *sshops.Identity, []issue, error) {
	m := sshops.Manager{Home: a.Home, Runner: a.Runner, HTTP: a.SSHHTTP, MetadataURL: a.SSHMetadataURL}
	identities, err := m.Discover(ctx)
	if err != nil {
		return "failed", nil, nil, fmt.Errorf("unsafe SSH state: %w", err)
	}
	var managed *sshops.Identity
	for _, identity := range identities {
		if identity.PrivatePath == filepath.Join(a.Home, ".ssh", "ops") {
			if identity.PublicPath == filepath.Join(a.Home, ".ssh", "ops.pub") {
				copy := identity
				managed = &copy
			}
		}
	}
	// Unrelated local identities are preserved; setup never deletes user keys.
	if p.CreateSSHIdentity && managed == nil {
		if err := a.beginMutation(ctx); err != nil {
			return "failed", nil, nil, err
		}
		a.progress("Creating SSH key...")
		identity, err := m.EnsureIdentity(ctx)
		if err != nil {
			return "failed", nil, []issue{*setupIssue("SSH", err)}, nil
		}
		managed = &identity
	}
	if managed == nil {
		return "failed", nil, nil, errors.New("managed SSH identity changed after planning")
	}
	if p.ReviewSSHAgent || p.LoadSSHAgent {
		agentKeys, available, err := m.AgentIdentities(ctx)
		if err != nil {
			return "failed", managed, []issue{*setupIssue("ssh-agent", err)}, nil
		}
		loaded := false
		if available {
			for _, key := range agentKeys {
				if key.Fingerprint == managed.Fingerprint {
					loaded = true
					break
				}
			}
			if p.LoadSSHAgent && !loaded {
				if err := a.beginMutation(ctx); err != nil {
					return "failed", managed, nil, err
				}
				a.progress("Loading SSH key...")
				if err := m.Load(ctx, managed.PrivatePath); err != nil {
					return "failed", managed, []issue{*setupIssue("ssh-agent", err)}, nil
				}
			}
		}
	}
	if p.ConfigureSSH {
		if err := a.beginMutation(ctx); err != nil {
			return "failed", managed, nil, err
		}
		a.progress("Configuring SSH...")
		if err := m.ConfigureGitHub(ctx); err != nil {
			return "failed", managed, nil, fmt.Errorf("unsafe required SSH configuration: %w", err)
		}
	}
	return "ready", managed, nil, nil
}

func (a Runtime) managedSSHIdentity(ctx context.Context) (*sshops.Identity, error) {
	identities, err := (sshops.Manager{Home: a.Home, Runner: a.Runner}).Discover(ctx)
	if err != nil {
		return nil, err
	}
	privatePath := filepath.Join(a.Home, ".ssh", "ops")
	for _, identity := range identities {
		if identity.PrivatePath == privatePath && identity.PublicPath == privatePath+".pub" {
			copy := identity
			return &copy, nil
		}
	}
	return nil, errors.New("managed SSH identity is unavailable")
}

func (a Runtime) configureGitHub(ctx context.Context, _ ui.UI, managed *sshops.Identity, p plan.Plan) (string, []issue) {
	if managed == nil {
		return "skipped", nil
	}
	m := githubops.Manager{Runner: a.Runner}
	lateAuthenticated := false
	if p.AuthenticateGitHub {
		lateAuthenticated = m.Authenticated(ctx)
		if !lateAuthenticated {
			if err := a.beginMutation(ctx); err != nil {
				return "failed", nil
			}
			a.progress("Signing in to GitHub...")
			if err := m.Login(ctx); err != nil {
				return "failed", []issue{*setupIssue("GitHub authentication", err)}
			}
		}
	}
	refreshedSSHKeyScope := false
	refreshSSHKeyScope := func() error {
		if err := a.beginMutation(ctx); err != nil {
			return err
		}
		a.progress("Authorizing GitHub SSH key access...")
		return m.RefreshSSHKeyScope(ctx)
	}
	if p.RefreshGitHubSSHKeyScope {
		if err := refreshSSHKeyScope(); err != nil {
			return "failed", []issue{*setupIssue("GitHub authorization", err)}
		}
		refreshedSSHKeyScope = true
	}
	keys, err := m.Keys(ctx)
	if err != nil && lateAuthenticated && !refreshedSSHKeyScope && githubops.IsSSHKeyScopeError(err) {
		if refreshErr := refreshSSHKeyScope(); refreshErr != nil {
			return "failed", []issue{*setupIssue("GitHub authorization", refreshErr)}
		}
		refreshedSSHKeyScope = true
		keys, err = m.Keys(ctx)
	}
	if err != nil {
		return "failed", []issue{*setupIssue("GitHub SSH keys", err)}
	}
	registered := false
	unrelated := 0
	for _, key := range keys {
		if key.Fingerprint == managed.Fingerprint {
			registered = true
			continue
		}
		unrelated++
	}
	// Existing account keys are never removed by workstation setup.
	added := false
	if p.ConfigureGitHubKey && !registered {
		if err := a.beginMutation(ctx); err != nil {
			return "failed", nil
		}
		added, err = m.AddManaged(ctx, managed.PublicPath)
		if err != nil {
			return "failed", []issue{*setupIssue("GitHub SSH key", err)}
		}
	}
	if err := m.VerifySSH(ctx); err != nil {
		return "failed", []issue{*setupIssue("GitHub SSH verification", err)}
	}
	if !added {
		return "ready", nil
	}
	fmt.Fprintln(a.Out, "GitHub SSH keys")
	if unrelated == 1 {
		fmt.Fprintln(a.Out, "  1 existing key preserved")
	} else if unrelated > 1 {
		fmt.Fprintf(a.Out, "  %d existing keys preserved\n", unrelated)
	}
	fmt.Fprintln(a.Out, "  This workstation added")
	return "ready", nil
}
