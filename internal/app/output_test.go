package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
)

func TestShowPlanRealV100Workstation(t *testing.T) {
	p := realWorkstationPlan(t)
	var output bytes.Buffer
	Runtime{Out: &output}.showPlan(p)
	want := "Plan\n" +
		"\nSystem\n" +
		"  full system upgrade  upgrade  pacman\n" +
		"\nCore\n" +
		"  paru  install  AUR bootstrap; review required\n" +
		"\nApplications\n" +
		"  bitwarden              install  pacman\n" +
		"  com.tutanota.Tutanota  install  flatpak\n" +
		"\nIdentity and access\n" +
		"  SSH identities                review        unrelated local keys\n" +
		"  github.com SSH configuration  configure     managed identity and host trust\n" +
		"  github                        authenticate  CLI login\n" +
		"  GitHub SSH keys               review        existing keys after login, if present\n" +
		"  GitHub SSH key                configure     register after login, if missing\n" +
		"\nUnchanged\n" +
		"  5 core components\n" +
		"  6 applications\n"
	if output.String() != want {
		t.Fatalf("plan mismatch\n--- got ---\n%s--- want ---\n%s", output.String(), want)
	}
	for _, readyIdentifier := range []string{"librewolf-bin", "mullvad-browser-bin", "mullvad-vpn", "discord", "spotify-launcher", "steam"} {
		if strings.Contains(output.String(), readyIdentifier) {
			t.Fatalf("ready application %q dominated the change plan", readyIdentifier)
		}
	}
}

func TestShowPlanApplicationSourcesAndLongIdentifiers(t *testing.T) {
	p := plan.Plan{Applications: []plan.Application{
		{Declaration: config.Application{Identifier: "bitwarden", Source: "pacman"}, State: "install"},
		{Declaration: config.Application{Identifier: "com.tutanota.Tutanota", Source: "flatpak"}, State: "install"},
		{Declaration: config.Application{Identifier: "an-extremely-long-application-identifier", Source: "aur"}, State: "install"},
	}}
	var output bytes.Buffer
	Runtime{Out: &output}.showPlan(p)
	want := "Plan\n\nApplications\n" +
		"  bitwarden                                 install  pacman\n" +
		"  com.tutanota.Tutanota                     install  flatpak\n" +
		"  an-extremely-long-application-identifier  install  aur; review required\n"
	if output.String() != want {
		t.Fatalf("plan mismatch\n--- got ---\n%s--- want ---\n%s", output.String(), want)
	}
}

func TestShowPlanDefersRemoteKeyComparisonUntilIdentityExists(t *testing.T) {
	p := plan.Plan{
		CreateSSHIdentity: true, ReviewGitHubKeys: true, ConfigureGitHubKey: true,
		GitHubKeyStateUnknown: true, GitHubKeyAfterIdentity: true,
	}
	var output bytes.Buffer
	Runtime{Out: &output}.showPlan(p)
	if !strings.Contains(output.String(), "GitHub SSH key   configure  register after identity creation, if missing") ||
		strings.Contains(output.String(), "register after login") {
		t.Fatalf("future managed fingerprint was presented as known:\n%s", output.String())
	}
}

func TestShowPlanRendersDependenciesAndServicesWithOwners(t *testing.T) {
	state := plan.State{
		Installed: map[string]bool{"git": true, "openssh": true, "github-cli": true, "flatpak": true, "base-devel": true},
		Foreign:   map[string]bool{}, Flatpaks: map[string]bool{}, Paru: true, Flathub: true, Multilib: true,
		GitName: "User", GitEmail: "user@example.com", ManagedSSHIdentity: true, SSHConfigurationReady: true,
		SSHHostKeyFreshness: plan.SSHHostKeyFreshnessCurrent,
		GitHubAuth:          true, GitHubKeysKnown: true, ManagedGitHubKeyKnown: true, ManagedGitHubKey: true,
	}
	resolver := outputResolver{pacman: map[string]plan.Package{
		"mullvad-vpn": {Name: "mullvad-vpn", Repository: "extra", Optional: []string{"libfoo: integration", "aaa-helper: helper"}},
		"libfoo":      {Name: "libfoo", Repository: "extra"},
		"aaa-helper":  {Name: "aaa-helper", Repository: "extra"},
	}}
	p, err := plan.Build(context.Background(), config.Config{Version: 1, Applications: []config.Application{{Identifier: "mullvad-vpn", Source: "pacman"}}}, state, resolver)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	Runtime{Out: &output}.showPlan(p)
	want := "Plan\n\nSystem\n" +
		"  full system upgrade  upgrade  pacman\n" +
		"\nApplications\n" +
		"  mullvad-vpn -> aaa-helper              install  pacman\n" +
		"  mullvad-vpn -> libfoo                  install  pacman\n" +
		"  mullvad-vpn                            install  pacman\n" +
		"  mullvad-vpn -> mullvad-daemon.service  enable   systemd\n" +
		"\nUnchanged\n  7 core components\n"
	if output.String() != want {
		t.Fatalf("plan mismatch\n--- got ---\n%s--- want ---\n%s", output.String(), want)
	}
}

func TestShowPlanOnlyChangeAndAllReady(t *testing.T) {
	tests := []struct {
		name string
		plan plan.Plan
		want string
	}{
		{
			name: "one configuration change",
			plan: plan.Plan{Core: readyCore(), ConfigureGit: true},
			want: "Plan\n\nIdentity and access\n  git  configure  user identity; input required\n\nUnchanged\n  7 core components\n",
		},
		{
			name: "all ready",
			plan: plan.Plan{Core: readyCore(), Applications: readyApplications()},
			want: "Plan\n\nNo changes\n  workstation is already ready\n\nUnchanged\n  7 core components\n  8 applications\n",
		},
		{
			name: "no mutations with unavailable host-key freshness",
			plan: plan.Plan{Core: readyCore(), Applications: readyApplications(), SSHHostKeyFreshness: plan.SSHHostKeyFreshnessUnavailable},
			want: "Plan\n\nNo changes planned\n\nChecks\n  GitHub SSH host-key freshness  unavailable  retry later\n\nUnchanged\n  7 core components\n  8 applications\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			Runtime{Out: &output}.showPlan(test.plan)
			if output.String() != test.want {
				t.Fatalf("plan mismatch\n--- got ---\n%s--- want ---\n%s", output.String(), test.want)
			}
		})
	}
}

func TestShowPlanIsDeterministic(t *testing.T) {
	firstCore := map[string]string{"git": "ready", "ssh": "required", "github": "ready", "aur": "required", "paru": "required", "flatpak": "ready", "flathub": "ready"}
	secondCore := map[string]string{"flathub": "ready", "flatpak": "ready", "paru": "required", "aur": "required", "github": "ready", "ssh": "required", "git": "ready"}
	first := plan.Plan{Core: firstCore, CorePackages: []string{"openssh", "base-devel", "git"}, FullUpgrade: true}
	second := plan.Plan{Core: secondCore, CorePackages: []string{"git", "openssh", "base-devel"}, FullUpgrade: true}
	var firstOutput, secondOutput bytes.Buffer
	Runtime{Out: &firstOutput}.showPlan(first)
	Runtime{Out: &secondOutput}.showPlan(second)
	if firstOutput.String() != secondOutput.String() {
		t.Fatalf("map or input order changed output\n--- first ---\n%s--- second ---\n%s", firstOutput.String(), secondOutput.String())
	}
}

func TestProgressAndFailureRenderingRemainStructured(t *testing.T) {
	var progress bytes.Buffer
	Runtime{Out: &progress}.showProgress("com.tutanota.Tutanota", actionInstall, "flatpak")
	if want := "\nProgress\n  com.tutanota.Tutanota  install  flatpak\n"; progress.String() != want {
		t.Fatalf("progress = %q, want %q", progress.String(), want)
	}

	var failure bytes.Buffer
	code := (Runtime{Err: &failure}).fatal(errors.New("example failure"))
	want := "Failed\n\nops\n  cause           example failure\n  impact          workstation preparation could not safely continue\n  action          resolve the error and run ops again\n\nWorkstation preparation stopped.\n"
	if code != Fatal || failure.String() != want {
		t.Fatalf("failure code=%d\n--- got ---\n%s--- want ---\n%s", code, failure.String(), want)
	}
}

func TestPlanActionVocabularyIsCompleteAndClosed(t *testing.T) {
	p := plan.Plan{
		EnableMultilib: true, FullUpgrade: true, BootstrapParu: true,
		Applications: []plan.Application{{
			Declaration: config.Application{Identifier: "example", Source: "pacman"}, State: "install",
			Services: []string{"example.service"},
		}},
		ConfigureGit: true, ReviewSSHIdentities: true, AuthenticateGitHub: true,
	}
	want := map[string]bool{
		actionInstall: true, actionConfigure: true, actionUpgrade: true,
		actionEnable: true, actionAuthenticate: true, actionReview: true,
	}
	got := make(map[string]bool)
	for _, section := range planSections(p) {
		if section.Diagnostic {
			continue
		}
		for _, row := range section.Rows {
			got[row.Action] = true
			if row.Action == "ready" || row.Action == "required" || row.Action == "configuration required" {
				t.Fatalf("state label used as action: %#v", row)
			}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("actions=%v, want=%v", got, want)
	}
	for action := range want {
		if !got[action] {
			t.Fatalf("missing action %q in %v", action, got)
		}
	}
}

func realWorkstationPlan(t *testing.T) plan.Plan {
	t.Helper()
	applications := []config.Application{
		{Identifier: "librewolf-bin", Source: "aur"},
		{Identifier: "mullvad-browser-bin", Source: "aur"},
		{Identifier: "mullvad-vpn", Source: "pacman"},
		{Identifier: "bitwarden", Source: "pacman"},
		{Identifier: "com.tutanota.Tutanota", Source: "flatpak"},
		{Identifier: "discord", Source: "pacman"},
		{Identifier: "spotify-launcher", Source: "pacman"},
		{Identifier: "steam", Source: "pacman"},
	}
	state := plan.State{
		Installed: map[string]bool{
			"git": true, "openssh": true, "github-cli": true, "flatpak": true, "base-devel": true,
			"librewolf-bin": true, "mullvad-browser-bin": true, "mullvad-vpn": true,
			"discord": true, "spotify-launcher": true, "steam": true,
		},
		Foreign:  map[string]bool{"librewolf-bin": true, "mullvad-browser-bin": true},
		Flatpaks: map[string]bool{}, Flathub: true, Multilib: true,
		GitName: "User", GitEmail: "user@example.com",
		ManagedSSHIdentity: true, UnrelatedSSHIdentities: 1,
	}
	resolver := outputResolver{
		pacman:  map[string]plan.Package{"bitwarden": {Name: "bitwarden", Repository: "extra"}},
		flatpak: map[string]bool{"com.tutanota.Tutanota": true},
	}
	p, err := plan.Build(context.Background(), config.Config{Version: 1, Applications: applications}, state, resolver)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type outputResolver struct {
	pacman  map[string]plan.Package
	aur     map[string]plan.Package
	flatpak map[string]bool
}

func (r outputResolver) Pacman(_ context.Context, name string) (plan.Package, bool, error) {
	pkg, ok := r.pacman[name]
	return pkg, ok, nil
}

func (r outputResolver) AUR(_ context.Context, name string) (plan.Package, bool, error) {
	pkg, ok := r.aur[name]
	return pkg, ok, nil
}

func (r outputResolver) Flatpak(_ context.Context, name string) (bool, error) {
	return r.flatpak[name], nil
}

func readyCore() map[string]string {
	core := make(map[string]string, len(plan.CoreOrder))
	for _, component := range plan.CoreOrder {
		core[component] = "ready"
	}
	return core
}

func readyApplications() []plan.Application {
	identifiers := []string{"librewolf-bin", "mullvad-browser-bin", "mullvad-vpn", "bitwarden", "com.tutanota.Tutanota", "discord", "spotify-launcher", "steam"}
	applications := make([]plan.Application, 0, len(identifiers))
	for _, identifier := range identifiers {
		applications = append(applications, plan.Application{Declaration: config.Application{Identifier: identifier, Source: "pacman"}, State: "ready"})
	}
	return applications
}
