package app

import (
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

func TestSSHDeletionRequiresBothConfirmations(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen unavailable")
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	_ = os.Mkdir(dir, 0o700)
	generateTestIdentity(t, dir, "ops")
	private := filepath.Join(dir, "existing")
	generateTestIdentity(t, dir, "existing")
	runtime := Runtime{Home: home, Runner: run.Exec{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard}, Out: io.Discard, Err: io.Discard}
	terminal := ui.UI{In: strings.NewReader("n\ny\n"), Out: io.Discard}
	status, _, issues, fatal := runtime.configureSSH(context.Background(), terminal, plan.Plan{ReviewSSHIdentities: true})
	if fatal != nil || len(issues) != 0 || status != "ready" {
		t.Fatalf("status=%s issues=%v fatal=%v", status, issues, fatal)
	}
	if _, err := os.Stat(private); !os.IsNotExist(err) {
		t.Fatal("private identity was not deleted after both confirmations")
	}
	if _, err := os.Stat(private + ".pub"); !os.IsNotExist(err) {
		t.Fatal("public identity was not deleted after both confirmations")
	}
}

func TestSSHDeletionDefaultsToKeep(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen unavailable")
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	_ = os.Mkdir(dir, 0o700)
	generateTestIdentity(t, dir, "ops")
	private := filepath.Join(dir, "existing")
	generateTestIdentity(t, dir, "existing")
	runtime := Runtime{Home: home, Runner: run.Exec{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard}, Out: io.Discard, Err: io.Discard}
	terminal := ui.UI{In: strings.NewReader("\n"), Out: io.Discard}
	_, _, _, _ = runtime.configureSSH(context.Background(), terminal, plan.Plan{ReviewSSHIdentities: true})
	if _, err := os.Stat(private); err != nil {
		t.Fatal("default keep removed identity")
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
	deleted    []string
	failDelete bool
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
		if f.failDelete {
			return run.Result{}, errors.New("denied")
		}
		return run.Result{}, nil
	}
	return run.Result{}, errors.New("unexpected command")
}

func TestGitHubKeysReviewedIndividuallyAndDoubleConfirmed(t *testing.T) {
	fake := &githubFake{}
	runtime := Runtime{Runner: fake, Out: io.Discard, Err: io.Discard}
	terminal := ui.UI{In: strings.NewReader("n\ny\n"), Out: io.Discard}
	fingerprint, _ := sshops.PublicFingerprint(wirePublic(1))
	status, issues := runtime.configureGitHub(context.Background(), terminal, &sshops.Identity{PublicPath: "unused", Fingerprint: fingerprint}, plan.Plan{ReviewGitHubKeys: true})
	if status != "ready" || len(issues) != 0 || len(fake.deleted) != 1 || fake.deleted[0] != "2" {
		t.Fatalf("status=%s issues=%v deleted=%v", status, issues, fake.deleted)
	}
}

func TestGitHubDeletionFailureIsReported(t *testing.T) {
	fake := &githubFake{failDelete: true}
	runtime := Runtime{Runner: fake, Out: io.Discard, Err: io.Discard}
	terminal := ui.UI{In: strings.NewReader("n\ny\n"), Out: io.Discard}
	status, issues := runtime.configureGitHub(context.Background(), terminal, &sshops.Identity{}, plan.Plan{ReviewGitHubKeys: true})
	if status != "failed" || len(issues) != 1 {
		t.Fatalf("status=%s issues=%v", status, issues)
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
