package plan

import (
	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/config"
	"strings"
	"testing"
)

func TestCoreVersionDriftPlansUpdateNotRepair(t *testing.T) {
	for _, name := range []string{"git", "openssh", "github-cli", "flatpak"} {
		for _, auth := range []archrepo.Authenticity{archrepo.VerifiedOfficial, archrepo.AuthenticityInconclusive, archrepo.InvalidOfficialContent} {
			t.Run(name+"/"+string(auth), func(t *testing.T) {
				state := convergedState()
				state.Installed[name] = true
				state.OfficialStates = map[string]archrepo.InstalledState{name: {Target: archrepo.Prerequisite(name), Authenticity: auth, Currency: archrepo.OlderThanCurrent}}
				cfg := config.Config{}
				if name == "flatpak" {
					cfg.Applications = []config.Application{{Source: config.Flatpak, Identifier: "org.example.App"}}
				}
				p := Build(cfg, state, nil)
				if !p.FullUpgrade {
					t.Fatal("missing update")
				}
				repair := auth == archrepo.InvalidOfficialContent
				if (len(p.CorePackages) > 0) != repair {
					t.Fatalf("repair classification %+v", p)
				}
				for _, status := range p.Core {
					if !repair && strings.Contains(status, "repair") {
						t.Fatal(status)
					}
				}
			})
		}
	}
}

func TestDeclaredPackageVersionDriftAndSourceLag(t *testing.T) {
	declaration := config.Application{Source: config.Pacman, Identifier: "firefox"}
	cfg := config.Config{Applications: []config.Application{declaration}}
	for _, currency := range []archrepo.Currency{archrepo.OlderThanCurrent, archrepo.NewerThanCurrent, archrepo.Current} {
		state := convergedState()
		state.Installed["firefox"], state.Explicit["firefox"] = true, true
		state.OfficialStates = map[string]archrepo.InstalledState{"firefox": {Target: "extra/firefox", Authenticity: archrepo.VerifiedOfficial, Currency: currency}}
		state.OfficialMatches["firefox"] = "extra/firefox"
		p := Build(cfg, state, nil)
		if p.FullUpgrade != (currency == archrepo.OlderThanCurrent) {
			t.Fatalf("%+v", p)
		}
		if strings.Contains(p.Applications[0].Cause, "repair") {
			t.Fatal(p.Applications[0])
		}
		if currency == archrepo.NewerThanCurrent {
			if p.Applications[0].State != Unavailable {
				t.Fatal(p.Applications[0])
			}
			for _, target := range p.UpgradeTargets {
				if target == "extra/firefox" {
					t.Fatal("automatic downgrade target")
				}
			}
		}
	}
}

func TestExistingOfficialStatePreservesMultilibAndFinalRepair(t *testing.T) {
	declaration := config.Application{Source: config.Pacman, Identifier: "lib32-example"}
	cfg := config.Config{Applications: []config.Application{declaration}}
	for _, auth := range []archrepo.Authenticity{archrepo.VerifiedOfficial, archrepo.InvalidOfficialContent} {
		state := convergedState()
		state.Multilib = false
		state.Installed[declaration.Identifier] = true
		state.OfficialStates = map[string]archrepo.InstalledState{declaration.Identifier: {
			Target: "multilib/lib32-example", Authenticity: auth, Currency: archrepo.OlderThanCurrent,
		}}
		// Final inspection has no resolution facts; its typed evidence must still
		// preserve the package's source, action and diagnostic.
		p := Build(cfg, state, nil)
		app := p.Applications[0]
		if !p.EnableMultilib || !p.FullUpgrade || app.State != Install || app.OfficialState == nil {
			t.Fatalf("lost existing package work: %+v", p)
		}
		if strings.Contains(app.Cause, "repair") != (auth == archrepo.InvalidOfficialContent) {
			t.Fatalf("incorrect final diagnosis: %+v", app)
		}
	}
}
