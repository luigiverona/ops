package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/flatpak"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/resolve"
	"github.com/luigiverona/ops/internal/ui"
)

func TestShowPlanConciseIntent(t *testing.T) {
	tests := []struct {
		name string
		plan plan.Plan
		want string
	}{
		{"mixed", realWorkstationPlan(t), "Workstation setup\n\nInstall\n  bitwarden (pacman)\n  com.tutanota.Tutanota (Flatpak)\n\nConfigure\n  SSH for GitHub\n  GitHub authentication\n  Register this workstation's SSH key with GitHub if needed\n\nManage GitHub SSH settings separately; preserve other host configuration.\n\nThe system will be updated.\n  Use official Arch repositories only (core, extra, multilib); custom repositories are excluded.\n\n"},
		{"identity", plan.Plan{ConfigureGit: true, CreateSSHIdentity: true, AuthenticateGitHub: true}, "Workstation setup\n\nConfigure\n  Git identity\n  SSH for GitHub\n  GitHub authentication\n\n"},
		{"ready", plan.Plan{Core: readyCore(), Applications: readyApplications()}, ""},
		{"scope refresh", plan.Plan{RefreshGitHubSSHKeyScope: true}, "Workstation setup\n\nConfigure\n  GitHub SSH key access\n\n"},
		{"application configuration", plan.Plan{Applications: []plan.Application{{Declaration: config.Application{Source: "pacman", Identifier: "mullvad-vpn"}, State: "configure", Services: []string{"mullvad-daemon.service"}}}}, "Workstation setup\n\nConfigure\n  mullvad-vpn (pacman)\n\nEnable and start\n  mullvad-daemon.service\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			Runtime{Out: &out}.showPlan(test.plan)
			if out.String() != test.want {
				t.Fatalf("got %q, want %q", out.String(), test.want)
			}
		})
	}
}

func TestShowPlanHidesImplementationButKeepsExactIdentifiers(t *testing.T) {
	p := declaredParuPlan(t)
	p.Applications = append(p.Applications, plan.Application{Declaration: config.Application{Source: "flatpak", Identifier: "org.example.AVeryLongIdentifier"}, State: "install"})
	p.Applications[0].AURSigningKeys = []string{"0123456789ABCDEF0123456789ABCDEF01234567"}
	var out bytes.Buffer
	Runtime{Out: &out}.showPlan(p)
	for _, want := range []string{"  paru (AUR)\n", "  org.example.AVeryLongIdentifier (Flatpak)\n", "Required dependencies"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, &out)
		}
	}
	assertConciseOutput(t, out.String())
	for _, hidden := range []string{"base-devel", "llvm-libs", "rust", "0123456789ABCDEF"} {
		if strings.Contains(out.String(), hidden) {
			t.Fatalf("internal dependency leaked: %s", &out)
		}
	}
}

func TestShowPlanPreservesDiagnostics(t *testing.T) {
	p := plan.Plan{ConfigureGit: true, Applications: []plan.Application{{
		Declaration: config.Application{Source: "aur", Identifier: "broken"}, State: "unresolved", Cause: "exact identifier not found",
	}}}
	var out bytes.Buffer
	Runtime{Out: &out}.showPlan(p)
	if !strings.Contains(out.String(), "Cannot install broken: exact identifier not found") || strings.Contains(out.String(), "\nInstall\n  broken") {
		t.Fatalf("misleading intent: %s", &out)
	}
}

func TestShowPlanIsDeterministic(t *testing.T) {
	var first, second bytes.Buffer
	Runtime{Out: &first}.showPlan(plan.Plan{CorePackages: []string{"git", "openssh"}, FullUpgrade: true})
	Runtime{Out: &second}.showPlan(plan.Plan{CorePackages: []string{"openssh", "git"}, FullUpgrade: true})
	if first.String() != second.String() {
		t.Fatal("dependency ordering leaked into summary")
	}
}

func TestFreshPlanAndKnownAccountConsequences(t *testing.T) {
	for _, known := range []bool{false, true} {
		state := plan.State{}
		if known {
			state = readyExecutionState()
			state.ManagedGitHubKey = false
		}
		p := plan.Build(config.Config{Version: 2}, state, nil)
		var out bytes.Buffer
		Runtime{Out: &out}.showPlan(p)
		registration := "Register this workstation's SSH key with GitHub"
		if !known {
			registration += " if needed"
			for _, want := range []string{"Git identity", "SSH for GitHub", "GitHub authentication", "Manage GitHub SSH settings separately; preserve other host configuration."} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q: %s", want, &out)
				}
			}
		}
		if !strings.Contains(out.String(), registration+"\n") || known && strings.Contains(out.String(), "if needed") {
			t.Fatalf("misleading registration: %s", &out)
		}
		assertConciseOutput(t, out.String())
	}
}

func TestMixedSourcesAndServicesUsePlanIdentity(t *testing.T) {
	p := plan.Plan{EnableMultilib: true, Applications: []plan.Application{
		{Declaration: config.Application{Source: config.Pacman, Identifier: "firefox"}, State: plan.Install, Services: []string{"z.service", "a.service"}},
		{Declaration: config.Application{Source: config.AUR, Identifier: "downgrade"}, State: plan.Install, Services: []string{"a.service"}},
		{Declaration: config.Application{Source: config.Flatpak, Identifier: "org.gimp.GIMP"}, State: plan.Install},
	}}
	var out bytes.Buffer
	Runtime{Out: &out}.showPlan(p)
	want := "Workstation setup\n\nInstall\n  firefox (pacman)\n  downgrade (AUR)\n  org.gimp.GIMP (Flatpak)\n\nEnable and start\n  a.service\n  z.service\n\nEnable multilib.\n\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestAURApprovalDisclosesOnlyPlannedKeysAndAdditionalOutputs(t *testing.T) {
	const fingerprint = "0123456789ABCDEF0123456789ABCDEF01234567"
	for _, extra := range []bool{false, true} {
		application := plan.Application{Declaration: config.Application{Source: config.AUR, Identifier: "example"}, AURSource: plan.AURSource{Commit: bootstrapCommit, Metadata: aurmeta.Metadata{PackageBase: "example"}}, AUROutputs: []string{"example"}}
		if extra {
			application.AURSigningKeys = []string{fingerprint}
			application.AUROutputs = append(application.AUROutputs, "example-libs")
		}
		var out bytes.Buffer
		err := (Runtime{Out: &out}).reviewAUR(context.Background(), ui.UI{In: strings.NewReader("\n\n"), Out: &out}, application, map[string]string{"PKGBUILD": "source"})
		if !errors.Is(err, errReviewDeclined) {
			t.Fatalf("default-no err=%v", err)
		}
		text := out.String()
		approval := strings.Index(text, "Build and install example? [y/N]")
		for _, want := range []string{"Package: example\n", "Revision: " + bootstrapCommit, "Reviewed build instructions will run as your normal user and can access your files."} {
			if at := strings.Index(text, want); at < 0 || at >= approval {
				t.Fatalf("missing before approval %q: %s", want, text)
			}
		}
		if strings.Contains(text, "Package base:") {
			t.Fatal("redundant base")
		}
		for _, want := range []string{"Import public signing keys into your GnuPG keyring:", fingerprint, "Also install required outputs from this package base:", "example-libs"} {
			at := strings.Index(text, want)
			if (at >= 0) != extra || extra && at >= approval {
				t.Fatalf("incorrect consequence %q: %s", want, text)
			}
		}
	}
}

func TestReportKeepsActionableErrorsAndEscapesTerminalControls(t *testing.T) {
	var out bytes.Buffer
	Runtime{Out: &out}.report("ready", "ready", "failed", []issue{
		{State: "Failed", Name: "example", Cause: "missing\nnext\x1b[31m", Impact: "not installed", Action: "fix declaration"},
	})
	for _, want := range []string{"missing\n", "next\\x1b[31m", "not installed", "fix declaration", "Workstation setup incomplete."} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, &out)
		}
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "\nFinal\n") {
		t.Fatalf("unsafe/noisy error: %s", &out)
	}
	var fatal bytes.Buffer
	if code := (Runtime{Err: &fatal}).fatal(errors.New("useful stderr")); code != Fatal || !strings.Contains(fatal.String(), "useful stderr") || !strings.Contains(fatal.String(), "run ops again") {
		t.Fatalf("fatal error lost detail: %s", &fatal)
	}
}

func assertConciseOutput(t *testing.T, output string) {
	t.Helper()
	for _, hidden := range []string{"\nPlan\n", "\nProgress\n", "\nReview\n", "\nFinal\n", ".SRCINFO", ".PKGINFO", " -> ", "external", "install reason", "confirm transaction in pacman", "ops-aur-", "ops-paru-"} {
		if strings.Contains("\n"+output, hidden) {
			t.Fatalf("unexpected default output %q:\n%s", hidden, output)
		}
	}
}
func TestAURReviewShowsBuildInstructionsAndRequiresOneApproval(t *testing.T) {
	files := map[string]string{
		"PKGBUILD":           "source setup.sh\n",
		".SRCINFO":           "internal metadata\n",
		"setup.sh":           "echo build\n",
		"package.install":    "post_install() { echo install; }\n",
		"other-instructions": "echo extra\x1b[31m\n",
	}
	for _, answer := range []string{"y\n", "\n", "n\n", ""} {
		t.Run(fmt.Sprintf("%q", answer), func(t *testing.T) {
			var out, review bytes.Buffer
			err := (Runtime{Out: &out}).reviewAUR(context.Background(), ui.UI{In: strings.NewReader("\n\n\n\n" + answer), Out: &review}, plan.Application{Declaration: config.Application{Identifier: "example-bin"}, AURSource: plan.AURSource{Commit: bootstrapCommit, Metadata: aurmeta.Metadata{PackageBase: "example"}}}, files)
			if (err == nil) != (answer == "y\n") {
				t.Fatalf("answer=%q err=%v", answer, err)
			}
			if out.String() != "Reviewing example-bin...\n" || !strings.HasPrefix(review.String(), "AUR source review (1/4) - untrusted build instructions\nPackage: example-bin\nPackage base: example\nRevision: "+bootstrapCommit+"\n\nPKGBUILD\n\nsource setup.sh\n") {
				t.Fatalf("PKGBUILD not first: %s", &out)
			}
			for _, file := range []string{"setup.sh", "package.install", "other-instructions"} {
				if !strings.Contains(review.String(), "\n"+file+"\n") {
					t.Fatalf("build instructions hidden: %s", file)
				}
			}
			if strings.Count(review.String(), "?") != 1 || !strings.HasSuffix(review.String(), "Build and install example-bin? [y/N] ") {
				t.Fatalf("redundant or unsafe approval: %s", &out)
			}
			assertConciseOutput(t, out.String())
			if strings.Contains(review.String(), ".SRCINFO") {
				t.Fatal("metadata appeared in source review")
			}
			if strings.Contains(review.String(), "\x1b") || !strings.Contains(review.String(), "\\x1b") {
				t.Fatalf("unsafe review: %s", &out)
			}
			if files[".SRCINFO"] != "internal metadata\n" || !strings.Contains(files["other-instructions"], "\x1b") {
				t.Fatal("presentation changed raw verification inputs")
			}
		})
	}
	var out bytes.Buffer
	if err := (Runtime{Out: &out}).reviewAUR(context.Background(), ui.UI{}, plan.Application{Declaration: config.Application{Identifier: "example"}}, map[string]string{".SRCINFO": "metadata"}); err == nil {
		t.Fatal("missing PKGBUILD accepted")
	}
}

func TestFlatpakOnlyPlanDoesNotPromiseSystemWork(t *testing.T) {
	state := readyExecutionState()
	cfg := config.Config{Version: 2, Applications: []config.Application{{Source: config.Flatpak, Identifier: "org.example.App"}}}
	p := resolveAndPlan(context.Background(), cfg, state, outputResolver{flatpak: map[string]bool{"org.example.App": true}})
	if p.FullUpgrade || p.AddFlathub || len(p.CorePackages) != 0 {
		t.Fatalf("not a Flatpak-only fixture: %#v", p)
	}
	var out bytes.Buffer
	Runtime{Out: &out}.showPlan(p)
	if out.String() != "Workstation setup\n\nInstall\n  org.example.App (Flatpak)\n\n" {
		t.Fatalf("misleading summary: %s", &out)
	}
	runner := &prepareRunner{}
	out.Reset()
	code := (Runtime{Out: &out, Err: &out, Runner: runner}).executeForTest(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &out})
	if code != Success || strings.Contains(out.String(), "Updating system") {
		t.Fatalf("code=%d output=%s", code, &out)
	}
	for _, call := range runner.calls {
		if call.Name == "sudo" {
			t.Fatalf("Flatpak-only plan requested privilege: %#v", call)
		}
	}
}

func TestPlanSummaryOnlyAnnouncesPlannedSystemWork(t *testing.T) {
	for _, test := range []struct {
		p                                  plan.Plan
		update, dependencies, repositories bool
	}{
		{p: plan.Plan{ConfigureGit: true}},
		{p: plan.Plan{AddFlathub: true}},
		{p: plan.Plan{FullUpgrade: true}, update: true},
		{p: plan.Plan{CorePackages: []string{"git"}}, dependencies: true},
		{p: plan.Plan{EnableMultilib: true}, repositories: true},
		{p: plan.Plan{Applications: []plan.Application{{Declaration: config.Application{Source: config.AUR, Identifier: "example"}, State: plan.Unavailable, AURPackages: []plan.BuildPackage{{Name: "unused"}}}}}},
	} {
		var out bytes.Buffer
		Runtime{Out: &out}.showPlan(test.p)
		if strings.Contains(out.String(), "system will be updated") != test.update ||
			strings.Contains(out.String(), "Required dependencies") != test.dependencies ||
			strings.Contains(out.String(), "Enable multilib.") != test.repositories {
			t.Fatalf("plan=%#v output=%s", test.p, &out)
		}
	}
}

func TestIncompleteStatusNeverPrintsReady(t *testing.T) {
	var out bytes.Buffer
	Runtime{Out: &out}.report("failed", "ready", "ready", nil)
	if strings.Contains(out.String(), "Workstation ready.") || !strings.Contains(out.String(), "Run ops doctor") {
		t.Fatalf("output=%s", &out)
	}
}

func TestServiceProgressIsOnePhaseAndVerifiesEveryService(t *testing.T) {
	var output bytes.Buffer
	runner := &prepareRunner{}
	application := plan.Application{Declaration: config.Application{Source: "pacman", Identifier: "example"}, Services: []string{"first.service", "second.service"}}
	err := (Runtime{Runner: runner, Out: &output}).configureServices(context.Background(), application)
	if err != nil || output.String() != "Configuring services for example...\n" {
		t.Fatalf("err=%v output=%s", err, &output)
	}
	for _, service := range application.Services {
		var calls []string
		for _, call := range runner.calls {
			if call.Args[len(call.Args)-1] == service {
				calls = append(calls, call.Name+" "+strings.Join(call.Args, " "))
			}
		}
		want := "sudo -n systemctl enable --now " + service + ",systemctl is-enabled " + service + ",systemctl is-active " + service
		if strings.Join(calls, ",") != want {
			t.Fatalf("service=%s calls=%v", service, calls)
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
		Explicit: map[string]bool{
			"git": true, "openssh": true, "github-cli": true, "flatpak": true, "base-devel": true,
			"librewolf-bin": true, "mullvad-browser-bin": true, "mullvad-vpn": true,
			"discord": true, "spotify-launcher": true, "steam": true,
		},
		Services:        map[string]bool{"mullvad-daemon.service": true},
		OfficialMatches: map[string]string{"git": "extra/git", "openssh": "core/openssh", "github-cli": "extra/github-cli", "flatpak": "extra/flatpak", "mullvad-vpn": "extra/mullvad-vpn", "discord": "extra/discord", "spotify-launcher": "extra/spotify-launcher", "steam": "multilib/steam"},
		Foreign:         map[string]bool{"librewolf-bin": true, "mullvad-browser-bin": true},
		Flatpaks:        map[string]string{}, Flathub: flatpak.Remote{Name: "flathub", URL: flatpak.FlathubRepositoryURL, Enabled: true}, Multilib: true,
		GitName: "User", GitEmail: "user@example.com",
		ManagedSSHIdentity: true, UnrelatedSSHIdentities: 1,
	}
	resolver := outputResolver{
		pacman:  map[string]plan.Package{"bitwarden": {Name: "bitwarden", Repository: "extra"}},
		flatpak: map[string]bool{"com.tutanota.Tutanota": true},
	}
	p := resolveAndPlan(context.Background(), config.Config{Version: 2, Applications: applications}, state, resolver)
	return p
}

type outputResolver struct {
	pacman  map[string]plan.Package
	aur     map[string]plan.Package
	flatpak map[string]bool
	source  *plan.AURSource
	deps    map[string]plan.OfficialDependency
}

func (r outputResolver) Pacman(_ context.Context, name string) (plan.Package, bool, error) {
	pkg, ok := r.pacman[name]
	if ok && pkg.Repository == "" {
		pkg.Repository = "extra"
	}
	return pkg, ok, nil
}

func (r outputResolver) AUR(_ context.Context, name string) (plan.Package, bool, error) {
	pkg, ok := r.aur[name]
	return pkg, ok, nil
}

func (r outputResolver) AURSource(_ context.Context, _ string) (plan.AURSource, bool, error) {
	if r.source != nil {
		return *r.source, true, nil
	}
	return plan.AURSource{Commit: "0123456789012345678901234567890123456789", Metadata: aurmeta.Metadata{
		PackageBase: "paru", Version: "1.0.0-1", Packages: []aurmeta.Package{{Name: "paru"}},
	}}, true, nil
}

func (r outputResolver) OfficialDependency(_ context.Context, requirement string) (plan.OfficialDependency, error) {
	if dependency, ok := r.deps[requirement]; ok {
		return dependency, nil
	}
	return plan.OfficialDependency{Requirement: requirement, Provider: "extra/" + requirement, Packages: []string{"extra/" + requirement}, Satisfied: true}, nil
}
func (r outputResolver) UserPGPKey(_ context.Context, _ string) (bool, error) { return true, nil }

func (r outputResolver) CompareVersions(_ context.Context, _, _ string) (int, error) { return 0, nil }

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

func resolveAndPlan(ctx context.Context, cfg config.Config, state plan.State, resolver resolve.MetadataResolver) plan.Plan {
	return plan.Build(cfg, state, resolve.Applications(ctx, cfg, state, resolver))
}
