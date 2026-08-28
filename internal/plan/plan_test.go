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
	s.GitName, s.GitEmail, s.SSHReady, s.GitHubAuth, s.GitHubSSHAccess = "n", "e", true, true, true
	return s
}

func TestFreshAndReadyPlanning(t *testing.T) {
	fresh, err := Build(context.Background(), config.Config{Version: 1}, emptyState(), fakeResolver{})
	if err != nil || !fresh.FullUpgrade || !fresh.BootstrapParu || !fresh.AddFlathub {
		t.Fatalf("fresh = %#v, %v", fresh, err)
	}
	ready, err := Build(context.Background(), config.Config{Version: 1}, readyState(), fakeResolver{})
	if err != nil || ready.FullUpgrade || ready.BootstrapParu || ready.AddFlathub || len(ready.OfficialPackages) != 0 {
		t.Fatalf("ready = %#v, %v", ready, err)
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

func TestIdempotencyAndNoRemovalPlanning(t *testing.T) {
	s := readyState()
	s.Installed["firefox"], s.Installed["old-app"] = true, true
	cfg := config.Config{Version: 1, Applications: []config.Application{{Category: "browser", Source: "pacman", Identifier: "firefox"}}}
	p, err := Build(context.Background(), cfg, s, fakeResolver{})
	if err != nil || p.Applications[0].State != "ready" || len(p.OfficialPackages) != 0 {
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
