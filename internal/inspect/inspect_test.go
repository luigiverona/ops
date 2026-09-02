package inspect

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	sshops "github.com/luigiverona/ops/internal/ssh"
)

type stateRunner struct {
	managedKey        string
	otherKey          string
	authenticated     bool
	insufficientScope bool
	apiCalls          int
	home              string
}

func (f *stateRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	switch spec.Name {
	case "pacman":
		if len(spec.Args) > 0 && spec.Args[0] == "-Qq" {
			return run.Result{Stdout: "git\nopenssh\ngithub-cli\nflatpak\nbase-devel\n"}, nil
		}
		if len(spec.Args) > 0 && spec.Args[0] == "-Qeq" {
			return run.Result{Stdout: "git\nopenssh\ngithub-cli\nflatpak\nbase-devel\n"}, nil
		}
		return run.Result{}, nil
	case "paru":
		return run.Result{}, nil
	case "flatpak":
		if len(spec.Args) > 0 && spec.Args[0] == "remotes" {
			return run.Result{Stdout: "flathub\n"}, nil
		}
		return run.Result{}, nil
	case "git":
		if spec.Args[len(spec.Args)-1] == "user.name" {
			return run.Result{Stdout: "User\n"}, nil
		}
		return run.Result{Stdout: "user@example.com\n"}, nil
	case "ssh-keygen":
		path := spec.Args[len(spec.Args)-1]
		key := f.managedKey
		if filepath.Base(path) == "other" {
			key = f.otherKey
		}
		fingerprint, _ := sshops.PublicFingerprint(key)
		return run.Result{Stdout: "256 " + fingerprint + " key (ED25519)\n"}, nil
	case "ssh-add":
		return run.Result{Stdout: f.managedKey + "\n" + f.otherKey + "\n"}, nil
	case "ssh":
		if len(spec.Args) > 0 && spec.Args[0] == "-G" {
			return run.Result{Stdout: "host github.com\nuser git\nhostname github.com\nidentitiesonly yes\nstricthostkeychecking true\nidentityfile " + filepath.Join(f.home, ".ssh", "ops") + "\nuserknownhostsfile " + filepath.Join(f.home, ".ssh", "ops_known_hosts") + "\n"}, nil
		}
	case "gh":
		if len(spec.Args) > 1 && spec.Args[0] == "auth" && spec.Args[1] == "status" {
			if f.authenticated {
				return run.Result{}, nil
			}
			return run.Result{}, errors.New("not authenticated")
		}
		if len(spec.Args) > 0 && spec.Args[0] == "api" {
			f.apiCalls++
			if f.insufficientScope {
				return run.Result{}, &run.Error{Name: "gh", Args: spec.Args, Stderr: `gh: This API operation needs the "admin:public_key" scope`, Err: errors.New("exit status 1")}
			}
			return run.Result{Stdout: `[{"id":1,"title":"managed","key":"` + f.managedKey + `"},{"id":2,"title":"other","key":"` + f.otherKey + `"}]`}, nil
		}
	}
	return run.Result{}, errors.New("unexpected command")
}

func TestStatePlansScopeRefreshInsteadOfFailingInspection(t *testing.T) {
	runner := &stateRunner{managedKey: statePublicKey(1), otherKey: statePublicKey(2), authenticated: true, insufficientScope: true}
	state, err := (Workstation{Home: t.TempDir(), Runner: runner, PacmanConf: filepath.Join(t.TempDir(), "missing")}).State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.GitHubAuth || !state.GitHubSSHKeyScopeInsufficient || state.GitHubKeysKnown || runner.apiCalls != 1 {
		t.Fatalf("scope state=%#v api=%d", state, runner.apiCalls)
	}
}

func TestStateInspectsManagedLocalAgentAndGitHubKeysReadOnly(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	managedKey, otherKey := statePublicKey(1), statePublicKey(2)
	for name, content := range map[string]string{
		"ops": "-----BEGIN OPENSSH PRIVATE KEY-----\nmanaged\n", "ops.pub": managedKey + "\n",
		"other": "-----BEGIN OPENSSH PRIVATE KEY-----\nother\n", "other.pub": otherKey + "\n",
	} {
		if err := os.WriteFile(filepath.Join(sshDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runner := &stateRunner{managedKey: managedKey, otherKey: otherKey, authenticated: true}
	state, err := (Workstation{Home: home, Runner: runner, PacmanConf: filepath.Join(t.TempDir(), "missing")}).State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.ManagedSSHIdentity || state.SSHConfigurationReady || state.UnrelatedSSHIdentities != 1 {
		t.Fatalf("local SSH state = %#v", state)
	}
	if !state.SSHAgentAvailable || !state.ManagedSSHAgentIdentity || state.UnrelatedSSHAgentIdentities != 1 {
		t.Fatalf("agent state = %#v", state)
	}
	if !state.GitHubAuth || !state.GitHubKeysKnown || !state.ManagedGitHubKeyKnown || !state.ManagedGitHubKey || state.OtherGitHubKeys != 1 || runner.apiCalls != 1 {
		t.Fatalf("GitHub state = %#v, api calls=%d", state, runner.apiCalls)
	}
}

func TestStateDoesNotQueryGitHubKeysBeforeAuthentication(t *testing.T) {
	runner := &stateRunner{managedKey: statePublicKey(1), otherKey: statePublicKey(2)}
	state, err := (Workstation{Home: t.TempDir(), Runner: runner, PacmanConf: filepath.Join(t.TempDir(), "missing")}).State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.GitHubAuth || state.GitHubKeysKnown || state.ManagedGitHubKeyKnown || state.ManagedGitHubKey || runner.apiCalls != 0 {
		t.Fatalf("unauthenticated GitHub state was invented: %#v, api calls=%d", state, runner.apiCalls)
	}
}

func TestStateKeepsManagedGitHubKeyComparisonUnknownWithoutLocalIdentity(t *testing.T) {
	runner := &stateRunner{managedKey: statePublicKey(1), otherKey: statePublicKey(2), authenticated: true}
	state, err := (Workstation{Home: t.TempDir(), Runner: runner, PacmanConf: filepath.Join(t.TempDir(), "missing")}).State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.GitHubAuth || !state.GitHubKeysKnown || state.ManagedGitHubKeyKnown || state.ManagedGitHubKey || runner.apiCalls != 1 {
		t.Fatalf("future managed fingerprint was invented: %#v, api calls=%d", state, runner.apiCalls)
	}
	if state.SSHHostKeyFreshness != plan.SSHHostKeyFreshnessUnknown {
		t.Fatalf("missing managed SSH configuration freshness=%q", state.SSHHostKeyFreshness)
	}
}

func TestStateKeepsRecognizedSSHConfigurationReadyWhenFreshnessUnavailable(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	managedKey, otherKey := statePublicKey(1), statePublicKey(2)
	if err := os.WriteFile(filepath.Join(sshDir, "ops"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nmanaged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "ops.pub"), []byte(managedKey+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &stateRunner{home: home, managedKey: managedKey, otherKey: otherKey, authenticated: true}
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ssh_keys":["`+stateHostKey(3)+`"]}`)
	}))
	manager := sshops.Manager{Home: home, Runner: runner, HTTP: metadata.Client(), MetadataURL: metadata.URL}
	if err := manager.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	metadata.Close()
	before, err := os.ReadFile(filepath.Join(sshDir, "ops_known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := (Workstation{
		Home: home, Runner: runner, PacmanConf: filepath.Join(t.TempDir(), "missing"),
		SSHHTTP: metadata.Client(), SSHMetadataURL: metadata.URL,
	}).State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.SSHConfigurationReady || state.SSHHostKeyFreshness != plan.SSHHostKeyFreshnessUnavailable {
		t.Fatalf("outage state=%#v", state)
	}
	after, err := os.ReadFile(filepath.Join(sshDir, "ops_known_hosts"))
	if err != nil || string(after) != string(before) {
		t.Fatal("read-only unavailable inspection rewrote managed host keys")
	}
}

func statePublicKey(fill byte) string {
	typeName := []byte("ssh-ed25519")
	blob := make([]byte, 4+len(typeName)+4+32)
	blob[3] = byte(len(typeName))
	copy(blob[4:], typeName)
	offset := 4 + len(typeName)
	blob[offset+3] = 32
	for i := offset + 4; i < len(blob); i++ {
		blob[i] = fill
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " " + strings.Repeat("x", int(fill))
}

func stateHostKey(fill byte) string {
	fields := strings.Fields(statePublicKey(fill))
	return fields[0] + " " + fields[1]
}
