package plan

import (
	"reflect"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/flatpak"
)

func TestProvenanceReadinessAndRemediation(t *testing.T) {
	declaration := config.Application{Source: config.Flatpak, Identifier: "org.example.App"}
	cfg := config.Config{Applications: []config.Application{declaration}}
	for _, tc := range []struct {
		name, url, origin string
		enabled           bool
		want              ApplicationState
		add, enable       bool
	}{
		{"ready", flatpak.FlathubRepositoryURL, "flathub", true, Ready, false, false},
		{"disabled", flatpak.FlathubRepositoryURL, "flathub", false, Configure, false, true},
		{"wrong URL", "https://evil/", "flathub", true, Failed, false, false},
		{"wrong origin", flatpak.FlathubRepositoryURL, "other", true, Failed, false, false},
		{"missing", "", "", false, Install, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := convergedState()
			state.Installed["flatpak"] = true
			state.OfficialMatches["flatpak"] = "extra/flatpak"
			state.Flatpaks = map[string]string{declaration.Identifier: tc.origin}
			if tc.url != "" {
				state.Flathub = flatpak.Remote{SourceTrusted: true, Name: "flathub", URL: tc.url, Enabled: tc.enabled}
			}
			facts := Facts{declaration: {State: Install}}
			p := Build(cfg, state, facts)
			if p.Applications[0].State != tc.want || p.AddFlathub != tc.add || p.EnableFlathub != tc.enable {
				t.Fatalf("%+v", p)
			}
			if !reflect.DeepEqual(p, Build(cfg, state, facts)) {
				t.Fatal("impure plan")
			}
			final := Build(cfg, state, nil)
			if tc.want != Ready && final.Applications[0].State == Ready {
				t.Fatal("final name/ID-only state became healthy")
			}
		})
	}
}
func TestCustomNativeCannotSatisfyCoreOrDeclaration(t *testing.T) {
	state := convergedState()
	state.OfficialMatches = nil
	state.Installed["firefox"] = true
	app := config.Application{Source: config.Pacman, Identifier: "firefox"}
	if IsInstalled(app, state) {
		t.Fatal("native means official")
	}
	p := Build(config.Config{Applications: []config.Application{app}}, state, nil)
	if len(p.CorePackages) != 3 || p.Core["git"] == "ready" {
		t.Fatalf("custom prerequisites ready: %+v", p)
	}
	if !reflect.DeepEqual(p.UpgradeTargets, []string{"core/openssh", "extra/git", "extra/github-cli"}) {
		t.Fatalf("upgrade shadows not protected: %v", p.UpgradeTargets)
	}
}

func TestFlathubWithoutCompleteTrustEvidenceIsManual(t *testing.T) {
	app := config.Application{Source: config.Flatpak, Identifier: "org.example.App"}
	for _, enabled := range []bool{true, false} {
		state := convergedState()
		state.Flathub = flatpak.Remote{Name: "flathub", URL: flatpak.FlathubRepositoryURL, Enabled: enabled}
		p := Build(config.Config{Applications: []config.Application{app}}, state, Facts{app: {State: Install}})
		if p.AddFlathub || p.EnableFlathub || p.Applications[0].State != Failed {
			t.Fatalf("untrusted source planned for automatic correction: %+v", p)
		}
	}
}
