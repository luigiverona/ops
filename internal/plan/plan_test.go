package plan

import (
	"context"
	"testing"

	"github.com/luigiverona/ops/internal/config"
)

type fakeResolver struct {
	pacman map[string]Package
	aur    map[string]Package
	flat   map[string]bool
}

func (f fakeResolver) Pacman(_ context.Context, name string) (Package, bool, error) {
	p, ok := f.pacman[name]
	return p, ok, nil
}
func (f fakeResolver) AUR(_ context.Context, name string) (Package, bool, error) {
	p, ok := f.aur[name]
	return p, ok, nil
}
func (f fakeResolver) Flatpak(_ context.Context, name string) (bool, error) { return f.flat[name], nil }

func emptyState() State {
	return State{Installed: map[string]bool{}, Foreign: map[string]bool{}, Flatpaks: map[string]bool{}}
}

func readyState() State {
	s := emptyState()
	for _, pkg := range []string{"git", "openssh", "github-cli", "flatpak", "base-devel"} {
		s.Installed[pkg] = true
	}
	s.Paru, s.Flathub, s.Multilib = true, true, true
	s.GitName, s.GitEmail = "n", "e"
	s.ManagedSSHIdentity, s.SSHConfigurationReady = true, true
	s.SSHHostKeyFreshness = SSHHostKeyFreshnessCurrent
	s.GitHubAuth, s.GitHubKeysKnown, s.ManagedGitHubKeyKnown, s.ManagedGitHubKey = true, true, true, true
	return s
}

func TestFreshAndReadyPlanning(t *testing.T) {
	fresh, err := Build(context.Background(), config.Config{Version: 1}, emptyState(), fakeResolver{})
	if err != nil || !fresh.FullUpgrade || !fresh.BootstrapParu || !fresh.AddFlathub || !fresh.ConfigureGit || !fresh.CreateSSHIdentity || !fresh.ConfigureSSH || !fresh.AuthenticateGitHub || !fresh.ReviewGitHubKeys || !fresh.ConfigureGitHubKey || !fresh.GitHubKeyStateUnknown {
		t.Fatalf("fresh = %#v, %v", fresh, err)
	}
	ready, err := Build(context.Background(), config.Config{Version: 1}, readyState(), fakeResolver{})
	if err != nil || ready.FullUpgrade || ready.BootstrapParu || ready.AddFlathub || len(ready.CorePackages) != 0 || ready.ConfigureGit || ready.CreateSSHIdentity || ready.ReviewSSHIdentities || ready.ReviewSSHAgent || ready.LoadSSHAgent || ready.ConfigureSSH || ready.AuthenticateGitHub || ready.ReviewGitHubKeys || ready.ConfigureGitHubKey || ready.GitHubKeyStateUnknown {
		t.Fatalf("ready = %#v, %v", ready, err)
	}
}

func TestSSHAndGitHubActionsComeFromInspectedState(t *testing.T) {
	tests := []struct {
		name  string
		state State
		check func(t *testing.T, p Plan)
	}{
		{
			name: "setup includes explicit local and agent review",
			state: func() State {
				s := readyState()
				s.SSHConfigurationReady = false
				s.UnrelatedSSHIdentities = 1
				s.SSHAgentAvailable = true
				s.ManagedSSHAgentIdentity = false
				s.UnrelatedSSHAgentIdentities = 2
				return s
			}(),
			check: func(t *testing.T, p Plan) {
				if !p.ConfigureSSH || !p.ReviewSSHIdentities || !p.ReviewSSHAgent || !p.LoadSSHAgent {
					t.Fatalf("SSH actions = %#v", p)
				}
			},
		},
		{
			name: "authenticated missing managed key",
			state: func() State {
				s := readyState()
				s.ManagedGitHubKey = false
				s.OtherGitHubKeys = 2
				return s
			}(),
			check: func(t *testing.T, p Plan) {
				if p.AuthenticateGitHub || !p.ReviewGitHubKeys || !p.ConfigureGitHubKey || p.GitHubKeyStateUnknown {
					t.Fatalf("GitHub actions = %#v", p)
				}
			},
		},
		{
			name: "unauthenticated key state remains explicitly unknown",
			state: func() State {
				s := readyState()
				s.GitHubAuth, s.GitHubKeysKnown, s.ManagedGitHubKeyKnown, s.ManagedGitHubKey = false, false, false, false
				return s
			}(),
			check: func(t *testing.T, p Plan) {
				if !p.AuthenticateGitHub || !p.ReviewGitHubKeys || !p.ConfigureGitHubKey || !p.GitHubKeyStateUnknown {
					t.Fatalf("GitHub actions = %#v", p)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, err := Build(context.Background(), config.Config{Version: 1}, test.state, fakeResolver{})
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, p)
		})
	}
}

func TestMissingManagedIdentityKeepsRemoteManagedKeyComparisonUnknown(t *testing.T) {
	state := readyState()
	state.ManagedSSHIdentity = false
	state.SSHConfigurationReady = false
	state.SSHHostKeyFreshness = SSHHostKeyFreshnessUnknown
	state.GitHubKeysKnown = true
	state.ManagedGitHubKeyKnown = false
	state.ManagedGitHubKey = false
	p, err := Build(context.Background(), config.Config{Version: 1}, state, fakeResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.CreateSSHIdentity || !p.GitHubKeyStateUnknown || !p.GitHubKeyAfterIdentity || !p.ConfigureGitHubKey {
		t.Fatalf("future managed key was modeled as known: %#v", p)
	}
	if p.SSHHostKeyFreshness != SSHHostKeyFreshnessUnknown {
		t.Fatalf("missing local configuration freshness=%q", p.SSHHostKeyFreshness)
	}
}

func TestSSHHostKeyFreshnessPlansOnlyKnownStaleness(t *testing.T) {
	tests := []struct {
		name      string
		freshness SSHHostKeyFreshness
		configure bool
		sshStatus string
	}{
		{name: "current", freshness: SSHHostKeyFreshnessCurrent, sshStatus: "ready"},
		{name: "stale", freshness: SSHHostKeyFreshnessStale, configure: true, sshStatus: "configuration required"},
		{name: "unavailable", freshness: SSHHostKeyFreshnessUnavailable, sshStatus: "unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := readyState()
			state.SSHHostKeyFreshness = test.freshness
			state.UnrelatedSSHIdentities = 2
			state.SSHAgentAvailable = true
			state.UnrelatedSSHAgentIdentities = 3
			p, err := Build(context.Background(), config.Config{Version: 1}, state, fakeResolver{})
			if err != nil {
				t.Fatal(err)
			}
			if p.ConfigureSSH != test.configure || p.SSHStatus != test.sshStatus || p.ReviewSSHIdentities || p.ReviewSSHAgent || p.LoadSSHAgent {
				t.Fatalf("plan=%#v", p)
			}
		})
	}
}

func TestReadyManagedAccessDoesNotForceRepeatedIdentityReview(t *testing.T) {
	state := readyState()
	state.UnrelatedSSHIdentities = 2
	state.SSHAgentAvailable = true
	state.UnrelatedSSHAgentIdentities = 3
	state.OtherGitHubKeys = 4
	p, err := Build(context.Background(), config.Config{Version: 1}, state, fakeResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if p.ReviewSSHIdentities || p.ReviewSSHAgent || p.LoadSSHAgent || p.ReviewGitHubKeys || p.ConfigureGitHubKey {
		t.Fatalf("ready access forced repeated review: %#v", p)
	}
}

func TestAppPrerequisitesSourcesAndMultilib(t *testing.T) {
	cfg := config.Config{Version: 1, Applications: []config.Application{
		{Category: "game", Source: "pacman", Identifier: "steam"},
		{Category: "browser", Source: "aur", Identifier: "browser-bin"},
		{Category: "mail", Source: "flatpak", Identifier: "org.example.Mail"},
	}}
	r := fakeResolver{pacman: map[string]Package{
		"steam":     {Name: "steam", Repository: "multilib", Optional: []string{"gamescope: optional compositor", "choice-a: alternative", "choice-b: alternative"}},
		"gamescope": {Name: "gamescope", Repository: "extra"},
		"choice-a":  {Name: "choice-a", Conflicts: []string{"choice-b"}},
		"choice-b":  {Name: "choice-b"},
	}, aur: map[string]Package{"browser-bin": {Name: "browser-bin"}}, flat: map[string]bool{"org.example.Mail": true}}
	s := readyState()
	s.Multilib = false
	p, err := Build(context.Background(), cfg, s, r)
	if err != nil {
		t.Fatal(err)
	}
	if !p.EnableMultilib || len(p.Applications) != 3 || len(p.Applications[0].Dependencies) != 1 || p.Applications[0].Dependencies[0].Identifier != "gamescope" {
		t.Fatalf("unexpected plan: %#v", p)
	}
}

func TestDuplicateApplicationActionsArePlannedOnce(t *testing.T) {
	cfg := config.Config{Version: 1, Applications: []config.Application{
		{Source: "pacman", Identifier: "first"},
		{Source: "pacman", Identifier: "second"},
		{Source: "pacman", Identifier: "shared"},
	}}
	r := fakeResolver{pacman: map[string]Package{
		"first":  {Name: "first", Optional: []string{"shared", "helper"}},
		"second": {Name: "second", Optional: []string{"helper"}},
		"shared": {Name: "shared"},
		"helper": {Name: "helper"},
	}}
	p, err := Build(context.Background(), cfg, readyState(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Applications[0].Dependencies) != 1 || p.Applications[0].Dependencies[0].Identifier != "helper" {
		t.Fatalf("first dependencies=%#v", p.Applications[0].Dependencies)
	}
	if len(p.Applications[1].Dependencies) != 0 || len(p.Applications[2].Dependencies) != 0 {
		t.Fatalf("duplicate dependencies remain: %#v", p.Applications)
	}
}

func TestIdempotencyAndNoRemovalPlanning(t *testing.T) {
	s := readyState()
	s.Installed["firefox"], s.Installed["old-app"] = true, true
	cfg := config.Config{Version: 1, Applications: []config.Application{{Category: "browser", Source: "pacman", Identifier: "firefox"}}}
	p, err := Build(context.Background(), cfg, s, fakeResolver{})
	if err != nil || p.Applications[0].State != "ready" || len(p.CorePackages) != 0 {
		t.Fatalf("second run not idempotent: %#v, %v", p, err)
	}
	// old-app is absent from intent and no removal operation exists in Plan.
}

func TestUnresolved(t *testing.T) {
	cfg := config.Config{Version: 1, Applications: []config.Application{{Category: "browser", Source: "pacman", Identifier: "missing"}}}
	p, err := Build(context.Background(), cfg, readyState(), fakeResolver{})
	if err != nil || p.Applications[0].State != "unresolved" {
		t.Fatalf("plan = %#v, %v", p, err)
	}
}
