package archrepo_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/resolve"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

func query(t *testing.T, f *testpkg.PacmanFixture, args ...string) string {
	t.Helper()
	r, err := f.Run(context.Background(), run.Spec{Name: "pacman", Args: args})
	if err != nil {
		t.Fatalf("%v: %v %s", args, err, r.Stderr)
	}
	return r.Stdout
}

// Preserved D-R1 adversarial regression. Synthetic local state represents the
// custom archive's installed data;
// every pacman command is read-only and redirected to the isolated databases.
func TestReviewRejectsForgedCustomPackage(t *testing.T) {
	f := testpkg.NewPacmanFixture(t)
	official := testpkg.FixturePackage{Name: "git", Version: "1-1", Packager: "Official Builder", Payload: "official payload", Provides: "ops-virtual=2"}
	forged := official
	forged.Payload = "custom substituted payload"
	customHashes := f.Sync(t, "custom", forged)
	f.Sync(t, "core")
	officialHashes := f.Sync(t, "extra", official)
	f.Local(t, forged)
	if customHashes["git"] == officialHashes["git"] {
		t.Fatal("fixture archives must differ")
	}
	for _, target := range []string{"custom/git", "extra/git"} {
		t.Log(target + " " + strings.TrimSpace(query(t, f, "-Sp", "--print-format", "%h", "--", target)))
	}
	local, err := archrepo.ParseInfo(query(t, f, "-Qi", "--", "git"))
	if err != nil {
		t.Fatal(err)
	}
	sync, err := archrepo.ParseInfo(query(t, f, "-Si", "--", "extra/git"))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"Name", "Version", "Architecture", "Build Date", "Packager"} {
		if local[field] != sync[field] {
			t.Fatalf("fixture %s differs", field)
		}
	}
	if local["Repository"] != "" {
		t.Fatal("libalpm unexpectedly retained origin")
	}
	match, err := archrepo.InstalledMatch(context.Background(), f, "extra/git")
	if err != nil {
		t.Fatal(err)
	}
	if match {
		t.Error("D-R1: forged custom archive satisfies InstalledMatch")
	}
	matches, err := archrepo.InstalledMatches(context.Background(), f, []string{"git"})
	if err != nil {
		t.Fatal(err)
	}
	state := plan.State{Installed: map[string]bool{"git": true}, Explicit: map[string]bool{"git": true}, OfficialMatches: matches}
	app := config.Application{Source: config.Pacman, Identifier: "git"}
	p := plan.Build(config.Config{Applications: []config.Application{app}}, state, nil)
	if p.Core["git"] == "ready" || p.Applications[0].State == plan.Ready {
		t.Error("D-R1: forged custom package satisfies core and pacman declaration")
	}
	// Custom is configured, but place official first to ensure the resolver
	// selects an official transaction while -T is satisfied by the forged local.
	f.Configure(t, "core", "extra", "custom")
	for _, requirement := range []string{"git", "ops-virtual", "ops-virtual>=2"} {
		binding, err := (resolve.Resolver{Runner: f}).OfficialDependency(context.Background(), requirement)
		if err == nil && binding.Satisfied {
			t.Errorf("D-R1: forged satisfier accepted for %s: %+v", requirement, binding)
		}
	}
	// Model the forced official repair, then prove idempotence, later custom
	// replacement, and an official upgrade all use fresh content evidence.
	f.Local(t, official)
	for i := 0; i < 2; i++ {
		if match, err := archrepo.InstalledMatch(context.Background(), f, "extra/git"); err != nil || !match {
			t.Fatalf("post-repair state not ready: %v", err)
		}
	}
	f.Local(t, forged)
	if match, err := archrepo.InstalledMatch(context.Background(), f, "extra/git"); err != nil || match {
		t.Fatalf("later custom replacement retained readiness: %v", err)
	}
	upgraded := official
	upgraded.Version, upgraded.Payload = "2-1", "upgraded official payload"
	f.Sync(t, "extra", upgraded)
	if match, err := archrepo.InstalledMatch(context.Background(), f, "extra/git"); err != nil || match {
		t.Fatalf("changed archive retained readiness: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(f.Dir, "db/local/git-1-1")); err != nil {
		t.Fatal(err)
	}
	f.Local(t, upgraded)
	if match, err := archrepo.InstalledMatch(context.Background(), f, "extra/git"); err != nil || !match {
		t.Fatalf("authenticated upgrade did not reestablish readiness: %v", err)
	}
}

func TestReviewQualifiedNeededSkipsEqualVersion(t *testing.T) {
	f := testpkg.NewPacmanFixture(t)
	official := testpkg.FixturePackage{Name: "ops-probe", Version: "1-1", Packager: "Official", Payload: "official"}
	custom := official
	custom.Packager, custom.Payload = "Custom", "custom"
	f.Sync(t, "core")
	f.Sync(t, "extra", official)
	f.Sync(t, "custom", custom)
	f.Local(t, custom)
	if got := query(t, f, "-Sp", "--needed", "--noconfirm", "--print-format", "%r/%n", "--", "extra/ops-probe"); got != "" {
		t.Fatalf("--needed did not skip: %q", got)
	}
	if got := query(t, f, "-Sp", "--noconfirm", "--print-format", "%r/%n", "--", "extra/ops-probe"); got != "extra/ops-probe\n" {
		t.Fatalf("forced reinstall missing: %q", got)
	}
}

func TestReviewSatisfiedProviderMatrix(t *testing.T) {
	for _, scenario := range []string{"direct official", "virtual official", "versioned official", "custom", "foreign", "multiple"} {
		t.Run(scenario, func(t *testing.T) {
			f := testpkg.NewPacmanFixture(t)
			p := testpkg.FixturePackage{Name: "ops-provider", Version: "2-1", Packager: "Official", Payload: "official", Provides: "ops-virtual=2"}
			custom := p
			custom.Name, custom.Packager = "ops-custom", "Custom"
			f.Sync(t, "core")
			f.Sync(t, "extra", p)
			f.Sync(t, "custom", custom)
			requirement, want := "ops-virtual", true
			switch scenario {
			case "direct official":
				requirement = p.Name
				f.Local(t, p)
			case "virtual official":
				f.Local(t, p)
			case "versioned official":
				requirement = "ops-virtual>=2"
				f.Local(t, p)
			case "custom":
				f.Local(t, custom)
				want = false
			case "foreign":
				custom.Name = "ops-foreign"
				f.Local(t, custom)
				want = false
			case "multiple":
				f.Local(t, p)
				f.Local(t, custom)
			}
			f.Configure(t, "core", "extra", "custom")
			if got := query(t, f, "-T", "--", requirement); got != "" {
				t.Fatalf("requirement not satisfied: %s", got)
			}
			binding, err := (resolve.Resolver{Runner: f}).OfficialDependency(context.Background(), requirement)
			if (err == nil) != want {
				t.Fatalf("binding=%+v err=%v", binding, err)
			}
			if err == nil && (!binding.Satisfied || binding.Provider != "extra/ops-provider") {
				t.Fatalf("binding=%+v", binding)
			}
		})
	}
}

func TestReviewFullUpgradeMustNotOmitConfiguredRebuild(t *testing.T) {
	f := testpkg.NewPacmanFixture(t)
	lib1 := testpkg.FixturePackage{Name: "ops-library", Version: "1-1", Packager: "Official", Payload: "libops.so.1"}
	lib2 := lib1
	lib2.Version, lib2.Payload = "2-1", "libops.so.2"
	client1 := testpkg.FixturePackage{Name: "ops-client", Version: "1-1", Packager: "Custom", Payload: "requires libops.so.1", Depends: "ops-library"}
	client2 := client1
	client2.Version, client2.Payload = "2-1", "requires libops.so.2"
	f.Local(t, lib1)
	f.Local(t, client1)
	f.Sync(t, "core")
	f.Sync(t, "extra", lib2)
	f.Sync(t, "custom", client2)
	full := query(t, f, "-Sup", "--noconfirm", "--print-format", "%r/%n")
	if !strings.Contains(full, "custom/ops-client\n") || !strings.Contains(full, "extra/ops-library\n") {
		t.Fatal(full)
	}
	f.Configure(t, "core", "extra")
	filtered := query(t, f, "-Sup", "--noconfirm", "--print-format", "%r/%n")
	if filtered != "extra/ops-library\n" {
		t.Fatal(filtered)
	}
	t.Logf("complete configured upgrade: %q; filtered upgrade: %q", full, filtered)
}

func TestReviewRejectsOfficialNameSpoof(t *testing.T) {
	_, _, err := archrepo.OfficialConfig("[options]\nArchitecture = x86_64\nSigLevel = Never\n[core]\nServer = file:///custom/core\n[extra]\nServer = file:///custom/extra\n")
	if err == nil {
		t.Error("D-R4: custom repository content trusted solely by official section names")
	}
}

func TestReviewSatisfiedTransitiveCustomDependency(t *testing.T) {
	f := testpkg.NewPacmanFixture(t)
	provider := testpkg.FixturePackage{Name: "ops-builder", Version: "1-1", Packager: "Official", Payload: "official builder", Depends: "ops-compiler"}
	compiler := testpkg.FixturePackage{Name: "ops-compiler", Version: "1-1", Packager: "Official", Payload: "official compiler"}
	custom := compiler
	custom.Packager, custom.Payload = "Custom", "custom compiler"
	f.Sync(t, "core")
	f.Sync(t, "extra", provider, compiler)
	f.Sync(t, "custom", custom)
	f.Local(t, provider)
	f.Local(t, custom)
	f.Configure(t, "core", "extra", "custom")
	if got := query(t, f, "-T", "--", "ops-builder"); got != "" {
		t.Fatal(got)
	}
	binding, err := (resolve.Resolver{Runner: f}).OfficialDependency(context.Background(), "ops-builder")
	if err == nil && binding.Satisfied {
		t.Errorf("D-R6: satisfied custom transitive dependency omitted from provenance checks: %+v", binding)
	}
}

func TestReviewTransitiveProviderClosure(t *testing.T) {
	for _, scenario := range []string{"official", "custom", "foreign", "forged", "cycle", "missing", "repository drift"} {
		t.Run(scenario, func(t *testing.T) {
			f := testpkg.NewPacmanFixture(t)
			builder := testpkg.FixturePackage{Name: "ops-builder", Version: "1-1", Packager: "Official", Payload: "builder", Depends: "ops-virtual>=2"}
			compiler := testpkg.FixturePackage{Name: "ops-compiler", Version: "2-1", Packager: "Official", Payload: "compiler", Provides: "ops-virtual=2"}
			if scenario == "cycle" {
				compiler.Depends = "ops-builder"
			}
			f.Sync(t, "core")
			f.Sync(t, "extra", builder, compiler)
			f.Sync(t, "custom")
			f.Local(t, builder)
			switch scenario {
			case "forged":
				forged := compiler
				forged.Payload = "substituted compiler with copied official metadata"
				f.Local(t, forged)
			case "custom", "foreign":
				other := compiler
				other.Name, other.Packager, other.Payload = "ops-other", "Custom", "custom compiler"
				f.Local(t, other)
				if scenario == "custom" {
					f.Sync(t, "custom", other)
				}
			case "missing":
			default:
				f.Local(t, compiler)
			}
			f.Configure(t, "core", "extra", "custom")
			resolver := resolve.Resolver{Runner: f}
			binding, err := resolver.OfficialDependency(context.Background(), "ops-builder")
			if scenario == "forged" {
				if err != nil || binding.Satisfied || strings.Join(binding.Packages, ",") != "extra/ops-builder,extra/ops-compiler" {
					t.Fatalf("forged transitive provider not retained for repair: %+v %v", binding, err)
				}
				return
			}
			valid := scenario == "official" || scenario == "cycle" || scenario == "repository drift"
			if (err == nil) != valid {
				t.Fatalf("binding=%+v err=%v", binding, err)
			}
			if valid && (!binding.Satisfied || strings.Join(binding.Packages, ",") != "extra/ops-builder,extra/ops-compiler") {
				t.Fatalf("incomplete installed closure: %+v", binding)
			}
			if scenario == "repository drift" {
				f.Sync(t, "extra", builder)
				f.Sync(t, "core", compiler)
				changed, err := resolver.OfficialDependency(context.Background(), "ops-builder")
				if err != nil || strings.Join(changed.Packages, ",") != "core/ops-compiler,extra/ops-builder" {
					t.Fatalf("transitive repository drift was lost: %+v %v", changed, err)
				}
			}
		})
	}
}

func TestReviewExpandedConfigurationPreservesSecurityPolicy(t *testing.T) {
	if _, err := exec.LookPath("pacman-conf"); err != nil {
		t.Skip("pacman-conf unavailable")
	}
	dir := t.TempDir()
	include := filepath.Join(dir, "mirrors")
	if err := os.WriteFile(include, []byte("Server = https://mirror.example/$repo/os/$arch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input := "[options]\nArchitecture = x86_64\nRootDir = /\nDBPath = /var/lib/pacman\nGPGDir = /etc/pacman.d/gnupg\nSigLevel = Required DatabaseOptional TrustedOnly\nLocalFileSigLevel = Required\nRemoteFileSigLevel = Required\n[custom]\nSigLevel = Never\nServer = file:///custom\n[core]\nInclude = " + include + "\n[extra]\nSigLevel = Required\nInclude = " + include + "\n#[multilib]\n#Include = " + include + "\n"
	path := filepath.Join(dir, "pacman.conf")
	expand := func(data string) string {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		r, err := (run.Exec{}).Run(context.Background(), run.Spec{Name: "pacman-conf", Args: []string{"--config", path}})
		if err != nil {
			t.Fatalf("expand config: %v %s", err, r.Stderr)
		}
		return r.Stdout
	}
	expanded := expand(input)
	filtered, custom, err := archrepo.OfficialConfig(expanded)
	if err != nil || !custom {
		t.Fatalf("filter: %v custom=%v", err, custom)
	}
	if strings.Contains(filtered, "Include") || strings.Contains(filtered, "[custom]") || strings.Contains(filtered, "[multilib]") {
		t.Fatal(filtered)
	}
	roundTrip := expand(filtered)
	canonical, _, canonicalErr := archrepo.OfficialConfig(roundTrip)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	if canonical != filtered {
		t.Fatalf("effective policy changed during serialization:\nbefore:\n%s\nafter:\n%s", filtered, roundTrip)
	}
	for _, field := range []string{"RootDir", "DBPath", "GPGDir", "Architecture", "SigLevel", "LocalFileSigLevel", "RemoteFileSigLevel"} {
		if !strings.Contains(filtered, field+" = ") {
			t.Errorf("missing %s", field)
		}
	}
}

func TestNativeEqualVersionRepairPreservesInstallReason(t *testing.T) {
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare unavailable")
	}
	if _, err := (run.Exec{}).Run(context.Background(), run.Spec{Name: "unshare", Args: []string{"--user", "--map-root-user", "--", "/bin/true"}}); err != nil {
		t.Skip("unprivileged user namespace unavailable")
	}
	for _, reason := range []string{"0", "1"} {
		t.Run(reason, func(t *testing.T) {
			f := testpkg.NewPacmanFixture(t)
			p := testpkg.FixturePackage{Name: "ops-reason-fixture", Version: "1-1", Packager: "Fixture", Payload: "official"}
			f.Sync(t, "core")
			f.Sync(t, "extra", p)
			f.Local(t, p)
			f.Configure(t, "core", "extra")
			desc := filepath.Join(f.Dir, "db/local/ops-reason-fixture-1-1/desc")
			before, err := os.ReadFile(desc)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(desc, []byte(strings.Replace(string(before), "%REASON%\n0", "%REASON%\n"+reason, 1)), 0600); err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{"cache", "hooks"} {
				if err := os.Mkdir(filepath.Join(f.Dir, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			// Only disposable local DB state is changed; payload extraction,
			// scriptlets and system hook paths are outside this probe.
			args := []string{"--user", "--map-root-user", "--", "pacman", "--config", f.Conf, "--root", f.Dir, "--dbpath", filepath.Join(f.Dir, "db"), "--logfile", filepath.Join(f.Dir, "pacman.log"), "--cachedir", filepath.Join(f.Dir, "cache"), "--hookdir", filepath.Join(f.Dir, "hooks"), "-S", "--noconfirm", "--dbonly", "--noscriptlet", "--", "extra/ops-reason-fixture"}
			result, err := (run.Exec{}).Run(context.Background(), run.Spec{Name: "unshare", Args: args})
			if err != nil {
				t.Fatalf("isolated reason transaction: %v %s", err, result.Stderr)
			}
			after, err := os.ReadFile(desc)
			// libalpm omits REASON for explicit (the default zero value).
			retained := !strings.Contains(string(after), "%REASON%") || strings.Contains(string(after), "%REASON%\n0\n")
			if reason == "1" {
				retained = strings.Contains(string(after), "%REASON%\n1\n")
			}
			if err != nil || !retained {
				t.Fatalf("reinstall changed reason %s: %s %v", reason, after, err)
			}
			if !strings.Contains(result.Stdout, "reinstalling") {
				t.Fatal("equal version repair was skipped", result.Stdout)
			}
		})
	}
}
