package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/release"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
)

// Fake runners require an explicit fake mutation capability. Production never
// infers permission from a runner type or silently disables the ownership gate.
type testOwnership struct{}

func (testOwnership) Activate(context.Context) error { return nil }
func (testOwnership) Check() error                   { return nil }

type gateOwner struct {
	active                bool
	activationErr, poison error
	activations           int
}

func (o *gateOwner) Activate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.activations++
	if o.activationErr != nil {
		return o.activationErr
	}
	o.active = true
	return nil
}
func (o *gateOwner) Check() error {
	if o.poison != nil {
		return o.poison
	}
	if !o.active {
		return &run.OwnershipError{Err: errors.New("inactive")}
	}
	return nil
}

func TestReconcileOwnershipApprovalGate(t *testing.T) {
	for _, mode := range []string{"no-actions", "decline", "unavailable", "approved"} {
		t.Run(mode, func(t *testing.T) {
			owner := &gateOwner{}
			if mode == "unavailable" {
				owner.activationErr = &run.OwnershipError{Err: errors.New("no user manager")}
			}
			runner := &gateRunner{prepareRunner: &prepareRunner{}, owner: owner}
			var out strings.Builder
			a := Runtime{Ownership: owner, Runner: runner, Out: &out, Err: &out}
			p := plan.Plan{FullUpgrade: true}
			input := "y\n"
			if mode == "no-actions" {
				p = plan.Plan{GitStatus: "ready", SSHStatus: "ready", GitHubStatus: "ready"}
			}
			if mode == "decline" {
				input = "n\n"
			}
			result := a.executePlan(context.Background(), p, ui.UI{In: strings.NewReader(input), Out: &out})
			switch mode {
			case "no-actions", "decline":
				if owner.activations != 0 || len(runner.calls) != 0 {
					t.Fatal("read-only path required ownership")
				}
			case "unavailable":
				if result.status != Fatal || len(runner.calls) != 0 || owner.activations != 1 {
					t.Fatal("mutation or sudo preceded ownership", result, runner.calls)
				}
			case "approved":
				if owner.activations != 1 || !runner.sawSudo || runner.beforeActivation {
					t.Fatal("activation ordering failed")
				}
			}
		})
	}
}

type gateRunner struct {
	*prepareRunner
	owner                                    *gateOwner
	sawSudo, beforeActivation, poisonUpgrade bool
}

func (r *gateRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if !r.owner.active {
		r.beforeActivation = true
	}
	if s.Name == "sudo" {
		r.sawSudo = true
	}
	result, err := r.prepareRunner.Run(ctx, s)
	if r.poisonUpgrade && s.Name == "sudo" && strings.Contains(strings.Join(s.Args, " "), "-Syu") {
		r.owner.poison = &run.OwnershipError{Err: errors.New("owned subtree not empty")}
		return result, r.owner.poison
	}
	return result, err
}
func TestOwnershipPoisonStopsReconcileAndFileMutation(t *testing.T) {
	owner := &gateOwner{}
	runner := &gateRunner{prepareRunner: &prepareRunner{}, owner: owner, poisonUpgrade: true}
	var out strings.Builder
	a := Runtime{Ownership: owner, Runner: runner, Home: t.TempDir(), Out: &out, Err: &out}
	result := a.executePlan(context.Background(), plan.Plan{FullUpgrade: true, ConfigureGit: true, CreateSSHIdentity: true}, ui.UI{In: strings.NewReader("y\nUser\nuser@example.com\n"), Out: &out})
	if result.status != Fatal || !result.stopInspection || strings.Contains(out.String(), "Git name:") {
		t.Fatalf("continued after poison: %+v %s", result, &out)
	}
	if !run.OwnershipFailed(a.beginMutation(context.Background())) {
		t.Fatal("poisoned in-process mutation admitted")
	}
	if _, err := os.Stat(filepath.Join(a.Home, ".ssh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("SSH state changed")
	}
}
func TestUpdateOwnershipPrecedesDownloadAndSudo(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(fmt.Sprint(available), func(t *testing.T) {
			owner := &gateOwner{}
			if !available {
				owner.activationErr = &run.OwnershipError{Err: errors.New("no delegation")}
			}
			runner := &updateRunner{}
			requested := false
			client := release.Client{Runner: runner, HTTP: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				requested = true
				if !owner.active {
					t.Error("download before activation")
				}
				return nil, errors.New("fixture ends before verified download")
			})}, Trust: release.DefaultTrust()}
			var out strings.Builder
			a := Runtime{Ownership: owner, Runner: runner, Out: &out, Err: &out}
			code := a.installUpdate(context.Background(), client, "9.0.0", ui.UI{In: strings.NewReader("y\n"), Out: &out})
			if code != Fatal || requested != available || owner.activations != 1 || len(runner.calls) != 0 {
				t.Fatal("update ownership order", code, requested, runner.calls)
			}
		})
	}
}
func TestRuntimeSharesOwnerThroughTrustedAndTerminalCopies(t *testing.T) {
	a := DefaultRuntime()
	trusted := a.Runner.(*archrepo.TrustedRunner)
	e := trusted.Runner.(run.Exec)
	if e.Owner != a.Ownership {
		t.Fatal("runtime has separate controller")
	}
	rebound := trusted.WithRunner(e.WithIO(strings.NewReader(""), io.Discard, io.Discard))
	guarded := cancellationRunner{rebound}
	if !run.OwnershipFailed(guarded.CheckMutation()) {
		t.Fatal("inactive owner lost in wrappers")
	}
	if rebound.Runner.(run.Exec).Owner != e.Owner {
		t.Fatal("terminal copy lost controller")
	}
}

// Verification is simulated at the Runner boundary; no signing or privileged
// replacement occurs. Real signature verification has its separate release suite.
func TestApprovedUpdateOwnershipCoversVerificationAndReplacement(t *testing.T) {
	owner := &gateOwner{}
	runner := &ownedUpdateRunner{owner: owner}
	binary := []byte("fixture binary; never executed")
	manifest := fmt.Sprintf("%x  %s\n", sha256.Sum256(binary), release.BinaryName)
	client := release.Client{Runner: runner, Trust: release.DefaultTrust(), HTTP: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if !owner.active {
			t.Error("download before approval ownership")
		}
		body := "signature fixture"
		switch filepath.Base(req.URL.Path) {
		case release.ChecksumsName:
			body = manifest
		case release.BinaryName:
			body = string(binary)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	var out strings.Builder
	a := Runtime{Ownership: owner, Runner: runner, Out: &out, Err: &out}
	if code := a.installUpdate(context.Background(), client, "9.0.0", ui.UI{In: strings.NewReader("y\n"), Out: &out}); code != Success {
		t.Fatalf("code=%d: %s", code, &out)
	}
	if owner.activations != 1 || !runner.verified || !runner.sudo || !runner.replaced || runner.early {
		t.Fatalf("ordering: %+v", runner)
	}
}

type ownedUpdateRunner struct {
	owner                           *gateOwner
	verified, sudo, replaced, early bool
}

func (r *ownedUpdateRunner) Run(_ context.Context, s run.Spec) (run.Result, error) {
	if !r.owner.active {
		r.early = true
	}
	args := strings.Join(s.Args, " ")
	if s.Name == "gpg" {
		if strings.Contains(args, "--show-keys") {
			return run.Result{Stdout: "sub:-::::::::::s:\nfpr:::::::::" + release.DefaultTrust().Fingerprint + ":\n"}, nil
		}
		if strings.Contains(args, "--verify") {
			return run.Result{Stdout: "[GNUPG:] VALIDSIG " + release.DefaultTrust().Fingerprint + "\n"}, nil
		}
		return run.Result{}, nil
	}
	if s.Name == "sudo" {
		if args == "-v" {
			if !r.verified {
				r.early = true
			}
			r.sudo = true
		}
		if strings.Contains(args, "-n mv --") {
			if !r.sudo {
				r.early = true
			}
			r.replaced = true
		}
		return run.Result{}, nil
	}
	if args == "--version" {
		r.verified = true
		return run.Result{Stdout: "ops 9.0.0\n"}, nil
	}
	return run.Result{}, fmt.Errorf("unexpected command %s", s.Name)
}

func TestApplicationOwnershipFailureNeverContinuesToLaterMutation(t *testing.T) {
	for _, source := range []config.Source{config.Pacman, config.AUR, config.Flatpak} {
		t.Run(string(source), func(t *testing.T) {
			owner := &gateOwner{}
			runner := &applicationPoisonRunner{prepareRunner: &prepareRunner{}, owner: owner}
			first := plan.Application{Declaration: config.Application{Source: source, Identifier: "first"}, State: "install", Package: plan.Package{Name: "first", Repository: "extra"}}
			if source == config.AUR {
				first.AURSource = plan.AURSource{Commit: strings.Repeat("a", 40), Metadata: aurmeta.Metadata{PackageBase: "first", Packages: []aurmeta.Package{{Name: "first"}}}}
				first.AUROutputs = []string{"first"}
				first.AURExplicitOutputs = []string{"first"}
			}
			var out strings.Builder
			a := Runtime{Ownership: owner, Runner: runner, Out: &out, Err: &out, Home: t.TempDir()}
			result := a.executePlan(context.Background(), plan.Plan{Applications: []plan.Application{first, {Declaration: config.Application{Source: config.Flatpak, Identifier: "org.example.Later"}, State: "install"}}, ConfigureGit: true, CreateSSHIdentity: true}, ui.UI{In: strings.NewReader("y\n"), Out: &out})
			if result.status != Fatal || !result.stopInspection || owner.poison == nil || runner.afterPoison != 0 {
				t.Fatalf("continued after ownership failure: %+v later=%d output=%s", result, runner.afterPoison, &out)
			}
		})
	}
}

type applicationPoisonRunner struct {
	*prepareRunner
	owner       *gateOwner
	afterPoison int
}

func (r *applicationPoisonRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if r.owner.poison != nil {
		r.afterPoison++
		return run.Result{}, r.owner.poison
	}
	if s.Name == "sudo" && strings.Join(s.Args, " ") == "-v" {
		return r.prepareRunner.Run(ctx, s)
	}
	r.owner.poison = &run.OwnershipError{Err: errors.New("injected command cleanup failure")}
	return run.Result{}, r.owner.poison
}
