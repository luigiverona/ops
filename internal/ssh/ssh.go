// Package ssh manages the dedicated ops SSH identity conservatively.
package ssh

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/luigiverona/ops/internal/run"
)

// Identity is a content-validated private identity and its matching public key, if present.
type Identity struct {
	PrivatePath string
	PublicPath  string
	Fingerprint string
}

// AgentIdentity is loaded agent state and is never interpreted as permission to delete files.
type AgentIdentity struct {
	PublicKey   string
	Fingerprint string
}

// Manager owns only ~/.ssh/ops, ~/.ssh/ops.pub, ~/.ssh/ops_config,
// ~/.ssh/ops_known_hosts, and the marked dispatcher in ~/.ssh/config. Existing
// user configuration is preserved byte-for-byte in ~/.ssh/ops_user_config.
type Manager struct {
	Home        string
	Runner      run.Runner
	HTTP        *http.Client
	MetadataURL string
}

func (m Manager) dir() string { return filepath.Join(m.Home, ".ssh") }

// Discover validates all regular files by content and never follows symlinks.
func (m Manager) Discover(ctx context.Context) ([]Identity, error) {
	entries, err := os.ReadDir(m.dir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	public := make(map[string]string)
	type private struct{ path, fingerprint string }
	var privateKeys []private
	for _, entry := range entries {
		if protectedName(entry.Name()) {
			continue
		}
		path := filepath.Join(m.dir(), entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if line, ok := singlePublicKey(data); ok {
			fingerprint, err := PublicFingerprint(line)
			if err == nil {
				public[fingerprint] = path
			}
			continue
		}
		if !privateHeader(data) {
			continue
		}
		result, err := m.Runner.Run(ctx, run.Spec{Name: "ssh-keygen", Args: []string{"-l", "-E", "sha256", "-f", path}, Stdin: strings.NewReader("")})
		if err != nil {
			continue
		}
		fingerprint := fingerprintField(result.Stdout)
		if fingerprint != "" {
			privateKeys = append(privateKeys, private{path, fingerprint})
		}
	}
	identities := make([]Identity, 0, len(privateKeys))
	for _, key := range privateKeys {
		identities = append(identities, Identity{PrivatePath: key.path, PublicPath: public[key.fingerprint], Fingerprint: key.fingerprint})
	}
	return identities, nil
}

// Delete removes only the revalidated files recorded in identity.
func (m Manager) Delete(ctx context.Context, identity Identity) error {
	for _, path := range []string{identity.PrivatePath, identity.PublicPath} {
		if path == "" {
			continue
		}
		if filepath.Dir(filepath.Clean(path)) != filepath.Clean(m.dir()) {
			return fmt.Errorf("refusing identity path outside %s", m.dir())
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular identity file %s", path)
		}
		if err := m.verifyFingerprint(ctx, path, identity.Fingerprint); err != nil {
			return err
		}
	}
	for _, path := range []string{identity.PrivatePath, identity.PublicPath} {
		if path != "" {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m Manager) verifyFingerprint(ctx context.Context, path, want string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if line, ok := singlePublicKey(data); ok {
		got, err := PublicFingerprint(line)
		if err != nil || got != want {
			return errors.New("identity changed since review")
		}
		return nil
	}
	result, err := m.Runner.Run(ctx, run.Spec{Name: "ssh-keygen", Args: []string{"-l", "-E", "sha256", "-f", path}, Stdin: strings.NewReader("")})
	if err != nil || fingerprintField(result.Stdout) != want {
		return errors.New("identity changed since review")
	}
	return nil
}

// EnsureIdentity creates the managed Ed25519 identity through ssh-keygen's normal passphrase interaction.
func (m Manager) EnsureIdentity(ctx context.Context) (Identity, error) {
	if err := secureDir(m.dir()); err != nil {
		return Identity{}, err
	}
	path := filepath.Join(m.dir(), "ops")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if _, err := m.Runner.Run(ctx, run.Spec{Name: "ssh-keygen", Args: []string{"-q", "-t", "ed25519", "-f", path, "-C", "ops-managed"}, Interactive: true, Interaction: "SSH key passphrase prompt"}); err != nil {
			return Identity{}, err
		}
	}
	identities, err := m.Discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	for _, identity := range identities {
		if identity.PrivatePath == path && identity.PublicPath == path+".pub" {
			_ = os.Chmod(path, 0o600)
			_ = os.Chmod(path+".pub", 0o644)
			return identity, nil
		}
	}
	return Identity{}, errors.New("managed SSH identity verification failed")
}

// AgentIdentities inspects loaded identities independently from local files.
func (m Manager) AgentIdentities(ctx context.Context) ([]AgentIdentity, bool, error) {
	result, err := m.Runner.Run(ctx, run.Spec{Name: "ssh-add", Args: []string{"-L"}})
	if err != nil {
		message := strings.ToLower(result.Stderr)
		if strings.Contains(message, "could not open") {
			return nil, false, nil
		}
		if strings.Contains(message, "no identities") {
			return nil, true, nil
		}
		return nil, false, err
	}
	var identities []AgentIdentity
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fingerprint, err := PublicFingerprint(line)
		if err != nil {
			continue
		}
		identities = append(identities, AgentIdentity{PublicKey: line, Fingerprint: fingerprint})
	}
	return identities, true, nil
}

func (m Manager) Unload(ctx context.Context, identity AgentIdentity) error {
	_, err := m.Runner.Run(ctx, run.Spec{Name: "ssh-add", Args: []string{"-d", "-"}, Stdin: strings.NewReader(identity.PublicKey + "\n")})
	return err
}

func (m Manager) Load(ctx context.Context, path string) error {
	_, err := m.Runner.Run(ctx, run.Spec{Name: "ssh-add", Args: []string{path}, Interactive: true, Interaction: "SSH key passphrase prompt"})
	return err
}

// ConfigureGitHub isolates GitHub from additive user IdentityFile directives while
// preserving the user's configuration for every other host.
func (m Manager) ConfigureGitHub(ctx context.Context) error {
	if err := secureDir(m.dir()); err != nil {
		return err
	}
	configPath := filepath.Join(m.dir(), "config")
	includePath := filepath.Join(m.dir(), "ops_config")
	userConfigPath := filepath.Join(m.dir(), "ops_user_config")
	knownHostsPath := filepath.Join(m.dir(), "ops_known_hosts")
	legacy := []byte("Host github.com\n    HostName github.com\n    User git\n    IdentityFile ~/.ssh/ops\n    IdentitiesOnly yes\n")
	identityPath, err := sshConfigArgument(filepath.Join(m.dir(), "ops"))
	if err != nil {
		return err
	}
	knownHostsArgument, err := sshConfigArgument(knownHostsPath)
	if err != nil {
		return err
	}
	dispatcher, err := renderGitHubDispatcher(includePath, userConfigPath)
	if err != nil {
		return err
	}
	managed := []byte(fmt.Sprintf("%s\nHost github.com\n    HostName github.com\n    User git\n    IdentityFile %s\n    IdentitiesOnly yes\n    UserKnownHostsFile %s\n    StrictHostKeyChecking yes\n", managedMarker, identityPath, knownHostsArgument))
	var existing []byte
	if info, err := os.Lstat(configPath); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("refusing non-regular ~/.ssh/config")
		}
		existing, err = os.ReadFile(configPath)
		if err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dispatcherCurrent := string(existing) == string(dispatcher)
	preserved, preservedExists, err := readSafeUserConfig(userConfigPath)
	if err != nil {
		return err
	}
	if !dispatcherCurrent {
		userContent, err := withoutManagedDispatcher(existing)
		if err != nil {
			return err
		}
		if preservedExists && string(preserved) != string(userContent) {
			return errors.New("refusing to overwrite existing ~/.ssh/ops_user_config")
		}
		preserved = userContent
	}
	hostKeys, err := m.fetchGitHubHostKeys(ctx)
	if err != nil {
		return fmt.Errorf("retrieve official GitHub SSH host keys: %w", err)
	}
	knownHosts := renderKnownHosts(hostKeys)
	if err := checkManagedTarget(includePath, legacy); err != nil {
		return err
	}
	if err := checkManagedTarget(knownHostsPath); err != nil {
		return err
	}
	if err := atomicManagedWrite(knownHostsPath, knownHosts, 0o600); err != nil {
		return err
	}
	if err := atomicManagedWrite(includePath, managed, 0o600, legacy); err != nil {
		return err
	}
	if !preservedExists {
		if err := atomicRegularWrite(userConfigPath, preserved, 0o600); err != nil {
			return err
		}
	}
	if !dispatcherCurrent {
		if err := atomicRegularWrite(configPath, dispatcher, 0o600); err != nil {
			return err
		}
	}
	result, err := m.Runner.Run(ctx, run.Spec{Name: "ssh", Args: []string{"-G", "github.com", "-F", configPath}})
	if err != nil {
		return fmt.Errorf("verify effective SSH configuration: %w", err)
	}
	if !effectiveGitHubConfig(result.Stdout, m.Home) {
		return errors.New("effective SSH configuration does not enforce the managed identity and host-key trust")
	}
	return nil
}

// GitHubConfigured verifies effective configuration without changing files.
func (m Manager) GitHubConfigured(ctx context.Context) bool {
	if !recognizedManagedFile(filepath.Join(m.dir(), "ops_config")) || !validManagedKnownHosts(filepath.Join(m.dir(), "ops_known_hosts")) {
		return false
	}
	configPath := filepath.Join(m.dir(), "config")
	dispatcher, err := renderGitHubDispatcher(filepath.Join(m.dir(), "ops_config"), filepath.Join(m.dir(), "ops_user_config"))
	if err != nil {
		return false
	}
	if !recognizedExactFile(configPath, dispatcher) {
		return false
	}
	if _, exists, err := readSafeUserConfig(filepath.Join(m.dir(), "ops_user_config")); err != nil || !exists {
		return false
	}
	result, err := m.Runner.Run(ctx, run.Spec{Name: "ssh", Args: []string{"-G", "github.com", "-F", configPath}})
	if err != nil {
		return false
	}
	return effectiveGitHubConfig(result.Stdout, m.Home)
}

// HostKeyFreshness records whether recognized managed host keys match current
// authoritative metadata without conflating an unavailable check with staleness.
type HostKeyFreshness string

const (
	HostKeyFreshnessUnknown     HostKeyFreshness = "unknown"
	HostKeyFreshnessCurrent     HostKeyFreshness = "current"
	HostKeyFreshnessStale       HostKeyFreshness = "stale"
	HostKeyFreshnessUnavailable HostKeyFreshness = "unavailable"
)

// GitHubConfigurationStatus separates local configuration safety from remote
// host-key freshness. LocalReady is false when managed files are not recognized.
type GitHubConfigurationStatus struct {
	LocalReady bool
	Freshness  HostKeyFreshness
}

// InspectGitHubConfiguration performs read-only local verification and, only
// for recognized configuration, compares host keys with official metadata.
func (m Manager) InspectGitHubConfiguration(ctx context.Context) (GitHubConfigurationStatus, error) {
	if !m.GitHubConfigured(ctx) {
		return GitHubConfigurationStatus{Freshness: HostKeyFreshnessUnknown}, nil
	}
	hostKeys, err := m.fetchGitHubHostKeys(ctx)
	if err != nil {
		if metadataUnavailable(err) {
			return GitHubConfigurationStatus{LocalReady: true, Freshness: HostKeyFreshnessUnavailable}, nil
		}
		return GitHubConfigurationStatus{}, err
	}
	freshness := HostKeyFreshnessStale
	if recognizedExactFile(filepath.Join(m.dir(), "ops_known_hosts"), renderKnownHosts(hostKeys)) {
		freshness = HostKeyFreshnessCurrent
	}
	return GitHubConfigurationStatus{LocalReady: true, Freshness: freshness}, nil
}

// PublicFingerprint validates a single public-key line and returns its OpenSSH SHA256 fingerprint.
func PublicFingerprint(line string) (string, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 || !publicType(fields[0]) || strings.Contains(fields[0], "-cert-") {
		return "", errors.New("not a supported SSH public key")
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil || len(blob) == 0 {
		return "", errors.New("invalid SSH public key encoding")
	}
	if len(blob) < 4 {
		return "", errors.New("invalid SSH public key material")
	}
	length := int(blob[0])<<24 | int(blob[1])<<16 | int(blob[2])<<8 | int(blob[3])
	if length <= 0 || 4+length >= len(blob) || string(blob[4:4+length]) != fields[0] {
		return "", errors.New("SSH public key type does not match key material")
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

func singlePublicKey(data []byte) (string, bool) {
	var line string
	for _, candidate := range strings.Split(string(data), "\n") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || strings.HasPrefix(candidate, "#") {
			continue
		}
		if line != "" {
			return "", false
		}
		line = candidate
	}
	fields := strings.Fields(line)
	return line, len(fields) >= 2 && publicType(fields[0]) && !strings.Contains(fields[0], "-cert-")
}

func publicType(value string) bool {
	return value == "ssh-ed25519" || value == "ssh-rsa" || strings.HasPrefix(value, "ecdsa-sha2-") || strings.HasPrefix(value, "sk-ssh-ed25519@") || strings.HasPrefix(value, "sk-ecdsa-sha2-")
}

func privateHeader(data []byte) bool {
	text := string(data)
	return strings.Contains(text, "-----BEGIN OPENSSH PRIVATE KEY-----") || strings.Contains(text, "-----BEGIN RSA PRIVATE KEY-----") || strings.Contains(text, "-----BEGIN EC PRIVATE KEY-----") || strings.Contains(text, "-----BEGIN PRIVATE KEY-----")
}

func protectedName(name string) bool {
	switch name {
	case "config", "known_hosts", "known_hosts.old", "authorized_keys", "authorized_keys2", "ops_config", "ops_user_config", "ops_known_hosts":
		return true
	}
	return strings.HasSuffix(name, "-cert.pub") || strings.HasSuffix(name, "-cert")
}

func fingerprintField(output string) string {
	for _, field := range strings.Fields(output) {
		if strings.HasPrefix(field, "SHA256:") {
			return field
		}
	}
	return ""
}

func secureDir(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("~/.ssh is not a regular directory")
		}
		return os.Chmod(path, 0o700)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Mkdir(path, 0o700)
}

func atomicRegularWrite(path string, data []byte, mode os.FileMode) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular file %s", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".ops-ssh-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func atomicManagedWrite(path string, data []byte, mode os.FileMode, legacy ...[]byte) error {
	if err := checkManagedTarget(path, legacy...); err != nil {
		return err
	}
	return atomicRegularWrite(path, data, mode)
}

func checkManagedTarget(path string, legacy ...[]byte) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular managed path %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if hasManagedMarker(data) {
		return nil
	}
	for _, recognized := range legacy {
		if string(data) == string(recognized) {
			return nil
		}
	}
	return fmt.Errorf("refusing to overwrite unrecognized existing file %s", path)
}

func recognizedManagedFile(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && hasManagedMarker(data)
}

func recognizedExactFile(path string, want []byte) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && string(data) == string(want)
}

func hasManagedMarker(data []byte) bool {
	line, _, _ := strings.Cut(string(data), "\n")
	return line == managedMarker
}

func effectiveGitHubConfig(output, home string) bool {
	parsed, ok := parseEffectiveSSHConfig(output)
	if !ok || !parsed.singleEqualFold("host", "github.com") ||
		!parsed.singleEqualFold("hostname", "github.com") ||
		!parsed.single("user", "git") ||
		!parsed.enabled("identitiesonly") ||
		!parsed.enabled("stricthostkeychecking") {
		return false
	}
	return parsed.singlePath("identityfile", filepath.Join(home, ".ssh", "ops"), "~/.ssh/ops") &&
		parsed.singlePath("userknownhostsfile", filepath.Join(home, ".ssh", "ops_known_hosts"), "~/.ssh/ops_known_hosts")
}

type effectiveSSHConfig map[string][][]string

func parseEffectiveSSHConfig(output string) (effectiveSSHConfig, bool) {
	wanted := map[string]bool{
		"host": true, "hostname": true, "user": true,
		"identitiesonly": true, "stricthostkeychecking": true,
		"identityfile": true, "userknownhostsfile": true,
	}
	parsed := make(effectiveSSHConfig)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		key := strings.ToLower(fields[0])
		if !wanted[key] {
			continue
		}
		if len(fields) < 2 {
			return nil, false
		}
		parsed[key] = append(parsed[key], fields[1:])
	}
	return parsed, true
}

func (c effectiveSSHConfig) single(key, want string) bool {
	values := c[key]
	return len(values) == 1 && len(values[0]) == 1 && values[0][0] == want
}

func (c effectiveSSHConfig) singleEqualFold(key, want string) bool {
	values := c[key]
	return len(values) == 1 && len(values[0]) == 1 && strings.EqualFold(values[0][0], want)
}

func (c effectiveSSHConfig) enabled(key string) bool {
	values := c[key]
	if len(values) != 1 || len(values[0]) != 1 {
		return false
	}
	value := strings.ToLower(values[0][0])
	return value == "yes" || value == "true"
}

func (c effectiveSSHConfig) singlePath(key string, allowed ...string) bool {
	values := c[key]
	if len(values) != 1 || len(values[0]) != 1 {
		return false
	}
	for _, value := range allowed {
		if values[0][0] == value {
			return true
		}
	}
	return false
}

func sshConfigArgument(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("SSH configuration path contains invalid characters")
	}
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`, nil
}

func renderGitHubDispatcher(includePath, userConfigPath string) ([]byte, error) {
	includeArgument, err := sshConfigArgument(includePath)
	if err != nil {
		return nil, err
	}
	userConfigArgument, err := sshConfigArgument(userConfigPath)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("%s\nInclude %s\n# Preserve user configuration for every host except github.com.\nHost * !github.com\n    Include %s\n%s\n", managedIncludeStart, includeArgument, userConfigArgument, managedIncludeEnd)), nil
}

func readSafeUserConfig(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("refusing non-regular ~/.ssh/ops_user_config")
	}
	data, err := os.ReadFile(path)
	return data, true, err
}

func withoutManagedDispatcher(data []byte) ([]byte, error) {
	var output strings.Builder
	inside := false
	starts, ends := 0, 0
	for _, line := range strings.SplitAfter(string(data), "\n") {
		text := strings.TrimSuffix(line, "\n")
		switch text {
		case managedIncludeStart:
			starts++
			if inside {
				return nil, errors.New("malformed ops managed SSH configuration markers")
			}
			inside = true
		case managedIncludeEnd:
			ends++
			if !inside {
				return nil, errors.New("malformed ops managed SSH configuration markers")
			}
			inside = false
		default:
			if !inside {
				output.WriteString(line)
			}
		}
	}
	if inside || starts != ends || starts > 1 {
		return nil, errors.New("malformed ops managed SSH configuration markers")
	}
	return []byte(output.String()), nil
}
