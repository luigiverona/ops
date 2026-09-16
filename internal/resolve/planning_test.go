package resolve

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/flatpak"
	"github.com/luigiverona/ops/internal/plan"
)

type fakeResolver struct {
	pacman   map[string]Package
	aur      map[string]Package
	flat     map[string]bool
	source   *AURSource
	deps     map[string]OfficialDependency
	pgp      map[string]bool
	pgpErr   error
	pgpCalls *int
}

func (f fakeResolver) Pacman(_ context.Context, name string) (Package, bool, error) {
	p, ok := f.pacman[name]
	if ok && p.Repository == "" {
		p.Repository = "extra"
	}
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
	return OfficialDependency{Requirement: requirement, Provider: "extra/" + requirement, Packages: []string{"extra/" + requirement}, Satisfied: true}, nil
}
func (f fakeResolver) UserPGPKey(_ context.Context, fingerprint string) (bool, error) {
	if f.pgpCalls != nil {
		*f.pgpCalls = *f.pgpCalls + 1
	}
	return f.pgp[fingerprint], f.pgpErr
}
func (f fakeResolver) CompareVersions(_ context.Context, _, _ string) (int, error) { return 0, nil }
func (f fakeResolver) Flatpak(_ context.Context, name string) (bool, error)        { return f.flat[name], nil }

func testParuSource() AURSource {
	return AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "paru", Version: "1.0.0-1", Packages: []aurmeta.Package{{Name: "paru"}},
	}}
}

func emptyState() State {
	return State{OfficialMatches: map[string]string{"git": "extra/git", "openssh": "core/openssh", "github-cli": "extra/github-cli", "flatpak": "extra/flatpak", "base-devel": "extra/base-devel"}, Installed: map[string]bool{}, Explicit: map[string]bool{}, Foreign: map[string]bool{}, Flatpaks: map[string]string{}}
}

func readyState() State {
	s := emptyState()
	for _, pkg := range []string{"git", "openssh", "github-cli", "flatpak", "base-devel"} {
		s.Installed[pkg] = true
		s.Explicit[pkg] = true
	}
	s.Flathub, s.Multilib = flatpak.Remote{Name: "flathub", URL: flatpak.FlathubRepositoryURL, Enabled: true}, true
	s.GitName, s.GitEmail = "n", "e"
	s.ManagedSSHIdentity, s.SSHConfigurationReady = true, true
	s.SSHHostKeyFreshness = SSHHostKeyFreshnessCurrent
	s.GitHubAuth, s.GitHubKeysKnown, s.ManagedGitHubKeyKnown, s.ManagedGitHubKey = true, true, true, true
	return s
}

func TestFreshAndReadyPlanning(t *testing.T) {
	fresh := resolveAndPlan(context.Background(), config.Config{Version: 2}, emptyState(), fakeResolver{})
	if !fresh.FullUpgrade || fresh.AddFlathub || !fresh.ConfigureGit || !fresh.CreateSSHIdentity || !fresh.ConfigureSSH || !fresh.AuthenticateGitHub || !fresh.ReviewGitHubKeys || !fresh.ConfigureGitHubKey || !fresh.GitHubKeyStateUnknown {
		t.Fatalf("fresh = %#v", fresh)
	}
	ready := resolveAndPlan(context.Background(), config.Config{Version: 2}, readyState(), fakeResolver{})
	if ready.FullUpgrade || ready.AddFlathub || len(ready.CorePackages) != 0 || ready.ConfigureGit || ready.CreateSSHIdentity || ready.ReviewSSHIdentities || ready.ReviewSSHAgent || ready.LoadSSHAgent || ready.ConfigureSSH || ready.AuthenticateGitHub || ready.ReviewGitHubKeys || ready.ConfigureGitHubKey || ready.GitHubKeyStateUnknown {
		t.Fatalf("ready = %#v", ready)
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
			p := resolveAndPlan(context.Background(), config.Config{Version: 2}, test.state, fakeResolver{})
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
	p := resolveAndPlan(context.Background(), config.Config{Version: 2}, state, fakeResolver{})
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
			p := resolveAndPlan(context.Background(), config.Config{Version: 2}, state, fakeResolver{})
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
	p := resolveAndPlan(context.Background(), config.Config{Version: 2}, state, fakeResolver{})
	if p.ReviewSSHIdentities || p.ReviewSSHAgent || p.LoadSSHAgent || p.ReviewGitHubKeys || p.ConfigureGitHubKey {
		t.Fatalf("ready access forced repeated review: %#v", p)
	}
}

func TestAppPrerequisitesSourcesAndMultilib(t *testing.T) {
	cfg := config.Config{Version: 2, Applications: []config.Application{
		{Source: "pacman", Identifier: "steam"},
		{Source: "aur", Identifier: "browser-bin"},
		{Source: "flatpak", Identifier: "org.example.Mail"},
	}}
	r := fakeResolver{pacman: map[string]Package{
		"steam":     {Name: "steam", Repository: "multilib"},
		"gamescope": {Name: "gamescope", Repository: "extra"},
		"choice-a":  {Name: "choice-a"},
		"choice-b":  {Name: "choice-b"},
	}, aur: map[string]Package{"browser-bin": {Name: "browser-bin"}}, flat: map[string]bool{"org.example.Mail": true}}
	s := readyState()
	s.Multilib = false
	p := resolveAndPlan(context.Background(), cfg, s, r)
	if !p.EnableMultilib || len(p.Applications) != 3 {
		t.Fatalf("unexpected plan: %#v", p)
	}
}

func TestDuplicateApplicationActionsArePlannedOnce(t *testing.T) {
	cfg := config.Config{Version: 2, Applications: []config.Application{
		{Source: "pacman", Identifier: "first"},
		{Source: "pacman", Identifier: "second"},
		{Source: "pacman", Identifier: "shared"},
	}}
	r := fakeResolver{pacman: map[string]Package{
		"first":  {Name: "first"},
		"second": {Name: "second"},
		"shared": {Name: "shared"},
		"helper": {Name: "helper"},
	}}
	p := resolveAndPlan(context.Background(), cfg, readyState(), r)
	if len(p.Applications) != 3 {
		t.Fatalf("unexpected implicit applications: %#v", p.Applications)
	}
}

func TestIdempotencyAndNoRemovalPlanning(t *testing.T) {
	s := readyState()
	s.Installed["firefox"], s.Installed["old-app"] = true, true
	s.Explicit["firefox"] = true
	s.OfficialMatches["firefox"] = "extra/firefox"
	cfg := config.Config{Version: 2, Applications: []config.Application{{Source: "pacman", Identifier: "firefox"}}}
	p := resolveAndPlan(context.Background(), cfg, s, fakeResolver{})
	if p.Applications[0].State != "ready" || len(p.CorePackages) != 0 {
		t.Fatalf("second run not idempotent: %#v", p)
	}
	// old-app is absent from intent and no removal operation exists in Plan.
}

func TestDeclaredAURPlansConcreteDependencyTransaction(t *testing.T) {
	source := AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "paru", Version: "2.1.0-2",
		Depends: []string{"git", "libalpm.so>=14"}, MakeDepends: []string{"cargo"}, CheckDepends: []string{"test-tool"},
		Packages: []aurmeta.Package{{Name: "paru", Depends: []string{"runtime-helper"}}},
	}}
	resolver := fakeResolver{aur: map[string]Package{"paru": {Name: "paru", PackageBase: "paru"}}, source: &source, deps: map[string]OfficialDependency{
		"base-devel":     {Requirement: "base-devel", Provider: "extra/base-devel", Packages: []string{"extra/base-devel"}},
		"cargo":          {Requirement: "cargo", Provider: "extra/rust", Packages: []string{"extra/llvm-libs", "extra/rust"}},
		"git":            {Requirement: "git", Provider: "extra/git", Packages: []string{"extra/git"}, Satisfied: true},
		"libalpm.so>=14": {Requirement: "libalpm.so>=14", Provider: "core/pacman", Packages: []string{"core/pacman"}, Satisfied: true},
		"runtime-helper": {Requirement: "runtime-helper", Provider: "extra/runtime-helper", Packages: []string{"extra/runtime-helper"}, Satisfied: true},
		"test-tool":      {Requirement: "test-tool", Provider: "extra/test-tool", Packages: []string{"extra/test-tool"}},
	}}
	state := readyState()
	state.Installed["base-devel"] = false
	state.Explicit["base-devel"] = false
	p := resolveAndPlan(context.Background(), config.Config{Version: 2, Applications: []config.Application{{Source: "aur", Identifier: "paru"}}}, state, resolver)
	if p.Applications[0].State != "install" || strings.Join(p.Applications[0].AUROutputs, ",") != "paru" {
		t.Fatalf("bootstrap outputs=%v enabled=%v", p.Applications[0].AUROutputs, p.Applications[0].State == "install")
	}
	var packages []string
	for _, pkg := range p.Applications[0].AURPackages {
		packages = append(packages, pkg.Name)
	}
	if strings.Join(packages, ",") != "base-devel,llvm-libs,rust,test-tool" {
		t.Fatalf("bootstrap packages=%v", p.Applications[0].AURPackages)
	}
	if contains(p.CorePackages, "base-devel") {
		t.Fatalf("base-devel escaped the post-review bootstrap boundary: %v", p.CorePackages)
	}
	if len(p.Applications[0].AURDependencies) != 6 {
		t.Fatalf("dependency bindings=%#v", p.Applications[0].AURDependencies)
	}
}

func TestAURDependencyPreservesDeclaredOfficialApplication(t *testing.T) {
	source := AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "paru", Version: "1-1", MakeDepends: []string{"cargo", "rustfmt"}, Packages: []aurmeta.Package{{Name: "paru"}},
	}}
	transaction := []string{"extra/llvm-libs", "extra/rust"}
	resolver := fakeResolver{
		aur: map[string]Package{"paru": {Name: "paru", PackageBase: "paru"}}, source: &source,
		deps: map[string]OfficialDependency{
			"base-devel": {Requirement: "base-devel", Provider: "extra/base-devel", Packages: []string{"extra/base-devel"}, Satisfied: true},
			"cargo":      {Requirement: "cargo", Provider: "extra/rust", Packages: transaction},
			"rustfmt":    {Requirement: "rustfmt", Provider: "extra/rust", Packages: transaction},
		},
		pacman: map[string]Package{"rust": {Name: "rust", Repository: "extra"}},
	}
	state := readyState()
	p := resolveAndPlan(context.Background(), config.Config{Version: 2, Applications: []config.Application{{Source: "aur", Identifier: "paru"}, {Source: "pacman", Identifier: "rust"}}}, state, resolver)
	if len(p.Applications[0].AURPackages) != 2 || p.Applications[0].AURPackages[0].Name != "llvm-libs" || p.Applications[0].AURPackages[1].Name != "rust" || !p.Applications[0].AURPackages[1].AsExplicit {
		t.Fatalf("deduplicated packages=%#v", p.Applications[0].AURPackages)
	}
	if p.Applications[1].State != "install" {
		t.Fatalf("application was not represented exactly once: %#v", p.Applications[0])
	}
}

func TestAURApplicationPinsSourceAndPlansOfficialBuildDependencies(t *testing.T) {
	source := AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "browser-bin", Version: "1-1", Depends: []string{"runtime"}, MakeDepends: []string{"builder"}, Optional: []string{"feature>=1:2: optional integration"},
		Packages: []aurmeta.Package{{Name: "browser-bin"}},
	}}
	state := readyState()
	state.Installed["base-devel"] = true
	resolver := fakeResolver{
		aur:    map[string]Package{"browser-bin": {Name: "browser-bin", PackageBase: "browser-bin"}},
		pacman: map[string]Package{"feature": {Name: "feature", Repository: "extra"}},
		source: &source,
		deps: map[string]OfficialDependency{
			"runtime":      {Requirement: "runtime", Provider: "extra/runtime", Packages: []string{"extra/runtime", "extra/runtime-libs"}},
			"builder":      {Requirement: "builder", Provider: "extra/builder", Packages: []string{"extra/builder"}},
			"feature>=1:2": {Requirement: "feature>=1:2", Provider: "extra/feature", Packages: []string{"extra/feature", "extra/feature-libs"}},
			"base-devel":   {Requirement: "base-devel", Provider: "extra/base-devel", Packages: []string{"extra/base-devel"}, Satisfied: true},
		},
	}
	p := resolveAndPlan(context.Background(), config.Config{Version: 2, Applications: []config.Application{{Source: "aur", Identifier: "browser-bin"}}}, state, resolver)
	app := p.Applications[0]
	if app.State != "install" || app.AURSource.Commit != source.Commit || strings.Join(app.AUROutputs, ",") != "browser-bin" {
		t.Fatalf("application=%#v", app)
	}
	if len(app.AURDependencies) != 3 || len(app.AURPackages) != 3 || app.AURPackages[0].Name != "builder" || app.AURPackages[1].Name != "runtime" || app.AURPackages[2].Name != "runtime-libs" {
		t.Fatalf("required dependency transaction=%#v", app)
	}

}

func TestAURApplicationPlansOnlyMissingPinnedSigningKeys(t *testing.T) {
	first := "0123456789ABCDEF0123456789ABCDEF01234567"
	second := "FEDCBA9876543210FEDCBA9876543210FEDCBA98"
	source := AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "browser-bin", Version: "1-1", ValidPGPKeys: []string{first, second}, Packages: []aurmeta.Package{{Name: "browser-bin"}},
	}}
	state := readyState()
	resolver := fakeResolver{
		aur:    map[string]Package{"browser-bin": {Name: "browser-bin", PackageBase: "browser-bin"}},
		source: &source,
		pgp:    map[string]bool{first: true},
		deps:   map[string]OfficialDependency{"base-devel": {Requirement: "base-devel", Provider: "extra/base-devel", Packages: []string{"extra/base-devel"}, Satisfied: true}},
	}
	p := resolveAndPlan(context.Background(), config.Config{Version: 2, Applications: []config.Application{{Source: "aur", Identifier: "browser-bin"}}}, state, resolver)
	if got := strings.Join(p.Applications[0].AURSigningKeys, ","); got != second {
		t.Fatalf("planned signing keys=%q", got)
	}
}

func TestAURApplicationWithoutPinnedSigningKeysPlansNoKeyWork(t *testing.T) {
	calls := 0
	source := AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "browser-bin", Version: "1-1", Packages: []aurmeta.Package{{Name: "browser-bin"}},
	}}
	p := resolveAndPlan(context.Background(), config.Config{Version: 2, Applications: []config.Application{{Source: "aur", Identifier: "browser-bin"}}}, readyState(), fakeResolver{
		aur: map[string]Package{"browser-bin": {Name: "browser-bin", PackageBase: "browser-bin"}}, source: &source,
		deps: map[string]OfficialDependency{"base-devel": {Requirement: "base-devel", Provider: "extra/base-devel", Packages: []string{"extra/base-devel"}, Satisfied: true}}, pgpCalls: &calls,
	})
	if calls != 0 || len(p.Applications[0].AURSigningKeys) != 0 {
		t.Fatalf("empty signing-key metadata planned key work: calls=%d application=%#v", calls, p.Applications[0])
	}
}

func TestAURApplicationSigningKeyInspectionFailureFailsClosed(t *testing.T) {
	source := AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "browser-bin", Version: "1-1", ValidPGPKeys: []string{"0123456789ABCDEF0123456789ABCDEF01234567"}, Packages: []aurmeta.Package{{Name: "browser-bin"}},
	}}
	p := resolveAndPlan(context.Background(), config.Config{Version: 2, Applications: []config.Application{{Source: "aur", Identifier: "browser-bin"}}}, readyState(), fakeResolver{
		aur: map[string]Package{"browser-bin": {Name: "browser-bin", PackageBase: "browser-bin"}}, source: &source,
		deps: map[string]OfficialDependency{"base-devel": {Requirement: "base-devel", Provider: "extra/base-devel", Packages: []string{"extra/base-devel"}, Satisfied: true}}, pgpErr: errors.New("keyring unavailable"),
	})
	if p.Applications[0].State != "failed" || !strings.Contains(p.Applications[0].Cause, "signing-key inspection") {
		t.Fatalf("keyring failure did not fail closed: %#v", p.Applications[0])
	}
}

func TestAURInstallReasonsKeepPacmanAndAURDeclarationsSeparate(t *testing.T) {
	source := AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "suite", Version: "1-1", Depends: []string{"shared"}, Packages: []aurmeta.Package{
			{Name: "suite-cli", Depends: []string{"foo=1-1"}}, {Name: "foo"},
		},
	}}
	resolver := fakeResolver{
		aur: map[string]Package{
			"suite-cli": {Name: "suite-cli", PackageBase: "suite"},
			"foo":       {Name: "foo", PackageBase: "other"},
			"shared":    {Name: "shared", PackageBase: "other"},
		},
		source: &source,
		deps: map[string]OfficialDependency{
			"base-devel": {Requirement: "base-devel", Provider: "extra/base-devel", Packages: []string{"extra/base-devel"}, Satisfied: true},
			"shared":     {Requirement: "shared", Provider: "extra/shared", Packages: []string{"extra/shared"}},
		},
	}
	for _, test := range []struct {
		name             string
		applications     []config.Application
		explicitOutputs  string
		officialExplicit bool
	}{
		{name: "target only", applications: []config.Application{{Source: "aur", Identifier: "suite-cli"}}, explicitOutputs: "suite-cli", officialExplicit: false},
		{name: "declared AUR sibling", applications: []config.Application{{Source: "aur", Identifier: "suite-cli"}, {Source: "aur", Identifier: "foo"}}, explicitOutputs: "foo,suite-cli", officialExplicit: false},
		{name: "declared pacman dependency", applications: []config.Application{{Source: "aur", Identifier: "suite-cli"}, {Source: "pacman", Identifier: "shared"}}, explicitOutputs: "suite-cli", officialExplicit: true},
		{name: "AUR name does not make official dependency explicit", applications: []config.Application{{Source: "aur", Identifier: "suite-cli"}, {Source: "aur", Identifier: "shared"}}, explicitOutputs: "suite-cli", officialExplicit: false},
		{name: "pacman name does not make AUR split output explicit", applications: []config.Application{{Source: "aur", Identifier: "suite-cli"}, {Source: "pacman", Identifier: "foo"}}, explicitOutputs: "suite-cli", officialExplicit: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := resolveAndPlan(context.Background(), config.Config{Version: 2, Applications: test.applications}, readyState(), resolver)
			app := p.Applications[0]
			if strings.Join(app.AURExplicitOutputs, ",") != test.explicitOutputs {
				t.Fatalf("explicit AUR outputs=%#v", app.AURExplicitOutputs)
			}
			if len(app.AURPackages) != 1 || app.AURPackages[0].Name != "shared" || app.AURPackages[0].AsExplicit != test.officialExplicit {
				t.Fatalf("official dependency intent=%#v", app.AURPackages)
			}
		})
	}
}

func TestAURInstallReasonIntentDoesNotDependOnApplicationOrder(t *testing.T) {
	source := AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "suite", Version: "1-1", Depends: []string{"shared"}, Packages: []aurmeta.Package{
			{Name: "suite-cli", Depends: []string{"foo=1-1"}}, {Name: "foo"},
		},
	}}
	resolver := fakeResolver{
		aur: map[string]Package{
			"suite-cli": {Name: "suite-cli", PackageBase: "suite"},
			"foo":       {Name: "foo", PackageBase: "other"},
		},
		source: &source,
		deps: map[string]OfficialDependency{
			"base-devel": {Requirement: "base-devel", Provider: "extra/base-devel", Packages: []string{"extra/base-devel"}, Satisfied: true},
			"shared":     {Requirement: "shared", Provider: "extra/shared", Packages: []string{"extra/shared"}},
		},
	}
	configs := [][]config.Application{
		{{Source: "aur", Identifier: "suite-cli"}, {Source: "pacman", Identifier: "shared"}, {Source: "aur", Identifier: "foo"}},
		{{Source: "aur", Identifier: "foo"}, {Source: "pacman", Identifier: "shared"}, {Source: "aur", Identifier: "suite-cli"}},
	}
	for _, applications := range configs {
		p := resolveAndPlan(context.Background(), config.Config{Version: 2, Applications: applications}, readyState(), resolver)
		var app Application
		for _, candidate := range p.Applications {
			if candidate.Declaration.Identifier == "suite-cli" {
				app = candidate
			}
		}
		if strings.Join(app.AURExplicitOutputs, ",") != "foo,suite-cli" || len(app.AURPackages) != 1 || !app.AURPackages[0].AsExplicit {
			t.Fatalf("application order changed install-reason intent: %#v", app)
		}
	}
}

func TestAURInstallReasonsPreserveOfficialRepairAndAUROutputIntent(t *testing.T) {
	source := AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "suite", Version: "1-1", Depends: []string{"shared"}, Packages: []aurmeta.Package{
			{Name: "suite-cli", Depends: []string{"foo=1-1"}}, {Name: "foo"},
		},
	}}
	resolver := fakeResolver{
		aur:    map[string]Package{"suite-cli": {Name: "suite-cli", PackageBase: "suite"}},
		source: &source,
		deps: map[string]OfficialDependency{
			"base-devel": {Requirement: "base-devel", Provider: "extra/base-devel", Packages: []string{"extra/base-devel"}, Satisfied: true},
			"shared":     {Requirement: "shared", Provider: "extra/shared", Packages: []string{"extra/shared"}},
		},
	}
	for _, test := range []struct {
		name             string
		foreign          map[string]bool
		explicitOutputs  string
		officialExplicit bool
	}{
		{name: "official explicit does not transfer to AUR output", foreign: map[string]bool{}, explicitOutputs: "suite-cli", officialExplicit: true},
		{name: "official repair preserves foreign explicit reason", foreign: map[string]bool{"shared": true, "foo": true}, explicitOutputs: "foo,suite-cli", officialExplicit: true},
		{name: "official explicit dependency is preserved", foreign: map[string]bool{"foo": true}, explicitOutputs: "foo,suite-cli", officialExplicit: true},
		{name: "foreign explicit AUR output is preserved", foreign: map[string]bool{"shared": true, "foo": true}, explicitOutputs: "foo,suite-cli", officialExplicit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := readyState()
			state.Installed["shared"], state.Explicit["shared"] = true, true
			state.Installed["foo"], state.Explicit["foo"] = true, true
			for name := range test.foreign {
				state.Foreign[name] = true
			}
			p := resolveAndPlan(context.Background(), config.Config{Version: 2, Applications: []config.Application{{Source: "aur", Identifier: "suite-cli"}}}, state, resolver)
			app := p.Applications[0]
			if strings.Join(app.AURExplicitOutputs, ",") != test.explicitOutputs || len(app.AURPackages) != 1 || app.AURPackages[0].AsExplicit != test.officialExplicit {
				t.Fatalf("source-qualified explicit state was not preserved: %#v", app)
			}
		})
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
			"base-devel":      {Requirement: "base-devel", Provider: "extra/base-devel", Packages: []string{"extra/base-devel"}, Satisfied: true},
			"aur-only-helper": {Requirement: "aur-only-helper"},
		},
	}
	p := resolveAndPlan(context.Background(), config.Config{Version: 2, Applications: []config.Application{{Source: "aur", Identifier: "browser-bin"}}}, state, resolver)
	app := p.Applications[0]
	if app.State != "failed" || !strings.Contains(app.Cause, "missing provider transaction") || len(app.AURPackages) != 0 {
		t.Fatalf("unsupported AUR dependency was planned: %#v", app)
	}
}

func TestAURApplicationDoesNotSelectOptionalDependencies(t *testing.T) {
	state := readyState()
	source := AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "browser-bin", Version: "1-1", Optional: []string{"aur-helper: optional integration"}, Packages: []aurmeta.Package{{Name: "browser-bin"}},
	}}
	resolver := fakeResolver{
		aur: map[string]Package{
			"browser-bin": {Name: "browser-bin", PackageBase: "browser-bin"},
			"aur-helper":  {Name: "aur-helper", PackageBase: "aur-helper"},
		},
		source: &source,
	}
	p := resolveAndPlan(context.Background(), config.Config{Version: 2, Applications: []config.Application{{Source: "aur", Identifier: "browser-bin"}}}, state, resolver)
	if len(p.Applications) != 1 || p.Applications[0].State != "install" {
		t.Fatalf("unsupported optional AUR dependency was not rejected: %#v", p.Applications)
	}
}

func TestInstalledAURApplicationConvergesWithoutAnotherBuildPlan(t *testing.T) {
	state := readyState()
	state.Installed["browser-bin"] = true
	state.Foreign["browser-bin"] = true
	state.Explicit["browser-bin"] = true
	p := resolveAndPlan(context.Background(), config.Config{Version: 2, Applications: []config.Application{{Source: "aur", Identifier: "browser-bin"}}}, state, fakeResolver{})
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
	cfg := config.Config{Version: 2, Applications: []config.Application{{Source: "pacman", Identifier: "missing"}}}
	p := resolveAndPlan(context.Background(), cfg, readyState(), fakeResolver{})
	if p.Applications[0].State != "unresolved" {
		t.Fatalf("plan = %#v", p)
	}
}

type Package = plan.Package
type AURSource = plan.AURSource
type OfficialDependency = plan.OfficialDependency
type State = plan.State
type Plan = plan.Plan
type Application = plan.Application
type SSHHostKeyFreshness = plan.SSHHostKeyFreshness

const (
	SSHHostKeyFreshnessCurrent     = plan.SSHHostKeyFreshnessCurrent
	SSHHostKeyFreshnessUnknown     = plan.SSHHostKeyFreshnessUnknown
	SSHHostKeyFreshnessStale       = plan.SSHHostKeyFreshnessStale
	SSHHostKeyFreshnessUnavailable = plan.SSHHostKeyFreshnessUnavailable
)

func resolveAndPlan(ctx context.Context, cfg config.Config, state State, resolver MetadataResolver) Plan {
	return plan.Build(cfg, state, Applications(ctx, cfg, state, resolver))
}
