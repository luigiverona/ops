package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	sshops "github.com/luigiverona/ops/internal/ssh"
	"github.com/luigiverona/ops/internal/ui"
)

func TestSetupPreservesUnrelatedSSHIdentitiesWithoutPrompts(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen unavailable")
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	generateTestIdentity(t, dir, "ops")
	generateTestIdentity(t, dir, "existing")
	before := make(map[string][]byte)
	for _, name := range []string{"existing", "existing.pub"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = data
	}
	var output bytes.Buffer
	input := strings.NewReader("n\ny\n")
	runtime := Runtime{Home: home, Runner: run.Exec{Out: io.Discard, Err: io.Discard}, Out: &output, Err: &output}
	status, _, issues, fatal := runtime.configureSSH(context.Background(), ui.UI{In: input, Out: &output}, plan.Plan{ReviewSSHIdentities: true})
	if fatal != nil || len(issues) != 0 || status != "ready" || input.Len() != len("n\ny\n") || output.Len() != 0 {
		t.Fatalf("status=%s issues=%v fatal=%v output=%s", status, issues, fatal, &output)
	}
	for name, want := range before {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("unrelated key changed: %s, %v", name, err)
		}
	}
}
func generateTestIdentity(t *testing.T, dir, name string) {
	t.Helper()
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, name))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, output)
	}
}

type githubFake struct {
	deleted []string
}

func (f *githubFake) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	if spec.Name == "ssh" {
		return run.Result{Stderr: "successfully authenticated"}, errors.New("exit 1")
	}
	if len(spec.Args) >= 2 && spec.Args[0] == "auth" && spec.Args[1] == "status" {
		return run.Result{}, nil
	}
	if len(spec.Args) > 0 && spec.Args[0] == "api" {
		return run.Result{Stdout: `[{"id":1,"title":"first","key":"` + wirePublic(1) + `"},{"id":2,"title":"second","key":"` + wirePublic(2) + `"}]`}, nil
	}
	if len(spec.Args) >= 3 && spec.Args[0] == "ssh-key" && spec.Args[1] == "delete" {
		f.deleted = append(f.deleted, spec.Args[2])
		return run.Result{}, nil
	}
	return run.Result{}, errors.New("unexpected command")
}

func TestSetupPreservesGitHubKeysWithoutPrompts(t *testing.T) {
	fake := &githubFake{}
	var output bytes.Buffer
	input := strings.NewReader("n\ny\n")
	fingerprint, _ := sshops.PublicFingerprint(wirePublic(1))
	status, issues := (Runtime{Runner: fake, Out: &output, Err: &output}).configureGitHub(context.Background(), ui.UI{In: input, Out: &output}, &sshops.Identity{Fingerprint: fingerprint}, plan.Plan{ReviewGitHubKeys: true})
	if status != "ready" || len(issues) != 0 || len(fake.deleted) != 0 || input.Len() != len("n\ny\n") {
		t.Fatalf("status=%s issues=%v deleted=%v", status, issues, fake.deleted)
	}
	if output.Len() != 0 {
		t.Fatalf("output=%q", output.String())
	}
}

type agentPreservationRunner struct {
	prepareRunner
}

func (r *agentPreservationRunner) Run(ctx context.Context, spec run.Spec) (run.Result, error) {
	if spec.Name == "ssh-add" && strings.Join(spec.Args, " ") == "-L" {
		r.calls = append(r.calls, spec)
		return run.Result{Stdout: wirePublic(19) + "\n"}, nil
	}
	return r.prepareRunner.Run(ctx, spec)
}

func TestSetupPreservesUnrelatedAgentKeysWithoutPrompts(t *testing.T) {
	home, fingerprint, _ := unauthenticatedGitHubFixture(t)
	runner := &agentPreservationRunner{prepareRunner{sshFingerprint: fingerprint}}
	var output bytes.Buffer
	input := strings.NewReader("n\n")
	status, _, issues, fatal := (Runtime{Home: home, Runner: runner, Out: &output, Err: &output}).configureSSH(context.Background(), ui.UI{In: input, Out: &output}, plan.Plan{ReviewSSHAgent: true, LoadSSHAgent: true})
	if status != "ready" || len(issues) != 0 || fatal != nil || input.Len() != len("n\n") {
		t.Fatalf("status=%s issues=%v fatal=%v", status, issues, fatal)
	}
	loaded := false
	for _, call := range runner.calls {
		if call.Name != "ssh-add" {
			continue
		}
		args := strings.Join(call.Args, " ")
		if args == filepath.Join(home, ".ssh", "ops") && call.Interactive {
			loaded = true
			continue
		}
		if args != "-L" {
			t.Fatalf("unexpected agent mutation: %#v", call)
		}
	}
	if !loaded || strings.Contains(output.String(), "?") {
		t.Fatalf("managed load missing or extra prompt: %s", &output)
	}
}

type githubRegistrationRaceRunner struct {
	prepareRunner
}

func (r *githubRegistrationRaceRunner) Run(ctx context.Context, spec run.Spec) (run.Result, error) {
	result, err := r.prepareRunner.Run(ctx, spec)
	if r.githubAPICalls == 1 {
		r.remoteKeys = `[{"id":1,"title":"managed","key":` + strconv.Quote(wirePublic(9)) + ` }]`
	}
	return result, err
}

func TestGitHubSummaryDoesNotClaimAnExistingKeyWasAdded(t *testing.T) {
	home, fingerprint, _ := unauthenticatedGitHubFixture(t)
	runner := &githubRegistrationRaceRunner{prepareRunner{sshFingerprint: fingerprint}}
	var output bytes.Buffer
	status, issues := (Runtime{Home: home, Runner: runner, Out: &output, Err: &output}).configureGitHub(
		context.Background(), ui.UI{}, &sshops.Identity{Fingerprint: fingerprint, PublicPath: filepath.Join(home, ".ssh", "ops.pub")}, plan.Plan{ConfigureGitHubKey: true},
	)
	if status != "ready" || len(issues) != 0 || output.Len() != 0 {
		t.Fatalf("status=%s issues=%v output=%s", status, issues, &output)
	}
	for _, call := range runner.calls {
		if call.Name == "gh" && strings.HasPrefix(strings.Join(call.Args, " "), "ssh-key add") {
			t.Fatalf("duplicate registration: %#v", call)
		}
	}
}

func wirePublic(fill byte) string {
	typeName := []byte("ssh-ed25519")
	blob := make([]byte, 4+len(typeName)+4+32)
	blob[3] = byte(len(typeName))
	copy(blob[4:], typeName)
	offset := 4 + len(typeName)
	blob[offset+3] = 32
	for i := offset + 4; i < len(blob); i++ {
		blob[i] = fill
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " key-" + strconv.Itoa(int(fill))
}
