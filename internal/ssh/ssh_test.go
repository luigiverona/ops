package ssh

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luigiverona/ops/internal/run"
)

func managerFor(t *testing.T) Manager {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen unavailable")
	}
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	return Manager{Home: home, Runner: run.Exec{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard}}
}

func generate(t *testing.T, m Manager, name string) {
	t.Helper()
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(m.dir(), name), "-C", "test")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, output)
	}
}

type recordingRunner struct{ calls []run.Spec }

func TestDiscoverRejectsUnsafeManagedPathsBeforeCommands(t *testing.T) {
	for _, target := range []string{".ssh", ".ssh/ops", ".ssh/ops.pub"} {
		t.Run(target, func(t *testing.T) {
			home, outside := t.TempDir(), t.TempDir()
			if target != ".ssh" {
				if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, filepath.Join(home, target)); err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{}
			if _, err := (Manager{Home: home, Runner: runner}).Discover(context.Background()); err == nil || len(runner.calls) != 0 {
				t.Fatalf("unsafe path reached commands: %v %v", err, runner.calls)
			}
		})
	}
}

func (r *recordingRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	r.calls = append(r.calls, spec)
	return run.Result{}, errors.New("stop after command inspection")
}

func TestEnsureIdentityUsesQuietInteractiveKeygen(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	_, _ = (Manager{Home: home, Runner: runner}).EnsureIdentity(context.Background())
	if len(runner.calls) != 1 {
		t.Fatalf("calls=%#v", runner.calls)
	}
	call := runner.calls[0]
	if call.Name != "ssh-keygen" || !call.Interactive || call.Interaction == "" || strings.Join(call.Args, " ") != "-q -t ed25519 -f "+filepath.Join(home, ".ssh", "ops")+" -C ops-managed" {
		t.Fatalf("keygen invocation=%#v", call)
	}
}

func TestSSHKeygenQuietModeSuppressesStatusOutput(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen unavailable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ops")
	output, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", path, "-C", "test").CombinedOutput()
	if err != nil || len(output) != 0 {
		t.Fatalf("quiet ssh-keygen err=%v output=%q", err, output)
	}
}

func TestDiscoverNoKeysAndUnrelatedFiles(t *testing.T) {
	m := managerFor(t)
	for name, data := range map[string]string{
		"config": "Host example\n", "known_hosts": "example ssh-ed25519 AAAA\n",
		"authorized_keys": "not a key", "certificate.pub": "ssh-ed25519-cert-v01@openssh.com AAAA",
		"malformed": "-----BEGIN OPENSSH PRIVATE KEY-----\nbad",
	} {
		_ = os.WriteFile(filepath.Join(m.dir(), name), []byte(data), 0o600)
	}
	_ = os.Mkdir(filepath.Join(m.dir(), "directory"), 0o700)
	_ = os.Symlink("/etc/passwd", filepath.Join(m.dir(), "linked-key"))
	ids, err := m.Discover(context.Background())
	if err != nil || len(ids) != 0 {
		t.Fatalf("identities = %#v, %v", ids, err)
	}
}

func TestAuthorizedKeysIsNeverAnIdentityFile(t *testing.T) {
	m := managerFor(t)
	generate(t, m, "source")
	public, _ := os.ReadFile(filepath.Join(m.dir(), "source.pub"))
	_ = os.Remove(filepath.Join(m.dir(), "source.pub"))
	_ = os.WriteFile(filepath.Join(m.dir(), "authorized_keys"), public, 0o600)
	ids, err := m.Discover(context.Background())
	if err != nil || len(ids) != 1 || ids[0].PublicPath != "" {
		t.Fatalf("authorized_keys was classified as identity pair: %#v, %v", ids, err)
	}
}

func TestDiscoverPairsMultipleAndOrphanPublic(t *testing.T) {
	m := managerFor(t)
	generate(t, m, "first")
	generate(t, m, "second")
	data, _ := os.ReadFile(filepath.Join(m.dir(), "second.pub"))
	_ = os.WriteFile(filepath.Join(m.dir(), "orphan.pub"), data, 0o644)
	ids, err := m.Discover(context.Background())
	if err != nil || len(ids) != 2 {
		t.Fatalf("identities = %#v, %v", ids, err)
	}
	for _, id := range ids {
		if id.PublicPath == "" || id.Fingerprint == "" {
			t.Fatalf("unpaired identity: %#v", id)
		}
	}
}

func TestDeleteExactIdentityOnly(t *testing.T) {
	m := managerFor(t)
	generate(t, m, "delete-me")
	untouched := filepath.Join(m.dir(), "known_hosts")
	_ = os.WriteFile(untouched, []byte("keep"), 0o600)
	ids, _ := m.Discover(context.Background())
	if err := m.Delete(context.Background(), ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(untouched); err != nil {
		t.Fatal("unrelated file was removed")
	}
	if _, err := os.Stat(ids[0].PrivatePath); !os.IsNotExist(err) {
		t.Fatal("private key remains")
	}
}

func TestDeleteRejectsSymlinkSwap(t *testing.T) {
	m := managerFor(t)
	generate(t, m, "key")
	ids, _ := m.Discover(context.Background())
	if err := os.Remove(ids[0].PrivatePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", ids[0].PrivatePath); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(context.Background(), ids[0]); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

type configRunner struct {
	home          string
	extraIdentity string
}

type metadataTransport func(*http.Request) (*http.Response, error)

func (f metadataTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (f configRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	if spec.Name == "ssh" && len(spec.Args) > 0 && spec.Args[0] == "-G" {
		output := "host github.com\nuser git\nhostname github.com\nidentitiesonly yes\nstricthostkeychecking true\nidentityfile " + filepath.Join(f.home, ".ssh", "ops") + "\n"
		if f.extraIdentity != "" {
			output += "identityfile " + f.extraIdentity + "\n"
		}
		return run.Result{Stdout: output + "userknownhostsfile " + filepath.Join(f.home, ".ssh", "ops_known_hosts") + "\n"}, nil
	}
	return run.Result{}, fmt.Errorf("unexpected command: %s", spec.Name)
}

func TestConfigureGitHubPreservesConfigAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	_ = os.Mkdir(dir, 0o700)
	original := "Host example.com\n    IdentityFile ~/.ssh/example\n"
	_ = os.WriteFile(filepath.Join(dir, "config"), []byte(original), 0o600)
	unrelatedKnownHosts := filepath.Join(dir, "known_hosts")
	_ = os.WriteFile(unrelatedKnownHosts, []byte("example.com ssh-ed25519 unrelated\n"), 0o600)
	m := managerWithMetadata(t, home)
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "config"))
	if !strings.Contains(string(data), "Host * !github.com") || strings.Contains(string(data), original) {
		t.Fatalf("managed dispatcher is invalid: %s", data)
	}
	preserved, _ := os.ReadFile(filepath.Join(dir, "ops_user_config"))
	if string(preserved) != original {
		t.Fatalf("user config not preserved: %q", preserved)
	}
	managed, _ := os.ReadFile(filepath.Join(dir, "ops_config"))
	if !strings.HasPrefix(string(managed), managedMarker+"\n") || !strings.Contains(string(managed), "IdentitiesOnly yes") || !strings.Contains(string(managed), "StrictHostKeyChecking yes") {
		t.Fatal("missing deterministic identity selection")
	}
	knownHosts, _ := os.ReadFile(filepath.Join(dir, "ops_known_hosts"))
	if !strings.Contains(string(knownHosts), "github.com ssh-ed25519 ") || !validManagedKnownHosts(filepath.Join(dir, "ops_known_hosts")) {
		t.Fatalf("managed GitHub host keys are invalid: %s", knownHosts)
	}
	if data, _ := os.ReadFile(unrelatedKnownHosts); string(data) != "example.com ssh-ed25519 unrelated\n" {
		t.Fatal("ordinary known_hosts was modified")
	}
	if !m.GitHubConfigured(context.Background()) {
		t.Fatal("fresh GitHub SSH configuration was not recognized")
	}
	status, err := m.InspectGitHubConfiguration(context.Background())
	if err != nil || !status.LocalReady || status.Freshness != HostKeyFreshnessCurrent {
		t.Fatalf("fresh GitHub SSH configuration status=%#v, %v", status, err)
	}
}

func TestInspectGitHubConfigurationReportsStaleOfficialHostKeys(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := managerWithMetadata(t, home)
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	stale := renderKnownHosts([]string{testHostKey("ssh-ed25519", 9)})
	if err := os.WriteFile(filepath.Join(dir, "ops_known_hosts"), stale, 0o600); err != nil {
		t.Fatal(err)
	}
	if !m.GitHubConfigured(context.Background()) {
		t.Fatal("structurally valid stale host keys were not suitable for the freshness test")
	}
	status, err := m.InspectGitHubConfiguration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.LocalReady || status.Freshness != HostKeyFreshnessStale {
		t.Fatalf("stale GitHub host keys status=%#v", status)
	}
}

func TestInspectGitHubConfigurationSeparatesUnavailableMetadata(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := managerWithMetadata(t, home)
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Run("transport", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		client, endpoint := server.Client(), server.URL
		server.Close()
		check := m
		check.HTTP, check.MetadataURL = client, endpoint
		status, err := check.InspectGitHubConfiguration(context.Background())
		if err != nil || !status.LocalReady || status.Freshness != HostKeyFreshnessUnavailable {
			t.Fatalf("transport status=%#v err=%v", status, err)
		}
	})

	t.Run("service", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
		}))
		defer server.Close()
		check := m
		check.HTTP, check.MetadataURL = server.Client(), server.URL
		status, err := check.InspectGitHubConfiguration(context.Background())
		if err != nil || !status.LocalReady || status.Freshness != HostKeyFreshnessUnavailable {
			t.Fatalf("service status=%#v err=%v", status, err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		check := m
		check.HTTP = &http.Client{Transport: metadataTransport(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		})}
		check.MetadataURL = "https://metadata.invalid/test"
		status, err := check.InspectGitHubConfiguration(context.Background())
		if err != nil || !status.LocalReady || status.Freshness != HostKeyFreshnessUnavailable {
			t.Fatalf("timeout status=%#v err=%v", status, err)
		}
	})
}

func TestInspectGitHubConfigurationRejectsMalformedAuthoritativeMetadata(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := managerWithMetadata(t, home)
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ssh_keys":["ssh-ed25519 not-base64"]}`)
	}))
	defer server.Close()
	m.HTTP, m.MetadataURL = server.Client(), server.URL
	if _, err := m.InspectGitHubConfiguration(context.Background()); err == nil {
		t.Fatal("malformed authoritative metadata was treated as unavailable")
	}
}

func TestConfigureGitHubDoesNotRefreshWithoutCurrentMetadata(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := managerWithMetadata(t, home)
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	stale := renderKnownHosts([]string{testHostKey("ssh-ed25519", 9)})
	knownHostsPath := filepath.Join(dir, "ops_known_hosts")
	if err := os.WriteFile(knownHostsPath, stale, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	m.HTTP, m.MetadataURL = server.Client(), server.URL
	if err := m.ConfigureGitHub(context.Background()); err == nil {
		t.Fatal("host-key refresh succeeded without current metadata")
	}
	after, err := os.ReadFile(knownHostsPath)
	if err != nil || string(after) != string(stale) {
		t.Fatal("unavailable refresh altered existing managed host keys")
	}
}

func TestInspectGitHubConfigurationDoesNotFetchForUnrecognizedLocalState(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ops_config"), []byte("unowned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer server.Close()
	m := Manager{Home: home, Runner: configRunner{home: home}, HTTP: server.Client(), MetadataURL: server.URL}
	status, err := m.InspectGitHubConfiguration(context.Background())
	if err != nil || status.LocalReady || status.Freshness != HostKeyFreshnessUnknown || requests != 0 {
		t.Fatalf("unsafe local status=%#v err=%v requests=%d", status, err, requests)
	}
}

func TestEffectiveGitHubConfigParsesRealOpenSSHOutput(t *testing.T) {
	home := "/home/ah"
	managed := "host github.com\nuser git\nhostname github.com\nidentitiesonly yes\nstricthostkeychecking true\nidentityfile ~/.ssh/ops\nuserknownhostsfile /home/ah/.ssh/ops_known_hosts\n"
	if !effectiveGitHubConfig(managed, home) {
		t.Fatal("real OpenSSH true/yes output was rejected")
	}

	observed := "host github.com\nuser git\nhostname github.com\nidentitiesonly yes\nstricthostkeychecking true\nidentityfile ~/.ssh/ops\nidentityfile /home/ah/.ssh/id_ed25519_ai_github\nuserknownhostsfile /home/ah/.ssh/ops_known_hosts\n"
	if effectiveGitHubConfig(observed, home) {
		t.Fatal("extra effective IdentityFile was accepted")
	}

	for _, directive := range []string{"identitiesonly", "stricthostkeychecking"} {
		for _, value := range []string{"false", "no", "off"} {
			t.Run(directive+" "+value, func(t *testing.T) {
				unsafe := strings.Replace(managed, directive+" "+map[string]string{"identitiesonly": "yes", "stricthostkeychecking": "true"}[directive], directive+" "+value, 1)
				if effectiveGitHubConfig(unsafe, home) {
					t.Fatalf("unsafe boolean %s %s was accepted", directive, value)
				}
			})
		}
	}

	for name, unsafe := range map[string]string{
		"wrong host":              strings.Replace(managed, "host github.com", "host example.com", 1),
		"wrong hostname":          strings.Replace(managed, "hostname github.com", "hostname example.com", 1),
		"wrong user":              strings.Replace(managed, "user git", "user root", 1),
		"wrong known-hosts file":  strings.Replace(managed, "/home/ah/.ssh/ops_known_hosts", "/home/ah/.ssh/known_hosts", 1),
		"multiple known-hosts":    strings.Replace(managed, "userknownhostsfile /home/ah/.ssh/ops_known_hosts", "userknownhostsfile /home/ah/.ssh/ops_known_hosts /home/ah/.ssh/known_hosts", 1),
		"duplicate identity line": managed + "identityfile ~/.ssh/ops\n",
	} {
		t.Run(name, func(t *testing.T) {
			if effectiveGitHubConfig(unsafe, home) {
				t.Fatal("unsafe effective configuration was accepted")
			}
		})
	}
}

func TestConfigureGitHubAndConfiguredRejectExtraIdentity(t *testing.T) {
	home := t.TempDir()
	m := managerWithMetadataRunner(t, home, configRunner{
		home:          home,
		extraIdentity: filepath.Join(home, ".ssh", "unrelated"),
	})
	if err := m.ConfigureGitHub(context.Background()); err == nil {
		t.Fatal("ConfigureGitHub accepted an extra effective identity")
	}
	if m.GitHubConfigured(context.Background()) {
		t.Fatal("GitHubConfigured accepted an extra effective identity")
	}
}

func TestConfigureGitHubRealOpenSSHIsolation(t *testing.T) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh unavailable")
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	unrelatedGitHubIdentity := filepath.Join(dir, "id_ed25519_ai_github")
	unrelatedExampleIdentity := filepath.Join(dir, "id_ed25519_example")
	userConfig := "Host github.com\n    IdentityFile " + unrelatedGitHubIdentity + "\nHost example.com\n    IdentityFile " + unrelatedExampleIdentity + "\n"
	legacyManaged := managedMarker + "\nHost github.com\n    HostName github.com\n    User git\n    IdentityFile ~/.ssh/ops\n    IdentitiesOnly yes\n    UserKnownHostsFile ~/.ssh/ops_known_hosts\n    StrictHostKeyChecking yes\n"
	legacyMain := managedIncludeStart + "\nInclude ~/.ssh/ops_config\n" + managedIncludeEnd + "\n" + userConfig
	if err := os.WriteFile(filepath.Join(dir, "ops_config"), []byte(legacyManaged), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(legacyMain), 0o600); err != nil {
		t.Fatal(err)
	}

	m := managerWithMetadataRunner(t, home, run.Exec{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard})
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !m.GitHubConfigured(context.Background()) {
		t.Fatal("GitHubConfigured disagrees with successful ConfigureGitHub")
	}
	assertRealEffectiveIdentity(t, sshPath, filepath.Join(dir, "config"), "github.com", filepath.Join(dir, "ops"))
	assertRealEffectiveIdentity(t, sshPath, filepath.Join(dir, "config"), "example.com", unrelatedExampleIdentity)
	preserved, err := os.ReadFile(filepath.Join(dir, "ops_user_config"))
	if err != nil || string(preserved) != userConfig {
		t.Fatalf("preserved config = %q, %v", preserved, err)
	}

	before := readSSHConfigurationFiles(t, dir)
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := readSSHConfigurationFiles(t, dir)
	for name, data := range before {
		if string(after[name]) != string(data) {
			t.Fatalf("repeated ConfigureGitHub changed %s", name)
		}
	}
}

func TestConfigureGitHubMigratesExactV100PartialState(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	configDir := filepath.Join(dir, "config.d")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	privateKey := []byte("existing managed private key\n")
	publicKey := []byte("existing managed public key\n")
	userInclude := "Include ~/.ssh/config.d/ai-github.conf\n"
	oldMain := managedIncludeStart + "\nInclude ~/.ssh/ops_config\n" + managedIncludeEnd + "\n" + userInclude
	oldManaged := managedMarker + "\nHost github.com\n    HostName github.com\n    User git\n    IdentityFile ~/.ssh/ops\n    IdentitiesOnly yes\n    UserKnownHostsFile ~/.ssh/ops_known_hosts\n    StrictHostKeyChecking yes\n"
	aiConfig := "Host github.com\n    IdentityFile ~/.ssh/id_ed25519_ai_github\nHost example.com\n    User deploy\n"
	files := map[string][]byte{
		"ops":                     privateKey,
		"ops.pub":                 publicKey,
		"ops_config":              []byte(oldManaged),
		"ops_known_hosts":         renderKnownHosts([]string{testHostKey("ssh-ed25519", 1), testHostKey("ssh-rsa", 2)}),
		"config":                  []byte(oldMain),
		"config.d/ai-github.conf": []byte(aiConfig),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// OpenSSH expands ~ from the OS account home rather than Manager.Home. Keep
	// this fixture byte-exact; real Include behavior is covered separately below
	// with absolute paths confined to a temporary directory.
	m := managerWithMetadata(t, home)
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	preserved, err := os.ReadFile(filepath.Join(dir, "ops_user_config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(preserved) != userInclude {
		t.Fatalf("preserved config = %q, want %q", preserved, userInclude)
	}
	if strings.Contains(string(preserved), managedIncludeStart) || strings.Contains(string(preserved), managedIncludeEnd) || strings.Contains(string(preserved), "Include ~/.ssh/ops_config") {
		t.Fatalf("ops-owned v1.0.0 include leaked into preserved config: %q", preserved)
	}
	dispatcher, err := renderGitHubDispatcher(filepath.Join(dir, "ops_config"), filepath.Join(dir, "ops_user_config"))
	if err != nil {
		t.Fatal(err)
	}
	mainConfig, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil || string(mainConfig) != string(dispatcher) {
		t.Fatalf("dispatcher = %q, %v", mainConfig, err)
	}
	if strings.Count(string(mainConfig), managedIncludeStart) != 1 || strings.Count(string(mainConfig), filepath.Join(dir, "ops_config")) != 1 {
		t.Fatalf("dispatcher does not contain exactly one managed include: %q", mainConfig)
	}
	for name, want := range map[string][]byte{
		"ops":                     privateKey,
		"ops.pub":                 publicKey,
		"config.d/ai-github.conf": []byte(aiConfig),
	} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("%s changed: %q, %v", name, got, err)
		}
	}
	if !recognizedManagedFile(filepath.Join(dir, "ops_config")) || !validManagedKnownHosts(filepath.Join(dir, "ops_known_hosts")) {
		t.Fatal("migrated managed files are not recognized")
	}

	before := readSSHConfigurationFiles(t, dir, "config", "ops", "ops.pub", "ops_config", "ops_known_hosts", "ops_user_config", "config.d/ai-github.conf")
	for runNumber := 2; runNumber <= 3; runNumber++ {
		if err := m.ConfigureGitHub(context.Background()); err != nil {
			t.Fatalf("ConfigureGitHub run %d: %v", runNumber, err)
		}
		after := readSSHConfigurationFiles(t, dir, "config", "ops", "ops.pub", "ops_config", "ops_known_hosts", "ops_user_config", "config.d/ai-github.conf")
		for name, want := range before {
			if string(after[name]) != string(want) {
				t.Fatalf("ConfigureGitHub run %d changed %s", runNumber, name)
			}
		}
	}
}

func TestConfigureGitHubRealOpenSSHPreservesComplexUserConfig(t *testing.T) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh unavailable")
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	configDir := filepath.Join(dir, "config.d")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := func(name string) string { return filepath.Join(dir, name) }
	nestedConfig := "Host github.com\n    IdentityFile " + path("nested-github-user") + "\nHost example.com\n    IdentityFile " + path("nested-example-user") + "\nHost *\n    ServerAliveInterval 17\n"
	userConfig := "IdentityFile " + path("global-user") + "\nHost *\n    IdentityFile " + path("wildcard-user") + "\nHost github.com\n    IdentityFile " + path("direct-github-user") + "\nHost example.com\n    User deploy\n    IdentityFile " + path("example-user") + "\nHost *.example.net !blocked.example.net\n    User patterned\n    IdentityFile " + path("pattern-user") + "\nHost blocked.example.net\n    User blocked\nHost *\n    Include " + filepath.Join(configDir, "nested.conf") + "\nMatch host github.com\n    IdentityFile " + path("match-github-user") + "\nMatch host example.com\n    IdentityFile " + path("match-example-user") + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "user.conf"), []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "nested.conf"), []byte(nestedConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	original := "Include " + filepath.Join(configDir, "user.conf") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	m := managerWithMetadataRunner(t, home, run.Exec{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard})
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config")
	assertRealEffectiveConfig(t, sshPath, configPath, "github.com", "git", []string{path("ops")}, "0")
	assertRealEffectiveConfig(t, sshPath, configPath, "example.com", "deploy", []string{path("global-user"), path("wildcard-user"), path("example-user"), path("nested-example-user"), path("match-example-user")}, "17")
	assertRealEffectiveConfig(t, sshPath, configPath, "foo.example.net", "patterned", []string{path("global-user"), path("wildcard-user"), path("pattern-user")}, "17")
	assertRealEffectiveConfig(t, sshPath, configPath, "blocked.example.net", "blocked", []string{path("global-user"), path("wildcard-user")}, "17")
	preserved, err := os.ReadFile(filepath.Join(dir, "ops_user_config"))
	if err != nil || string(preserved) != original {
		t.Fatalf("top-level user include changed: %q, %v", preserved, err)
	}
}

func assertRealEffectiveIdentity(t *testing.T, sshPath, configPath, host, want string) {
	t.Helper()
	cmd := exec.Command(sshPath, "-G", "-F", configPath, host)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("ssh -G %s: %v", host, err)
	}
	parsed, ok := parseEffectiveSSHConfig(string(output))
	if !ok || !parsed.singlePath("identityfile", want) {
		t.Fatalf("effective identity for %s is not %s:\n%s", host, want, output)
	}
	if host == "github.com" && !effectiveGitHubConfig(string(output), filepath.Dir(filepath.Dir(configPath))) {
		t.Fatalf("unsafe effective GitHub configuration:\n%s", output)
	}
}

func assertRealEffectiveConfig(t *testing.T, sshPath, configPath, host, wantUser string, wantIdentities []string, wantServerAliveInterval string) {
	t.Helper()
	cmd := exec.Command(sshPath, "-G", "-F", configPath, host)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("ssh -G %s: %v", host, err)
	}
	parsed, ok := parseEffectiveSSHConfig(string(output))
	if !ok || !parsed.single("user", wantUser) {
		t.Fatalf("effective user for %s is not %s:\n%s", host, wantUser, output)
	}
	var identities []string
	for _, values := range parsed["identityfile"] {
		identities = append(identities, values...)
	}
	if strings.Join(identities, "\n") != strings.Join(wantIdentities, "\n") {
		t.Fatalf("effective identities for %s = %q, want %q:\n%s", host, identities, wantIdentities, output)
	}
	if !strings.Contains(string(output), "serveraliveinterval "+wantServerAliveInterval+"\n") {
		t.Fatalf("effective ServerAliveInterval for %s is not %s:\n%s", host, wantServerAliveInterval, output)
	}
	if host == "github.com" && !effectiveGitHubConfig(string(output), filepath.Dir(filepath.Dir(configPath))) {
		t.Fatalf("unsafe effective GitHub configuration:\n%s", output)
	}
}

func readSSHConfigurationFiles(t *testing.T, dir string, names ...string) map[string][]byte {
	t.Helper()
	if len(names) == 0 {
		names = []string{"config", "ops_config", "ops_user_config", "ops_known_hosts"}
	}
	result := make(map[string][]byte)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		result[name] = data
	}
	return result
}

func TestConfigureGitHubRejectsMalformedOrMissingMetadata(t *testing.T) {
	for name, body := range map[string]string{
		"malformed": `{"ssh_keys":[`,
		"missing":   `{"ssh_keys":[]}`,
		"invalid":   `{"ssh_keys":["ssh-ed25519 not-base64"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
			defer server.Close()
			m := Manager{Home: home, Runner: configRunner{home: home}, HTTP: server.Client(), MetadataURL: server.URL}
			if err := m.ConfigureGitHub(context.Background()); err == nil {
				t.Fatal("expected metadata failure")
			}
			if _, err := os.Lstat(filepath.Join(home, ".ssh", "ops_known_hosts")); !os.IsNotExist(err) {
				t.Fatal("host-key file was created from invalid metadata")
			}
		})
	}
}

func TestConfigureGitHubRefusesUnownedOrSymlinkedManagedFiles(t *testing.T) {
	for _, name := range []string{"ops_config", "ops_known_hosts"} {
		t.Run(name+" unknown", func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".ssh")
			_ = os.Mkdir(dir, 0o700)
			path := filepath.Join(dir, name)
			_ = os.WriteFile(path, []byte("user content\n"), 0o600)
			m := managerWithMetadata(t, home)
			if err := m.ConfigureGitHub(context.Background()); err == nil || !strings.Contains(err.Error(), "unrecognized") {
				t.Fatalf("error = %v", err)
			}
			data, _ := os.ReadFile(path)
			if string(data) != "user content\n" {
				t.Fatal("unowned file was modified")
			}
		})
		t.Run(name+" symlink", func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".ssh")
			_ = os.Mkdir(dir, 0o700)
			target := filepath.Join(home, "outside")
			_ = os.WriteFile(target, []byte("outside\n"), 0o600)
			_ = os.Symlink(target, filepath.Join(dir, name))
			m := managerWithMetadata(t, home)
			if err := m.ConfigureGitHub(context.Background()); err == nil {
				t.Fatal("expected symlink rejection")
			}
			data, _ := os.ReadFile(target)
			if string(data) != "outside\n" {
				t.Fatal("symlink target was modified")
			}
		})
	}
}

func TestConfigureGitHubRefusesUnsafePreservedUserConfig(t *testing.T) {
	for _, test := range []struct {
		name    string
		symlink bool
	}{
		{name: "unrecognized existing file"},
		{name: "symlink", symlink: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".ssh")
			_ = os.Mkdir(dir, 0o700)
			_ = os.WriteFile(filepath.Join(dir, "config"), []byte("Host example.com\n"), 0o600)
			preserved := filepath.Join(dir, "ops_user_config")
			if test.symlink {
				target := filepath.Join(home, "outside")
				_ = os.WriteFile(target, []byte("outside\n"), 0o600)
				_ = os.Symlink(target, preserved)
			} else {
				_ = os.WriteFile(preserved, []byte("different user content\n"), 0o600)
			}
			m := managerWithMetadata(t, home)
			if err := m.ConfigureGitHub(context.Background()); err == nil {
				t.Fatal("expected preserved user config protection")
			}
		})
	}
}

func TestConfigureGitHubPreflightsMainConfigBeforeManagedWrites(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	_ = os.Mkdir(dir, 0o700)
	outside := filepath.Join(home, "outside-config")
	_ = os.WriteFile(outside, []byte("keep\n"), 0o600)
	_ = os.Symlink(outside, filepath.Join(dir, "config"))
	m := managerWithMetadata(t, home)
	if err := m.ConfigureGitHub(context.Background()); err == nil {
		t.Fatal("expected main config symlink rejection")
	}
	for _, name := range []string{"ops_config", "ops_known_hosts"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was written before preflight completed", name)
		}
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "keep\n" {
		t.Fatal("main config symlink target was modified")
	}
}

func TestInvalidManagedKnownHostsIsDetectedAndRepaired(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	_ = os.Mkdir(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "ops_config"), []byte(managedMarker+"\nHost github.com\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "ops_known_hosts"), []byte(managedMarker+"\ngithub.com ssh-ed25519 invalid\n"), 0o600)
	m := managerWithMetadata(t, home)
	if m.GitHubConfigured(context.Background()) {
		t.Fatal("invalid managed host-key file was accepted")
	}
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !m.GitHubConfigured(context.Background()) {
		t.Fatal("managed host-key file was not repaired")
	}
}

func TestConfigureGitHubMigratesExactLegacyOpsConfig(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	_ = os.Mkdir(dir, 0o700)
	legacy := "Host github.com\n    HostName github.com\n    User git\n    IdentityFile ~/.ssh/ops\n    IdentitiesOnly yes\n"
	_ = os.WriteFile(filepath.Join(dir, "ops_config"), []byte(legacy), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "config"), []byte("Include ~/.ssh/ops_config\n"), 0o600)
	m := managerWithMetadata(t, home)
	if err := m.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "ops_config"))
	if !strings.HasPrefix(string(data), managedMarker+"\n") {
		t.Fatal("exact legacy configuration was not migrated to marked ownership")
	}
}

func managerWithMetadata(t *testing.T, home string) Manager {
	return managerWithMetadataRunner(t, home, configRunner{home: home})
}

func managerWithMetadataRunner(t *testing.T, home string, runner run.Runner) Manager {
	t.Helper()
	body := `{"ssh_keys":["` + testHostKey("ssh-ed25519", 1) + `","` + testHostKey("ssh-rsa", 2) + `"]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
	t.Cleanup(server.Close)
	return Manager{Home: home, Runner: runner, HTTP: server.Client(), MetadataURL: server.URL}
}

func testHostKey(keyType string, fill byte) string {
	typeName := []byte(keyType)
	blob := make([]byte, 4+len(typeName)+4+32)
	blob[3] = byte(len(typeName))
	copy(blob[4:], typeName)
	offset := 4 + len(typeName)
	blob[offset+3] = 32
	for i := offset + 4; i < len(blob); i++ {
		blob[i] = fill
	}
	return keyType + " " + base64.StdEncoding.EncodeToString(blob)
}

type sshRunnerFunc func(context.Context, run.Spec) (run.Result, error)

func (f sshRunnerFunc) Run(ctx context.Context, spec run.Spec) (run.Result, error) {
	return f(ctx, spec)
}

type sshExit int

func (e sshExit) Error() string { return fmt.Sprintf("exit status %d", e) }
func (e sshExit) ExitCode() int { return int(e) }

func TestAgentIdentitiesClassification(t *testing.T) {
	key := testHostKey("ssh-ed25519", 42)
	for _, test := range []struct {
		name      string
		result    run.Result
		err       error
		available bool
		count     int
		wantError bool
	}{
		{name: "one identity", result: run.Result{Stdout: key + "\n"}, available: true, count: 1},
		{name: "empty stdout", result: run.Result{Stdout: "The agent has no identities.\n"}, err: sshExit(1), available: true},
		{name: "empty stderr", result: run.Result{Stderr: "The agent has no identities.\n"}, err: sshExit(1), available: true},
		{name: "uncontactable", err: sshExit(2)},
		{name: "exit two with output failure", err: errors.Join(sshExit(2), errors.New("capture failure")), wantError: true},
		{name: "empty with output failure", result: run.Result{Stdout: "The agent has no identities.\n"}, err: errors.Join(sshExit(1), errors.New("capture failure")), wantError: true},
		{name: "other exit one", result: run.Result{Stderr: "error fetching identities: agent refused operation\n"}, err: sshExit(1), wantError: true},
		{name: "silent exit one", err: sshExit(1), wantError: true},
		{name: "other exit", err: sshExit(255), wantError: true},
		{name: "empty text wrong exit", result: run.Result{Stdout: "The agent has no identities.\n"}, err: sshExit(255), wantError: true},
		{name: "empty with additional error", result: run.Result{Stdout: "The agent has no identities.\n", Stderr: "protocol failure"}, err: sshExit(1), wantError: true},
		{name: "substring is insufficient", result: run.Result{Stderr: "could not open key; no identities parsed"}, err: sshExit(1), wantError: true},
		{name: "unstructured failure", result: run.Result{Stderr: "Could not open a connection to your authentication agent."}, err: errors.New("exec failed"), wantError: true},
		// Existing listing semantics skip malformed lines and retain valid ones.
		{name: "mixed malformed output", result: run.Result{Stdout: "invalid\nssh-ed25519 !bad-base64\n" + key + "\n"}, available: true, count: 1},
		{name: "only malformed output", result: run.Result{Stdout: "invalid\n"}, available: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var commandErr error
			if test.err != nil {
				commandErr = fmt.Errorf("inspect: %w", &run.Error{Name: "ssh-add", Err: test.err})
			}
			m := Manager{Runner: sshRunnerFunc(func(_ context.Context, spec run.Spec) (run.Result, error) {
				if spec.Name != "ssh-add" || strings.Join(spec.Args, " ") != "-L" || spec.Interactive {
					t.Fatalf("unexpected command: %#v", spec)
				}
				return test.result, commandErr
			})}
			ids, available, err := m.AgentIdentities(context.Background())
			if available != test.available || len(ids) != test.count || (err != nil) != test.wantError {
				t.Fatalf("identities=%v available=%v err=%v", ids, available, err)
			}
			if test.wantError && err != commandErr {
				t.Fatalf("command error was not preserved: %v", err)
			}
			if len(ids) == 1 {
				want, _ := PublicFingerprint(key)
				if ids[0].Fingerprint != want || ids[0].PublicKey != key {
					t.Fatalf("identity=%v", ids[0])
				}
			}
		})
	}
}

func TestAgentIdentitiesRealOpenSSH(t *testing.T) {
	for _, tool := range []string{"ssh-agent", "ssh-add", "ssh-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	// Keep the Unix socket path short regardless of the test's full name.
	dir, err := os.MkdirTemp("", "ops-agent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "agent.sock")
	agent := exec.Command("ssh-agent", "-D", "-a", socket)
	agent.Env = append(os.Environ(), "SSH_AUTH_SOCK="+socket, "SSH_AGENT_PID=", "LC_ALL=C")
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = agent.Process.Kill()
		_ = agent.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("isolated ssh-agent did not create its socket")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m := managerFor(t)
	base := m.Runner
	m.Runner = sshRunnerFunc(func(ctx context.Context, spec run.Spec) (run.Result, error) {
		spec.Env = append(spec.Env, "SSH_AUTH_SOCK="+socket, "SSH_AGENT_PID=", "SSH_ASKPASS_REQUIRE=never")
		return base.Run(ctx, spec)
	})
	result, err := m.Runner.Run(ctx, run.Spec{Name: "ssh-add", Args: []string{"-L"}})
	t.Logf("empty: exit 1=%v stdout=%q stderr=%q", run.Exited(err, 1), result.Stdout, result.Stderr)
	if !run.Exited(err, 1) || result.Stdout != "The agent has no identities.\n" || result.Stderr != "" {
		t.Fatalf("unexpected empty-agent response: %#v, %v", result, err)
	}
	check := func(count int) []AgentIdentity {
		t.Helper()
		ids, available, err := m.AgentIdentities(ctx)
		if err != nil || !available || len(ids) != count {
			t.Fatalf("ids=%v available=%v err=%v", ids, available, err)
		}
		return ids
	}
	check(0)
	for _, socketValue := range []string{"", filepath.Join(dir, "missing.sock")} {
		unavailable := Manager{Runner: sshRunnerFunc(func(ctx context.Context, spec run.Spec) (run.Result, error) {
			spec.Env = append(spec.Env, "SSH_AUTH_SOCK="+socketValue, "SSH_AGENT_PID=")
			result, err := base.Run(ctx, spec)
			t.Logf("unavailable: exit 2=%v stdout=%q stderr=%q", run.Exited(err, 2), result.Stdout, result.Stderr)
			if !run.Exited(err, 2) || result.Stdout != "" {
				t.Fatalf("unexpected unavailable response: %#v, %v", result, err)
			}
			return result, err
		})}
		if ids, available, err := unavailable.AgentIdentities(ctx); err != nil || available || len(ids) != 0 {
			t.Fatalf("ids=%v available=%v err=%v", ids, available, err)
		}
	}
	generate(t, m, "unrelated")
	generate(t, m, "ops")
	if err := m.Load(ctx, filepath.Join(m.dir(), "unrelated")); err != nil {
		t.Fatal(err)
	}
	unrelated := check(1)[0]
	if _, err := m.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if got := check(1)[0]; got != unrelated {
		t.Fatal("discovery changed unrelated agent identity")
	}
	if err := m.Load(ctx, filepath.Join(m.dir(), "ops")); err != nil {
		t.Fatal(err)
	}
	ids := check(2)
	if ids[0] != unrelated && ids[1] != unrelated {
		t.Fatal("managed load removed unrelated agent identity")
	}
}

func TestDiscoverPrivateCorrespondenceRealOpenSSH(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		for _, layout := range []string{"matching", "mismatched", "no public", "matching elsewhere", "public only"} {
			t.Run(fmt.Sprintf("encrypted=%v/%s", encrypted, layout), func(t *testing.T) {
				m := managerFor(t)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				path := filepath.Join(m.dir(), "ops")
				passphrase := ""
				if encrypted {
					passphrase = "temporary-test-passphrase"
				}
				if output, err := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", passphrase, "-f", path).CombinedOutput(); err != nil {
					t.Fatalf("generate ops: %v: %s", err, output)
				}
				other := managerFor(t)
				generate(t, other, "B")
				publicA := readSSHConfigurationFiles(t, m.dir(), "ops.pub")["ops.pub"]
				publicB := readSSHConfigurationFiles(t, other.dir(), "B.pub")["B.pub"]
				want, err := PublicFingerprint(string(publicA))
				if err != nil {
					t.Fatal(err)
				}
				wantPublic := path + ".pub"
				switch layout {
				case "mismatched", "matching elsewhere":
					if err := os.WriteFile(path+".pub", publicB, 0o644); err != nil {
						t.Fatal(err)
					}
					wantPublic = ""
					if layout == "matching elsewhere" {
						wantPublic = filepath.Join(m.dir(), "unrelated-filename.pub")
						if err := os.WriteFile(wantPublic, publicA, 0o644); err != nil {
							t.Fatal(err)
						}
					}
				case "no public":
					if err := os.Remove(path + ".pub"); err != nil {
						t.Fatal(err)
					}
					wantPublic = ""
				case "public only":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				// OpenSSH 10.5p1's pathname -l reports B for private A + B.pub,
				// for both plain and encrypted A. Descriptor inspection must report A.
				if layout == "mismatched" {
					result, err := m.Runner.Run(ctx, run.Spec{Name: "ssh-keygen", Args: []string{"-l", "-E", "sha256", "-f", path}})
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("pathname fingerprint with mismatched sidecar: %s", strings.TrimSpace(result.Stdout))
				}
				entries, err := os.ReadDir(m.dir())
				if err != nil {
					t.Fatal(err)
				}
				var names []string
				for _, entry := range entries {
					names = append(names, entry.Name())
				}
				before := readSSHConfigurationFiles(t, m.dir(), names...)
				// Force any attempted passphrase prompt into a detectable helper.
				marker := filepath.Join(m.Home, "prompted")
				askpass := filepath.Join(m.Home, "askpass")
				if err := os.WriteFile(askpass, []byte("#!/bin/sh\ntouch \"$OPS_TEST_PROMPT_MARKER\"\nexit 1\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				base := m.Runner
				m.Runner = sshRunnerFunc(func(ctx context.Context, spec run.Spec) (run.Result, error) {
					if spec.Interactive || spec.Name != "ssh-keygen" || strings.Join(spec.Args, " ") != "-l -E sha256 -f /proc/self/fd/0" {
						t.Fatalf("unexpected inspection command: %#v", spec)
					}
					if _, ok := spec.Stdin.(*os.File); !ok {
						t.Fatal("private inspection requires the original file descriptor")
					}
					spec.Env = append(spec.Env, "SSH_ASKPASS_REQUIRE=force", "SSH_ASKPASS="+askpass, "OPS_TEST_PROMPT_MARKER="+marker)
					return base.Run(ctx, spec)
				})
				ids, err := m.Discover(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if layout == "public only" {
					if len(ids) != 0 {
						t.Fatalf("public-only file became a private identity: %v", ids)
					}
				} else {
					if len(ids) != 1 || ids[0] != (Identity{PrivatePath: path, PublicPath: wantPublic, Fingerprint: want}) {
						t.Fatalf("identities=%v, want public=%q fingerprint=%s", ids, wantPublic, want)
					}
					id, err := m.EnsureIdentity(ctx)
					if layout == "matching" {
						if err != nil || id != ids[0] {
							t.Fatalf("matching managed identity not verified: %v, %v", id, err)
						}
					} else if err == nil {
						t.Fatalf("unverified managed pair accepted: %v", id)
					}
				}
				if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("inspection attempted a passphrase prompt")
				}
				after := readSSHConfigurationFiles(t, m.dir(), names...)
				for name, data := range before {
					if !bytes.Equal(data, after[name]) {
						t.Fatalf("inspection modified %s", name)
					}
				}
			})
		}
	}
}

func TestDeleteRejectsPrivateReplacementWithStalePublic(t *testing.T) {
	m := managerFor(t)
	generate(t, m, "ops")
	ids, err := m.Discover(context.Background())
	if err != nil || len(ids) != 1 {
		t.Fatalf("identities=%v, %v", ids, err)
	}
	generate(t, m, "replacement")
	if err := os.Rename(filepath.Join(m.dir(), "replacement"), ids[0].PrivatePath); err != nil {
		t.Fatal(err)
	}
	before := readSSHConfigurationFiles(t, m.dir(), "ops", "ops.pub", "replacement.pub")
	if err := m.Delete(context.Background(), ids[0]); err == nil {
		t.Fatal("accepted replaced private key based on stale .pub")
	}
	after := readSSHConfigurationFiles(t, m.dir(), "ops", "ops.pub", "replacement.pub")
	for name, data := range before {
		if !bytes.Equal(data, after[name]) {
			t.Fatalf("failed revalidation modified %s", name)
		}
	}
}

func TestManagedIdentityRejectsNonregularFiles(t *testing.T) {
	for _, name := range []string{"ops", "ops.pub"} {
		t.Run(name, func(t *testing.T) {
			m := managerFor(t)
			if err := os.Mkdir(filepath.Join(m.dir(), name), 0o700); err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{}
			m.Runner = runner
			if _, err := m.Discover(context.Background()); err == nil {
				t.Fatal("discovery accepted nonregular managed path")
			}
			// Keep ops present to avoid the creation interaction for ops.pub.
			if name == "ops.pub" {
				if err := os.WriteFile(filepath.Join(m.dir(), "ops"), []byte("invalid"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := m.EnsureIdentity(context.Background()); err == nil || len(runner.calls) != 0 {
				t.Fatalf("nonregular managed path reached commands: %v", err)
			}
		})
	}
}

func TestPrivateFingerprintRejectsReplacementDuringInspection(t *testing.T) {
	m := managerFor(t)
	generate(t, m, "ops")
	generate(t, m, "replacement")
	path := filepath.Join(m.dir(), "ops")
	base := m.Runner
	m.Runner = sshRunnerFunc(func(ctx context.Context, spec run.Spec) (run.Result, error) {
		if err := os.Rename(filepath.Join(m.dir(), "replacement"), path); err != nil {
			t.Fatal(err)
		}
		return base.Run(ctx, spec)
	})
	if _, err := m.privateFingerprint(context.Background(), path); err == nil {
		t.Fatal("accepted fingerprint after the private path was replaced")
	}
}
