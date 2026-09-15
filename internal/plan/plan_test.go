package plan

import (
	"reflect"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/flatpak"
)

func convergedState() State {
	return State{OfficialMatches: map[string]string{"git": "extra/git", "openssh": "core/openssh", "github-cli": "extra/github-cli", "flatpak": "extra/flatpak", "base-devel": "extra/base-devel"}, Installed: map[string]bool{"git": true, "openssh": true, "github-cli": true}, Explicit: map[string]bool{}, Foreign: map[string]bool{}, Flatpaks: map[string]string{}, GitName: "User", GitEmail: "user@example.com", ManagedSSHIdentity: true, SSHConfigurationReady: true, SSHHostKeyFreshness: SSHHostKeyFreshnessCurrent, GitHubAuth: true, GitHubKeysKnown: true, ManagedGitHubKeyKnown: true, ManagedGitHubKey: true}
}

func TestBuildCapabilityDependencies(t *testing.T) {
	for _, tt := range []struct {
		name, config     string
		minimal          bool
		packages         []string
		flathub, upgrade bool
	}{
		{"pristine", "version=2", true, []string{"git", "github-cli", "openssh"}, false, true},
		{"no apps", "version=2", false, nil, false, false},
		{"pacman only", "version=2\npacman=[\"librewolf\"]", false, nil, false, true},
		{"AUR only", "version=2\naur=[\"example\"]", false, nil, false, true},
		{"Flatpak only", "version=2\nflatpak=[\"org.example.App\"]", false, []string{"flatpak"}, true, true},
		{"mixed", "version=2\npacman=[\"librewolf\"]\naur=[\"example\"]\nflatpak=[\"org.example.App\"]", false, []string{"flatpak"}, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Parse([]byte(tt.config))
			if err != nil {
				t.Fatal(err)
			}
			state := convergedState()
			if tt.minimal {
				state = State{}
			}
			facts := Facts{}
			for _, decl := range cfg.Applications {
				facts[decl] = Application{Declaration: decl, State: "install"}
			}
			p := Build(cfg, state, facts)
			if !reflect.DeepEqual(p.CorePackages, tt.packages) || p.AddFlathub != tt.flathub || p.FullUpgrade != tt.upgrade {
				t.Fatalf("plan=%+v", p)
			}
			if _, ok := p.Core["paru"]; ok {
				t.Fatal("implicit AUR helper")
			}
			if !reflect.DeepEqual(p, Build(cfg, state, facts)) {
				t.Fatal("nondeterministic plan")
			}
		})
	}
}

func TestBuildConvergenceAndServiceDrift(t *testing.T) {
	cfg, _ := config.Parse([]byte("version=2\npacman=[\"mullvad-vpn\"]\naur=[\"example\"]\nflatpak=[\"org.example.App\"]"))
	state := convergedState()
	for _, name := range []string{"mullvad-vpn", "example", "flatpak"} {
		state.Installed[name] = true
		state.Explicit[name] = true
	}
	state.OfficialMatches["mullvad-vpn"] = "extra/mullvad-vpn"
	state.Foreign["example"] = true
	state.Flatpaks["org.example.App"] = "flathub"
	state.Flathub = flatpak.Remote{SourceTrusted: true, Name: "flathub", URL: flatpak.FlathubRepositoryURL, Enabled: true}
	state.Services = map[string]bool{"mullvad-daemon.service": true}
	p := Build(cfg, state, nil)
	if p.FullUpgrade || p.AddFlathub || len(p.CorePackages) > 0 {
		t.Fatalf("not converged: %+v", p)
	}
	for _, app := range p.Applications {
		if app.State != "ready" {
			t.Fatalf("app=%+v", app)
		}
	}
	state.Services["mullvad-daemon.service"] = false
	p = Build(cfg, state, nil)
	if p.Applications[0].State != "configure" || len(p.Applications[0].Services) != 1 || p.FullUpgrade {
		t.Fatalf("service drift=%+v", p)
	}
	delete(state.Installed, "git")
	p = Build(cfg, state, nil)
	if !reflect.DeepEqual(p.CorePackages, []string{"git"}) {
		t.Fatalf("partial state=%+v", p)
	}
}

func TestBuildMultilibAndFactsRemainUnchanged(t *testing.T) {
	cfg, _ := config.Parse([]byte("version=2\npacman=[\"steam\"]"))
	decl := cfg.Applications[0]
	facts := Facts{decl: {Declaration: decl, State: "install", EnableMultilib: true}}
	state := convergedState()
	p := Build(cfg, state, facts)
	if !p.EnableMultilib || !p.FullUpgrade || facts[decl].State != "install" {
		t.Fatalf("plan=%+v facts=%+v", p, facts)
	}
	state.Multilib = true
	if Build(cfg, state, facts).EnableMultilib {
		t.Fatal("enabled repository replanned")
	}
}
