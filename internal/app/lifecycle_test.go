package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/resolve"
	"github.com/luigiverona/ops/internal/run"
	sshops "github.com/luigiverona/ops/internal/ssh"
	"github.com/luigiverona/ops/internal/ui"
)

// This model persists package, identity, and remote-key changes. Unknown or
// unavailable commands fail; it cannot make the original Git cycle disappear.
type lifecycleRunner struct {
	prepareRunner
	installed           map[string]bool
	events              []string
	upgraded            bool
	loseGitAtInspection bool
}

func (r *lifecycleRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	args := strings.Join(s.Args, " ")
	r.events = append(r.events, s.Name+" "+args)
	if s.Name == "uname" {
		return run.Result{Stdout: "x86_64\n"}, nil
	}
	if s.Name == "pacman" {
		switch s.Args[0] {
		case "-Qq", "-Qeq":
			if r.loseGitAtInspection && r.upgraded {
				delete(r.installed, "git")
			}
			var names []string
			for name := range r.installed {
				names = append(names, name)
			}
			sort.Strings(names)
			return run.Result{Stdout: strings.Join(names, "\n")}, nil
		case "-Qqm":
			return run.Result{}, nil
		case "-Q":
			for _, name := range s.Args[1:] {
				if !r.installed[name] {
					return run.Result{}, errors.New("package missing")
				}
			}
			return run.Result{}, nil
		}
	}
	if s.Name == "sudo" {
		if strings.HasPrefix(args, "-n pacman -Syu") {
			r.upgraded = true
		}
		if strings.HasPrefix(args, "-n pacman -S ") {
			if !r.upgraded {
				return run.Result{}, errors.New("install before full upgrade")
			}
			_, pkgs, _ := strings.Cut(args, " -- ")
			for _, name := range strings.Fields(pkgs) {
				_, bare, qualified := strings.Cut(name, "/")
				if qualified {
					name = bare
				}
				r.installed[name] = true
			}
		}
		return run.Result{}, nil
	}
	owner := map[string]string{"git": "git", "gh": "github-cli", "ssh": "openssh", "ssh-keygen": "openssh", "ssh-add": "openssh", "flatpak": "flatpak", "paru": "paru", "makepkg": "base-devel"}[s.Name]
	if owner != "" && !r.installed[owner] {
		return run.Result{}, fmt.Errorf("exec: %q: executable file not found in $PATH", s.Name)
	}
	if s.Name == "ssh" && s.Args[0] == "-G" {
		return run.Result{Stdout: "host github.com\nuser git\nhostname github.com\nidentitiesonly yes\nstricthostkeychecking true\nidentityfile " + filepath.Join(r.home, ".ssh", "ops") + "\nuserknownhostsfile " + filepath.Join(r.home, ".ssh", "ops_known_hosts") + "\n"}, nil
	}
	if s.Name == "ssh-add" {
		return run.Result{Stderr: "Could not open a connection to your authentication agent."}, errors.New("unavailable")
	}
	if s.Name == "gh" && strings.HasPrefix(args, "ssh-key add ") {
		r.remoteKeys = fmt.Sprintf(`[{"id":1,"title":"managed","key":%q}]`, r.sshPublicKey)
		return run.Result{}, nil
	}
	if s.Name == "gh" && args == "config get user --host github.com" {
		if r.authenticated {
			return run.Result{Stdout: "User\n"}, nil
		}
		return run.Result{}, nil
	}
	return r.prepareRunner.Run(ctx, s)
}

func testPacmanConf(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pacman.conf")
	if err := os.WriteFile(path, []byte("[options]\nArchitecture = auto\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func minimalRuntime(t *testing.T) (Runtime, *lifecycleRunner, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	key := wirePublic(27)
	fingerprint, _ := sshops.PublicFingerprint(key)
	runner := &lifecycleRunner{installed: map[string]bool{}, prepareRunner: prepareRunner{home: home, sshPublicKey: key, sshFingerprint: fingerprint}}
	hostKey := strings.Fields(wirePublic(28))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ssh_keys":["`+hostKey[0]+` `+hostKey[1]+`"]}`)
	}))
	t.Cleanup(server.Close)
	out := &bytes.Buffer{}
	return Runtime{Home: home, Runner: runner, Out: out, Err: out, EUID: func() int { return 1000 }, OSRelease: archOSRelease(t), PacmanConf: testPacmanConf(t), SSHHTTP: server.Client(), SSHMetadataURL: server.URL}, runner, out
}

func TestMinimalArchDoctorWithoutConfigurationOrTools(t *testing.T) {
	a, r, out := minimalRuntime(t)
	for _, withConfig := range []bool{false, true} {
		if withConfig {
			if _, err := config.EnsureDefault(config.Path(a.Home)); err != nil {
				t.Fatal(err)
			}
		}
		out.Reset()
		r.events = nil
		if code := a.Doctor(context.Background()); code != Issues {
			t.Fatalf("doctor=%d\n%s", code, out.String())
		}
		for _, event := range r.events {
			if !strings.HasPrefix(event, "pacman -Q") && event != "uname -m" {
				t.Fatalf("doctor invoked missing tool or mutation: %s", event)
			}
		}
		for _, name := range []string{"git", "ssh", "github"} {
			if !strings.Contains(out.String(), name) {
				t.Fatalf("missing diagnostic %s", name)
			}
		}
		if !withConfig {
			if !strings.Contains(out.String(), config.Path(a.Home)) || !strings.Contains(out.String(), "is missing; create a file using format 2") {
				t.Fatalf("missing config guidance: %s", out)
			}
			if _, err := os.Stat(config.Path(a.Home)); !os.IsNotExist(err) {
				t.Fatal("doctor created config")
			}
		}
	}
}

func TestMinimalFirstRunConvergesAndSecondRunIsNoOp(t *testing.T) {
	ctx := context.Background()
	a, r, out := minimalRuntime(t)
	if _, err := config.EnsureDefault(config.Path(a.Home)); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.Path(a.Home))
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.inspectState(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	facts := resolve.Applications(ctx, cfg, state, resolve.Resolver{Runner: r})
	p := plan.Build(cfg, state, facts)
	if strings.Join(p.CorePackages, ",") != "git,github-cli,openssh" {
		t.Fatalf("prerequisites=%v", p.CorePackages)
	}
	if code := a.preparePlan(ctx, cfg, p, ui.UI{In: strings.NewReader("y\nExample User\nuser@example.com\n"), Out: out}); code != Success {
		t.Fatalf("first run=%d\n%s\n%v", code, out.String(), r.events)
	}
	events := strings.Join(r.events, "\n")
	upgrade := strings.Index(events, "sudo -n pacman -Syu")
	install := strings.Index(events, "sudo -n pacman -S --noconfirm -- extra/git extra/github-cli core/openssh")
	verified := strings.Index(events, "pacman -Qi -- git")
	dependent := strings.Index(events, "git config")
	final := strings.LastIndex(events, "pacman -Qq")
	if !(upgrade >= 0 && install > upgrade && verified > install && dependent > verified && final > dependent) {
		t.Fatalf("invalid lifecycle order:\n%s", events)
	}
	assertConciseOutput(t, out.String())
	if !strings.HasSuffix(out.String(), "Workstation ready.\n") {
		t.Fatalf("missing final success: %s", out)
	}
	out.Reset()
	if code := a.Doctor(ctx); code != Success || out.String() != "Workstation healthy.\n" {
		t.Fatalf("doctor=%d\n%s", code, out.String())
	}
	r.events = nil
	out.Reset()
	if code := a.Prepare(ctx); code != Success || out.String() != "Workstation already ready.\n" {
		t.Fatalf("second run=%d\n%s", code, out.String())
	}
	for _, event := range r.events {
		if strings.HasPrefix(event, "sudo ") || strings.Contains(event, "auth login") || strings.Contains(event, "ssh-key add") {
			t.Fatalf("no-op mutated: %s", event)
		}
	}
}

func TestFinalInspectionDoesNotTrustSuccessfulMutations(t *testing.T) {
	a, r, out := minimalRuntime(t)
	r.loseGitAtInspection = true
	cfg := config.Config{Version: 2}
	p := plan.Build(cfg, plan.State{}, nil)
	code := a.preparePlan(context.Background(), cfg, p, ui.UI{In: strings.NewReader("y\nUser\nuser@example.com\n"), Out: out})
	if code != Issues || !strings.Contains(out.String(), "required component is missing") || strings.Contains(out.String(), "Workstation ready.") {
		t.Fatalf("unverified success=%d\n%s", code, out.String())
	}
}

func TestInvalidConfigurationStopsBeforeWorkstationInspection(t *testing.T) {
	for _, command := range []string{"ops", "doctor"} {
		for _, data := range []string{"pacman=[]", "version=1", "version=0", "version=-1", "version=3", "version=\"2\"", "version=2\nunknown=[]", "version=[", "version=2\nflatpak=[\"invalid\"]", "version=2\npacman=[\"--option\"]"} {
			t.Run(command+"/"+data, func(t *testing.T) {
				a, runner, out := minimalRuntime(t)
				path := config.Path(a.Home)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
				run := a.Prepare
				if command == "doctor" {
					run = a.Doctor
				}
				if code := run(context.Background()); code != Fatal || !strings.Contains(out.String(), path) {
					t.Fatalf("code=%d, output=%s", code, out)
				}
				if got := strings.Join(runner.events, "\n"); got != "uname -m" {
					t.Fatalf("commands before config rejection: %s", got)
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != data {
					t.Fatalf("configuration changed: %q, %v", got, err)
				}
				entries, err := os.ReadDir(a.Home)
				if err != nil || len(entries) != 1 || entries[0].Name() != ".config" {
					t.Fatalf("home changed: %v, %v", entries, err)
				}
			})
		}
	}
}

func TestPrepareMissingConfigurationDoesNotCreateFiles(t *testing.T) {
	a, runner, out := minimalRuntime(t)
	if code := a.Prepare(context.Background()); code != Fatal || !strings.Contains(out.String(), config.Path(a.Home)) || !strings.Contains(out.String(), "is missing; create a file using format 2") {
		t.Fatalf("code=%d, output=%s", code, out)
	}
	if got := strings.Join(runner.events, "\n"); got != "uname -m" {
		t.Fatalf("commands before config rejection: %s", got)
	}
	entries, err := os.ReadDir(a.Home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("home changed: %v, %v", entries, err)
	}
}
