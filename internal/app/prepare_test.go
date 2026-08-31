package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	sshops "github.com/luigiverona/ops/internal/ssh"
	"github.com/luigiverona/ops/internal/ui"
)

type prepareRunner struct {
	calls          []run.Spec
	failUpgrade    bool
	failFlatpak    string
	sshFingerprint string
	authenticated  bool
	remoteKeys     string
	failKeyAPI     bool
	home           string
	sshPublicKey   string
	gitName        string
	gitEmail       string
}

func (f *prepareRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, spec)
	joined := strings.Join(spec.Args, " ")
	if f.failUpgrade && spec.Name == "sudo" && strings.Contains(joined, "pacman -Syu") {
		return run.Result{}, errors.New("upgrade failed")
	}
	if f.failFlatpak != "" && spec.Name == "flatpak" && len(spec.Args) > 0 && spec.Args[0] == "install" && strings.Contains(joined, f.failFlatpak) {
		return run.Result{}, errors.New("flatpak install failed")
	}
	if spec.Name == "flatpak" && len(spec.Args) > 0 && spec.Args[0] == "remotes" {
		return run.Result{Stdout: "flathub\n"}, nil
	}
	if spec.Name == "git" && len(spec.Args) >= 4 && spec.Args[0] == "config" && spec.Args[1] == "--global" {
		switch {
		case spec.Args[2] == "--get" && spec.Args[3] == "user.name":
			return run.Result{Stdout: f.gitName + "\n"}, nil
		case spec.Args[2] == "--get" && spec.Args[3] == "user.email":
			return run.Result{Stdout: f.gitEmail + "\n"}, nil
		case spec.Args[2] == "user.name":
			f.gitName = spec.Args[3]
			return run.Result{}, nil
		case spec.Args[2] == "user.email":
			f.gitEmail = spec.Args[3]
			return run.Result{}, nil
		}
	}
	if spec.Name == "ssh-keygen" && len(spec.Args) > 0 && spec.Args[0] == "-t" && f.home != "" {
		if !spec.Interactive {
			return run.Result{}, errors.New("ssh-keygen identity creation was not interactive")
		}
		path := filepath.Join(f.home, ".ssh", "ops")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return run.Result{}, err
		}
		if err := os.WriteFile(path, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nfixture\n"), 0o600); err != nil {
			return run.Result{}, err
		}
		if err := os.WriteFile(path+".pub", []byte(f.sshPublicKey+"\n"), 0o644); err != nil {
			return run.Result{}, err
		}
		return run.Result{}, nil
	}
	if spec.Name == "ssh-keygen" && f.sshFingerprint != "" {
		return run.Result{Stdout: "256 " + f.sshFingerprint + " ops (ED25519)\n"}, nil
	}
	if spec.Name == "gh" && len(spec.Args) > 1 && spec.Args[0] == "auth" && spec.Args[1] == "status" {
		if f.authenticated {
			return run.Result{}, nil
		}
		return run.Result{}, errors.New("not authenticated")
	}
	if spec.Name == "gh" && len(spec.Args) > 1 && spec.Args[0] == "auth" && spec.Args[1] == "login" {
		f.authenticated = true
		return run.Result{}, nil
	}
	if spec.Name == "gh" && len(spec.Args) > 0 && spec.Args[0] == "api" {
		if f.failKeyAPI {
			return run.Result{}, errors.New("GitHub key API unavailable")
		}
		if f.remoteKeys != "" {
			return run.Result{Stdout: f.remoteKeys}, nil
		}
		return run.Result{Stdout: "[]"}, nil
	}
	if spec.Name == "ssh" && len(spec.Args) > 0 && spec.Args[0] == "-T" {
		return run.Result{Stderr: "successfully authenticated"}, errors.New("exit status 1")
	}
	return run.Result{}, nil
}

func TestPreparePlanDeclineRendersBeforeConfirmationAndDoesNotMutate(t *testing.T) {
	p := plan.Plan{Core: readyCore(), FullUpgrade: true, GitStatus: "ready", SSHStatus: "ready", GitHubStatus: "ready"}
	var output bytes.Buffer
	runner := &prepareRunner{}
	runtime := Runtime{Runner: runner, Out: &output, Err: &output}
	code := runtime.preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("n\n"), Out: &output})
	if code != Success || len(runner.calls) != 0 {
		t.Fatalf("code=%d calls=%#v", code, runner.calls)
	}
	text := output.String()
	if strings.Index(text, "Plan\n") < 0 || strings.Index(text, "Plan\n") > strings.Index(text, "Prepare this workstation?") {
		t.Fatalf("plan was not rendered before confirmation:\n%s", text)
	}
	if strings.Count(text, "Prepare this workstation?") != 1 {
		t.Fatalf("top-level confirmations=%d\n%s", strings.Count(text, "Prepare this workstation?"), text)
	}
	if !strings.Contains(text, "full system upgrade  upgrade  pacman; confirm transaction in pacman") {
		t.Fatalf("plan did not disclose pacman's transaction boundary:\n%s", text)
	}
}

func TestPreparePlanProgressMatchesMutationOrder(t *testing.T) {
	state := readyExecutionState()
	resolver := outputResolver{pacman: map[string]plan.Package{
		"mullvad-vpn":        {Name: "mullvad-vpn", Optional: []string{"example-dependency"}},
		"example-dependency": {Name: "example-dependency"},
	}}
	p, err := plan.Build(context.Background(), config.Config{Version: 1, Applications: []config.Application{{Identifier: "mullvad-vpn", Source: "pacman"}}}, state, resolver)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	runner := &prepareRunner{}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &output})
	if code != Success {
		t.Fatalf("code=%d\n%s", code, output.String())
	}
	wantProgress := []string{
		"full system upgrade|upgrade|pacman; confirm transaction in pacman",
		"mullvad-vpn -> example-dependency|install|pacman",
		"mullvad-vpn|install|pacman",
		"mullvad-vpn -> mullvad-daemon.service|enable|systemd",
	}
	if got := progressRecords(output.String()); strings.Join(got, "\n") != strings.Join(wantProgress, "\n") {
		t.Fatalf("progress records=%v, want=%v\n%s", got, wantProgress, output.String())
	}
	if got := mutationOrder(runner.calls); strings.Join(got, ",") != "upgrade,dependency,application,service" {
		t.Fatalf("mutation order=%v", got)
	}
	if planAt, confirmAt, progressAt := strings.Index(output.String(), "Plan\n"), strings.Index(output.String(), "Prepare this workstation?"), strings.Index(output.String(), "\nProgress\n"); planAt < 0 || confirmAt <= planAt || progressAt <= confirmAt {
		t.Fatalf("plan/confirmation/progress boundary is out of order:\n%s", output.String())
	}
	if strings.Count(output.String(), "Prepare this workstation?") != 1 {
		t.Fatalf("top-level confirmation count is not one:\n%s", output.String())
	}
}

func readyExecutionState() plan.State {
	return plan.State{
		Installed: map[string]bool{"git": true, "openssh": true, "github-cli": true, "flatpak": true, "base-devel": true},
		Foreign:   map[string]bool{}, Flatpaks: map[string]bool{}, Paru: true, Flathub: true, Multilib: true,
		GitName: "User", GitEmail: "user@example.com", ManagedSSHIdentity: true, SSHConfigurationReady: true,
		SSHHostKeyFreshness: plan.SSHHostKeyFreshnessCurrent,
		GitHubAuth:          true, GitHubKeysKnown: true, ManagedGitHubKeyKnown: true, ManagedGitHubKey: true,
	}
}

func progressRecords(output string) []string {
	lines := strings.Split(output, "\n")
	columns := regexp.MustCompile(` {2,}`)
	var records []string
	for i := 0; i < len(lines); i++ {
		if lines[i] != "Progress" {
			continue
		}
		for i++; i < len(lines) && strings.HasPrefix(lines[i], "  "); i++ {
			fields := columns.Split(strings.TrimSpace(lines[i]), 3)
			if len(fields) != 3 {
				continue
			}
			records = append(records, strings.Join(fields, "|"))
		}
	}
	return records
}

func TestPreparePlanAllReadyHasNoMutationOrProgress(t *testing.T) {
	state := readyExecutionState()
	state.Installed["bitwarden"] = true
	state.Flatpaks["com.tutanota.Tutanota"] = true
	p, err := plan.Build(context.Background(), config.Config{Version: 1, Applications: []config.Application{
		{Identifier: "bitwarden", Source: "pacman"},
		{Identifier: "com.tutanota.Tutanota", Source: "flatpak"},
	}}, state, outputResolver{})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	runner := &prepareRunner{}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &output})
	if code != Success {
		t.Fatalf("code=%d\n%s", code, output.String())
	}
	if !strings.Contains(output.String(), "No changes\n  workstation is already ready") || strings.Contains(output.String(), "\nProgress\n") {
		t.Fatalf("all-ready output is misleading:\n%s", output.String())
	}
	if mutations := mutationOrder(runner.calls); len(mutations) != 0 {
		t.Fatalf("all-ready plan mutated state: %v", mutations)
	}
	for _, call := range runner.calls {
		if call.Name == "ssh-add" || call.Name == "ssh-keygen" || call.Name == "gh" || call.Name == "ssh" {
			t.Fatalf("all-ready plan executed SSH/GitHub stage: %#v", call)
		}
	}
}

func TestPreparePlanContinuesUnrelatedWorkWhenHostKeyFreshnessUnavailable(t *testing.T) {
	p := plan.Plan{
		Core: readyCore(), AddFlathub: true,
		GitStatus: "ready", SSHStatus: "unavailable", GitHubStatus: "ready",
		SSHHostKeyFreshness: plan.SSHHostKeyFreshnessUnavailable,
	}
	var output bytes.Buffer
	runner := &prepareRunner{}
	code := (Runtime{Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &output})
	if code != Success {
		t.Fatalf("code=%d\n%s", code, output.String())
	}
	if !strings.Contains(output.String(), "GitHub SSH host-key freshness  unavailable  retry later") ||
		!strings.Contains(output.String(), "flathub  enable  Flatpak remote") {
		t.Fatalf("unavailable check hid or blocked unrelated work:\n%s", output.String())
	}
	for _, call := range runner.calls {
		if call.Name == "ssh" || call.Name == "ssh-keygen" || call.Name == "ssh-add" {
			t.Fatalf("unavailable freshness invented SSH work: %#v", call)
		}
	}
}

func TestPreparePlanUnauthenticatedGitHubReconcilesOnlyAfterConfirmation(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	publicKey := wirePublic(9)
	fingerprint, err := sshops.PublicFingerprint(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "ops"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nfixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "ops.pub"), []byte(publicKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := readyExecutionState()
	state.GitHubAuth, state.GitHubKeysKnown, state.ManagedGitHubKeyKnown, state.ManagedGitHubKey = false, false, false, false
	p, err := plan.Build(context.Background(), config.Config{Version: 1}, state, outputResolver{})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("authenticate and register", func(t *testing.T) {
		var output bytes.Buffer
		runner := &prepareRunner{sshFingerprint: fingerprint}
		code := (Runtime{Home: home, Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &output})
		if code != Success {
			t.Fatalf("code=%d\n%s", code, output.String())
		}
		want := []string{"github|authenticate|CLI login", "GitHub SSH key|configure|managed key"}
		if got := progressRecords(output.String()); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("progress=%v, want=%v\n%s", got, want, output.String())
		}
		if got := mutationOrder(runner.calls); strings.Join(got, ",") != "github-authenticate,github-key" {
			t.Fatalf("mutations=%v", got)
		}
		loginInteractive := false
		for _, call := range runner.calls {
			if call.Name == "gh" && len(call.Args) > 1 && call.Args[0] == "auth" && call.Args[1] == "login" {
				loginInteractive = call.Interactive
			}
		}
		if !loginInteractive {
			t.Fatal("gh auth login did not retain its interactive stream")
		}
		if !strings.Contains(output.String(), "GitHub SSH keys") || !strings.Contains(output.String(), "existing keys after login, if present") {
			t.Fatalf("unknown remote-key state was not planned:\n%s", output.String())
		}
		for _, prompt := range []string{"Authenticate GitHub CLI?", "Register ~/.ssh/ops.pub with GitHub?"} {
			if strings.Contains(output.String(), prompt) {
				t.Fatalf("redundant authorization prompt %q remains:\n%s", prompt, output.String())
			}
		}
	})
}

func TestApprovedGitAndSSHActionsDoNotAskForSecondAuthorization(t *testing.T) {
	t.Run("git values remain interactive", func(t *testing.T) {
		p := plan.Plan{Core: readyCore(), ConfigureGit: true, SSHStatus: "ready", GitHubStatus: "ready"}
		var output bytes.Buffer
		runner := &prepareRunner{}
		code := (Runtime{Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\nExample User\nuser@example.com\n"), Out: &output})
		if code != Success {
			t.Fatalf("code=%d\n%s", code, output.String())
		}
		if strings.Contains(output.String(), "Configure missing Git identity values?") || strings.Count(output.String(), "Prepare this workstation?") != 1 {
			t.Fatalf("redundant Git authorization remains:\n%s", output.String())
		}
		if !strings.Contains(output.String(), "Git user.name:") || !strings.Contains(output.String(), "Git user.email:") {
			t.Fatalf("required Git value input disappeared:\n%s", output.String())
		}
	})

	t.Run("ssh-keygen retains external interaction", func(t *testing.T) {
		home := t.TempDir()
		publicKey := wirePublic(7)
		fingerprint, err := sshops.PublicFingerprint(publicKey)
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		runner := &prepareRunner{home: home, sshPublicKey: publicKey, sshFingerprint: fingerprint}
		status, managed, issues, fatal := (Runtime{Home: home, Runner: runner, Out: &output, Err: &output}).configureSSH(context.Background(), ui.UI{In: strings.NewReader(""), Out: &output}, plan.Plan{CreateSSHIdentity: true})
		if fatal != nil || len(issues) != 0 || status != "ready" || managed == nil {
			t.Fatalf("status=%s managed=%#v issues=%v fatal=%v\n%s", status, managed, issues, fatal, output.String())
		}
		if strings.Contains(output.String(), "Create the managed") {
			t.Fatalf("redundant SSH authorization remains:\n%s", output.String())
		}
		created := false
		for _, call := range runner.calls {
			if call.Name == "ssh-keygen" && len(call.Args) > 0 && call.Args[0] == "-t" {
				created = call.Interactive
			}
		}
		if !created {
			t.Fatal("ssh-keygen creation did not retain its interactive stream")
		}
	})
}

func TestPreparePlanPostLoginKeyInspectionFailsClosed(t *testing.T) {
	home, fingerprint, p := unauthenticatedGitHubFixture(t)
	var output bytes.Buffer
	runner := &prepareRunner{sshFingerprint: fingerprint, failKeyAPI: true}
	code := (Runtime{Home: home, Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &output})
	if code != Issues {
		t.Fatalf("code=%d\n%s", code, output.String())
	}
	if got := mutationOrder(runner.calls); strings.Join(got, ",") != "github-authenticate" {
		t.Fatalf("remote failure caused unsafe mutation: %v", got)
	}
	if strings.Contains(strings.Join(progressRecords(output.String()), "\n"), "GitHub SSH key|configure") {
		t.Fatalf("registration progress preceded successful reconciliation:\n%s", output.String())
	}
}

func TestPreparePlanPostLoginExistingManagedKeySkipsRegistration(t *testing.T) {
	home, fingerprint, p := unauthenticatedGitHubFixture(t)
	publicKey, err := os.ReadFile(filepath.Join(home, ".ssh", "ops.pub"))
	if err != nil {
		t.Fatal(err)
	}
	remote := `[{"id":1,"title":"managed","key":` + strconv.Quote(strings.TrimSpace(string(publicKey))) + `}]`
	var output bytes.Buffer
	runner := &prepareRunner{sshFingerprint: fingerprint, remoteKeys: remote}
	code := (Runtime{Home: home, Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &output})
	if code != Success {
		t.Fatalf("code=%d\n%s", code, output.String())
	}
	if got := mutationOrder(runner.calls); strings.Join(got, ",") != "github-authenticate" {
		t.Fatalf("existing managed key was registered again: %v", got)
	}
	if strings.Contains(strings.Join(progressRecords(output.String()), "\n"), "GitHub SSH key|configure") {
		t.Fatalf("registration Progress emitted for an existing key:\n%s", output.String())
	}
}

func TestPreparePlanAuthenticatedMissingManagedKeyRegistersWithoutSecondAuthorization(t *testing.T) {
	home, fingerprint, _ := unauthenticatedGitHubFixture(t)
	state := readyExecutionState()
	state.ManagedGitHubKey = false
	p, err := plan.Build(context.Background(), config.Config{Version: 1}, state, outputResolver{})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	runner := &prepareRunner{sshFingerprint: fingerprint, authenticated: true}
	code := (Runtime{Home: home, Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &output})
	if code != Success || strings.Join(mutationOrder(runner.calls), ",") != "github-key" {
		t.Fatalf("code=%d mutations=%v\n%s", code, mutationOrder(runner.calls), output.String())
	}
	if strings.Contains(output.String(), "Register ~/.ssh/ops.pub with GitHub?") || strings.Count(output.String(), "Prepare this workstation?") != 1 {
		t.Fatalf("redundant registration authorization remains:\n%s", output.String())
	}

	state.ManagedGitHubKey = true
	second, err := plan.Build(context.Background(), config.Config{Version: 1}, state, outputResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if second.AuthenticateGitHub || second.ReviewGitHubKeys || second.ConfigureGitHubKey {
		t.Fatalf("successful reconciliation did not converge: %#v", second)
	}
}

func unauthenticatedGitHubFixture(t *testing.T) (string, string, plan.Plan) {
	t.Helper()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	publicKey := wirePublic(9)
	fingerprint, err := sshops.PublicFingerprint(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "ops"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nfixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "ops.pub"), []byte(publicKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := readyExecutionState()
	state.GitHubAuth, state.GitHubKeysKnown, state.ManagedGitHubKeyKnown, state.ManagedGitHubKey = false, false, false, false
	p, err := plan.Build(context.Background(), config.Config{Version: 1}, state, outputResolver{})
	if err != nil {
		t.Fatal(err)
	}
	return home, fingerprint, p
}

func TestPreparePlanPreservesFatalAndNonfatalFailureSemantics(t *testing.T) {
	t.Run("core failure stops", func(t *testing.T) {
		p := plan.Plan{Core: readyCore(), FullUpgrade: true, Applications: []plan.Application{{Declaration: config.Application{Identifier: "later", Source: "flatpak"}, State: "install"}}}
		var output bytes.Buffer
		runner := &prepareRunner{failUpgrade: true}
		code := (Runtime{Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &output})
		if code != Fatal || strings.Contains(strings.Join(mutationOrder(runner.calls), ","), "application") {
			t.Fatalf("code=%d mutations=%v\n%s", code, mutationOrder(runner.calls), output.String())
		}
	})

	t.Run("application failure continues", func(t *testing.T) {
		p := plan.Plan{Core: readyCore(), Applications: []plan.Application{
			{Declaration: config.Application{Identifier: "broken", Source: "flatpak"}, State: "install"},
			{Declaration: config.Application{Identifier: "working", Source: "flatpak"}, State: "install"},
		}, GitStatus: "ready", SSHStatus: "ready", GitHubStatus: "ready"}
		var output bytes.Buffer
		runner := &prepareRunner{failFlatpak: "broken"}
		code := (Runtime{Runner: runner, Out: &output, Err: &output}).preparePlan(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &output})
		if code != Issues || strings.Join(mutationOrder(runner.calls), ",") != "application,application" {
			t.Fatalf("code=%d mutations=%v\n%s", code, mutationOrder(runner.calls), output.String())
		}
		wantProgress := []string{"broken|install|flatpak", "working|install|flatpak"}
		if got := progressRecords(output.String()); strings.Join(got, "\n") != strings.Join(wantProgress, "\n") {
			t.Fatalf("progress=%v, want=%v\n%s", got, wantProgress, output.String())
		}
	})
}

func mutationOrder(calls []run.Spec) []string {
	var order []string
	for _, call := range calls {
		args := strings.Join(call.Args, " ")
		switch {
		case call.Name == "sudo" && strings.Contains(args, "pacman -Syu"):
			order = append(order, "upgrade")
		case call.Name == "sudo" && strings.Contains(args, "--asdeps"):
			order = append(order, "dependency")
		case call.Name == "sudo" && strings.Contains(args, "pacman -S --needed --noconfirm"):
			order = append(order, "application")
		case call.Name == "flatpak" && len(call.Args) > 0 && call.Args[0] == "install":
			order = append(order, "application")
		case call.Name == "sudo" && strings.Contains(args, "systemctl enable --now"):
			order = append(order, "service")
		case call.Name == "gh" && len(call.Args) > 1 && call.Args[0] == "auth" && call.Args[1] == "login":
			order = append(order, "github-authenticate")
		case call.Name == "gh" && len(call.Args) > 1 && call.Args[0] == "ssh-key" && call.Args[1] == "add":
			order = append(order, "github-key")
		}
	}
	return order
}
