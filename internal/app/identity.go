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

func (a Runtime) configureGit(ctx context.Context, terminal ui.UI) (string, *issue) {
	if a.presentation == nil {
		a.presentation = &presentation{}
	}
	m := gitops.Manager{Runner: a.Runner}
	current := m.Inspect(ctx)
	if gitops.ValidName(current.Name) && gitops.ValidEmail(current.Email) {
		return "ready", nil
	}
	name, email := current.Name, current.Email
	a.showProgress("git", actionConfigure, "user identity; input required")
	var err error
	if !gitops.ValidName(name) {
		name, err = terminal.Ask("Git user.name:")
		if err != nil {
			return "failed", setupIssue("Git", err)
		}
	}
	if !gitops.ValidEmail(email) {
		email, err = terminal.Ask("Git user.email:")
		if err != nil {
			return "failed", setupIssue("Git", err)
		}
	}
	if err := m.SetMissing(ctx, current, name, email); err != nil {
		return "failed", setupIssue("Git", err)
	}
	return "ready", nil
}

func (a Runtime) configureSSH(ctx context.Context, terminal ui.UI, p plan.Plan) (string, *sshops.Identity, []issue, error) {
	if a.presentation == nil {
		a.presentation = &presentation{}
	}
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
	if p.ReviewSSHIdentities {
		var unrelated []sshops.Identity
		for _, identity := range identities {
			if identity.PrivatePath == filepath.Join(a.Home, ".ssh", "ops") {
				continue
			}
			unrelated = append(unrelated, identity)
		}
		if len(unrelated) > 0 {
			a.showProgress("SSH identities", actionReview, "unrelated local keys")
		}
		for i, identity := range unrelated {
			a.showReview(fmt.Sprintf("SSH identity %d/%d", i+1, len(unrelated)), []ui.Field{{Name: "path", Value: identity.PrivatePath}, {Name: "fingerprint", Value: identity.Fingerprint}})
			keep, err := terminal.Confirm("Keep this identity?", true)
			if err != nil {
				return "failed", managed, nil, err
			}
			if keep {
				continue
			}
			a.showReview("Files selected for permanent deletion", []ui.Field{{Name: "path", Value: identity.PrivatePath}})
			if identity.PublicPath != "" {
				fmt.Fprint(a.Out, ui.RenderFields([]ui.Field{{Name: "public path", Value: identity.PublicPath}}))
			}
			remove, err := terminal.Confirm("Permanently delete this identity?", false)
			if err != nil {
				return "failed", managed, nil, err
			}
			if remove {
				if err := m.Delete(ctx, identity); err != nil {
					return "failed", managed, []issue{*setupIssue("SSH identity deletion", err)}, nil
				}
			}
		}
	}
	if p.CreateSSHIdentity && managed == nil {
		a.showProgress("SSH identity", actionConfigure, "managed Ed25519 key")
		a.showExternal("ssh-keygen", "SSH key passphrase prompt")
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
			unrelated := make([]sshops.AgentIdentity, 0, len(agentKeys))
			for _, key := range agentKeys {
				if key.Fingerprint == managed.Fingerprint {
					loaded = true
					continue
				}
				unrelated = append(unrelated, key)
			}
			if p.ReviewSSHAgent && len(unrelated) > 0 {
				a.showProgress("ssh-agent identities", actionReview, "unrelated loaded keys")
			}
			for i, key := range unrelated {
				if !p.ReviewSSHAgent {
					break
				}
				a.showReview(fmt.Sprintf("ssh-agent identity %d/%d", i+1, len(unrelated)), []ui.Field{{Name: "fingerprint", Value: key.Fingerprint}})
				keep, err := terminal.Confirm("Keep this identity loaded in ssh-agent?", true)
				if err != nil {
					return "failed", managed, nil, err
				}
				if !keep {
					if err := m.Unload(ctx, key); err != nil {
						return "failed", managed, []issue{*setupIssue("ssh-agent", err)}, nil
					}
				}
			}
			if p.LoadSSHAgent && !loaded {
				a.showProgress("ssh-agent managed key", actionConfigure, "load identity")
				a.showExternal("ssh-add", "SSH key passphrase prompt")
				if err := m.Load(ctx, managed.PrivatePath); err != nil {
					return "failed", managed, []issue{*setupIssue("ssh-agent", err)}, nil
				}
			}
		}
	}
	if p.ConfigureSSH {
		a.showProgress("github.com SSH configuration", actionConfigure, "managed identity and host trust")
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

func (a Runtime) configureGitHub(ctx context.Context, terminal ui.UI, managed *sshops.Identity, p plan.Plan) (string, []issue) {
	if a.presentation == nil {
		a.presentation = &presentation{}
	}
	if managed == nil {
		return "skipped", nil
	}
	m := githubops.Manager{Runner: a.Runner}
	lateAuthenticated := false
	if p.AuthenticateGitHub {
		lateAuthenticated = m.Authenticated(ctx)
		if !lateAuthenticated {
			a.showProgress("github", actionAuthenticate, "CLI login; SSH-key permission")
			a.showExternal("gh auth login", "GitHub device authentication")
			if err := m.Login(ctx); err != nil {
				return "failed", []issue{*setupIssue("GitHub authentication", err)}
			}
		}
	}
	refreshedSSHKeyScope := false
	refreshSSHKeyScope := func() error {
		a.showProgress("github", actionAuthenticate, "add SSH-key management permission")
		a.showExternal("gh auth refresh", "GitHub SSH-key authorization")
		return m.RefreshSSHKeyScope(ctx)
	}
	if p.RefreshGitHubSSHKeyScope {
		if err := refreshSSHKeyScope(); err != nil {
			return "failed", []issue{*setupIssue("GitHub authorization", err)}
		}
		refreshedSSHKeyScope = true
	}
	if p.GitHubKeyStateUnknown {
		a.showProgress("GitHub SSH keys", actionInspect, "reconcile account keys")
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
	var unrelated []githubops.Key
	for _, key := range keys {
		if key.Fingerprint == managed.Fingerprint {
			registered = true
			continue
		}
		unrelated = append(unrelated, key)
	}
	if p.ReviewGitHubKeys && len(unrelated) > 0 {
		a.showProgress("GitHub SSH keys", actionReview, "account keys")
		for i, key := range unrelated {
			a.showReview(fmt.Sprintf("GitHub key %d/%d", i+1, len(unrelated)), []ui.Field{{Name: "title", Value: key.Title}, {Name: "fingerprint", Value: key.Fingerprint}})
			keep, err := terminal.Confirm("Keep this key?", true)
			if err != nil {
				return "failed", []issue{*setupIssue("GitHub SSH keys", err)}
			}
			if keep {
				continue
			}
			remove, err := terminal.Confirm("Remove this key from GitHub?", false)
			if err != nil {
				return "failed", []issue{*setupIssue("GitHub SSH keys", err)}
			}
			if remove {
				if err := m.Delete(ctx, key); err != nil {
					return "failed", []issue{*setupIssue("GitHub SSH key deletion", err)}
				}
			}
		}
	}
	if p.ConfigureGitHubKey && !registered {
		a.showProgress("GitHub SSH key", actionConfigure, "managed key")
		if _, err := m.AddManaged(ctx, managed.PublicPath); err != nil {
			return "failed", []issue{*setupIssue("GitHub SSH key", err)}
		}
	}
	if err := m.VerifySSH(ctx); err != nil {
		return "failed", []issue{*setupIssue("GitHub SSH verification", err)}
	}
	return "ready", nil
}
