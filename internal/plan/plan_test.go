package plan

import (
	"context"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/config"
)

type fakeResolver struct {
	pacman map[string]Package
	aur    map[string]Package
	flat   map[string]bool
	source *AURSource
	deps   map[string]OfficialDependency
}

func (f fakeResolver) Pacman(_ context.Context, name string) (Package, bool, error) {
	p, ok := f.pacman[name]
	return p, ok, nil
}
func (f fakeResolver) AUR(_ context.Context, name string) (Package, bool, error) {
	p, ok := f.aur[name]
	return p, ok, nil
}
func (f fakeResolver) AURSource(_ context.Context, _ string) (AURSource, bool, error) {
	if f.source != nil {
		return *f.source, true, nil
	}
	return testParuSource(), true, nil
}
func (f fakeResolver) OfficialDependency(_ context.Context, requirement string) (OfficialDependency, error) {
	if dependency, ok := f.deps[requirement]; ok {
		return dependency, nil
	}
	return OfficialDependency{Requirement: requirement, Satisfied: true}, nil
}
func (f fakeResolver) CompareVersions(_ context.Context, _, _ string) (int, error) { return 0, nil }
func (f fakeResolver) Flatpak(_ context.Context, name string) (bool, error)        { return f.flat[name], nil }

func testParuSource() AURSource {
	return AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "paru", Version: "1.0.0-1", Packages: []aurmeta.Package{{Name: "paru"}},
	}}
}

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

func TestParuBootstrapPlansConcreteDependencyTransaction(t *testing.T) {
	source := AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "paru", Version: "2.1.0-2",
		Depends: []string{"git", "libalpm.so>=14"}, MakeDepends: []string{"cargo"}, CheckDepends: []string{"test-tool"},
		Packages: []aurmeta.Package{{Name: "paru", Depends: []string{"runtime-helper"}}},
	}}
	resolver := fakeResolver{source: &source, deps: map[string]OfficialDependency{
		"base-devel":     {Requirement: "base-devel", Provider: "base-devel", Packages: []string{"base-devel"}},
		"cargo":          {Requirement: "cargo", Provider: "rust", Packages: []string{"llvm-libs", "rust"}},
		"git":            {Requirement: "git", Satisfied: true},
		"libalpm.so>=14": {Requirement: "libalpm.so>=14", Satisfied: true},
		"runtime-helper": {Requirement: "runtime-helper", Satisfied: true},
		"test-tool":      {Requirement: "test-tool", Provider: "test-tool", Packages: []string{"test-tool"}},
	}}
	state := readyState()
	state.Paru = false
	state.Installed["base-devel"] = false
	p, err := Build(context.Background(), config.Config{Version: 1}, state, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if !p.BootstrapParu || strings.Join(p.ParuOutputs, ",") != "paru" {
		t.Fatalf("bootstrap outputs=%v enabled=%v", p.ParuOutputs, p.BootstrapParu)
	}
	var packages []string
	for _, pkg := range p.ParuPackages {
		packages = append(packages, pkg.Name)
	}
	if strings.Join(packages, ",") != "base-devel,llvm-libs,rust,test-tool" {
		t.Fatalf("bootstrap packages=%v", p.ParuPackages)
	}
	if contains(p.CorePackages, "base-devel") {
		t.Fatalf("base-devel escaped the post-review bootstrap boundary: %v", p.CorePackages)
	}
	if len(p.ParuDependencies) != 6 {
		t.Fatalf("dependency bindings=%#v", p.ParuDependencies)
	}
}

func TestParuBootstrapDeduplicatesConcretePackagesAndApplicationInstall(t *testing.T) {
	source := AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "paru", Version: "1-1", MakeDepends: []string{"cargo", "rustfmt"}, Packages: []aurmeta.Package{{Name: "paru"}},
	}}
	transaction := []string{"llvm-libs", "rust"}
	resolver := fakeResolver{
		source: &source,
		deps: map[string]OfficialDependency{
			"base-devel": {Requirement: "base-devel", Satisfied: true},
			"cargo":      {Requirement: "cargo", Provider: "rust", Packages: transaction},
			"rustfmt":    {Requirement: "rustfmt", Provider: "rust", Packages: transaction},
		},
		pacman: map[string]Package{"rust": {Name: "rust", Repository: "extra"}},
	}
	state := readyState()
	state.Paru = false
	p, err := Build(context.Background(), config.Config{Version: 1, Applications: []config.Application{{Source: "pacman", Identifier: "rust"}}}, state, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ParuPackages) != 2 || p.ParuPackages[0].Name != "llvm-libs" || p.ParuPackages[1].Name != "rust" || !p.ParuPackages[1].AsExplicit {
		t.Fatalf("deduplicated packages=%#v", p.ParuPackages)
	}
	if !p.Applications[0].CoveredByBootstrap || len(p.Applications[0].Dependencies) != 0 {
		t.Fatalf("application was not represented exactly once: %#v", p.Applications[0])
	}
}

func TestAURApplicationPinsSourceAndPlansOfficialBuildDependencies(t *testing.T) {
	source := AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "browser-bin", Version: "1-1", Depends: []string{"runtime"}, MakeDepends: []string{"builder"},
		Packages: []aurmeta.Package{{Name: "browser-bin"}},
	}}
	state := readyState()
	state.Installed["base-devel"] = true
	resolver := fakeResolver{
		aur:    map[string]Package{"browser-bin": {Name: "browser-bin", PackageBase: "browser-bin", Optional: []string{"feature>=1:2: optional integration"}}},
		pacman: map[string]Package{"feature": {Name: "feature", Repository: "extra"}},
		source: &source,
		deps: map[string]OfficialDependency{
			"runtime":      {Requirement: "runtime", Provider: "runtime", Packages: []string{"runtime", "runtime-libs"}},
			"builder":      {Requirement: "builder", Provider: "builder", Packages: []string{"builder"}},
			"feature>=1:2": {Requirement: "feature>=1:2", Provider: "feature", Packages: []string{"feature", "feature-libs"}},
			"base-devel":   {Requirement: "base-devel", Satisfied: true},
		},
	}
	p, err := Build(context.Background(), config.Config{Version: 1, Applications: []config.Application{{Source: "aur", Identifier: "browser-bin"}}}, state, resolver)
	if err != nil {
		t.Fatal(err)
	}
	app := p.Applications[0]
	if app.State != "install" || app.AURSource.Commit != source.Commit || strings.Join(app.AUROutputs, ",") != "browser-bin" {
		t.Fatalf("application=%#v", app)
	}
	if len(app.AURDependencies) != 4 || strings.Join([]string{app.AURPackages[0].Name, app.AURPackages[1].Name, app.AURPackages[2].Name, app.AURPackages[3].Name, app.AURPackages[4].Name}, ",") != "builder,feature,feature-libs,runtime,runtime-libs" {
		t.Fatalf("planned AUR dependency transaction=%#v", app.AURPackages)
	}
	if app.AURDependencies[2].Requirement != "feature>=1:2" || strings.Join(app.AURPackages[1].Purposes, ",") != "optional" {
		t.Fatalf("optional dependency was not bound into the planned transaction: %#v", app)
	}
}

func TestAURApplicationUnsupportedDependencyFailsClosed(t *testing.T) {
	source := AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "browser-bin", Version: "1-1", Depends: []string{"aur-only-helper"}, Packages: []aurmeta.Package{{Name: "browser-bin"}},
	}}
	state := readyState()
	resolver := fakeResolver{
		aur:    map[string]Package{"browser-bin": {Name: "browser-bin", PackageBase: "browser-bin"}},
		source: &source,
		deps: map[string]OfficialDependency{
			"base-devel":      {Requirement: "base-devel", Satisfied: true},
			"aur-only-helper": {Requirement: "aur-only-helper"},
		},
	}
	p, err := Build(context.Background(), config.Config{Version: 1, Applications: []config.Application{{Source: "aur", Identifier: "browser-bin"}}}, state, resolver)
	if err != nil {
		t.Fatal(err)
	}
	app := p.Applications[0]
	if app.State != "failed" || !strings.Contains(app.Cause, "missing provider transaction") || len(app.AURPackages) != 0 {
		t.Fatalf("unsupported AUR dependency was planned: %#v", app)
	}
}

func TestAURApplicationUnsupportedOptionalDependencyFailsClosed(t *testing.T) {
	state := readyState()
	resolver := fakeResolver{
		aur: map[string]Package{
			"browser-bin": {Name: "browser-bin", PackageBase: "browser-bin", Optional: []string{"aur-helper: optional integration"}},
			"aur-helper":  {Name: "aur-helper", PackageBase: "aur-helper"},
		},
	}
	p, err := Build(context.Background(), config.Config{Version: 1, Applications: []config.Application{{Source: "aur", Identifier: "browser-bin"}}}, state, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Applications) != 1 || p.Applications[0].State != "failed" || !strings.Contains(p.Applications[0].Cause, "cannot be resolved deterministically") {
		t.Fatalf("unsupported optional AUR dependency was not rejected: %#v", p.Applications)
	}
}

func TestInstalledAURApplicationConvergesWithoutAnotherBuildPlan(t *testing.T) {
	state := readyState()
	state.Installed["browser-bin"] = true
	state.Foreign["browser-bin"] = true
	p, err := Build(context.Background(), config.Config{Version: 1, Applications: []config.Application{{Source: "aur", Identifier: "browser-bin"}}}, state, fakeResolver{})
	if err != nil {
		t.Fatal(err)
	}
	app := p.Applications[0]
	if app.State != "ready" || app.AURSource.Commit != "" || len(app.AURPackages) != 0 {
		t.Fatalf("installed AUR application was planned again: %#v", app)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestUnresolved(t *testing.T) {
	cfg := config.Config{Version: 1, Applications: []config.Application{{Category: "browser", Source: "pacman", Identifier: "missing"}}}
	p, err := Build(context.Background(), cfg, readyState(), fakeResolver{})
	if err != nil || p.Applications[0].State != "unresolved" {
		t.Fatalf("plan = %#v, %v", p, err)
	}
}
