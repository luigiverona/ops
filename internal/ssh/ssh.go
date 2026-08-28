// Package ssh manages the dedicated ops SSH identity conservatively.
package ssh

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
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

// Manager owns only ~/.ssh/ops, ~/.ssh/ops.pub, and ~/.ssh/ops_config.
type Manager struct {
	Home   string
	Runner run.Runner
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
		if _, err := m.Runner.Run(ctx, run.Spec{Name: "ssh-keygen", Args: []string{"-t", "ed25519", "-f", path, "-C", "ops-managed"}, Interactive: true}); err != nil {
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
		if strings.Contains(message, "could not open") || strings.Contains(message, "no identities") {
			return nil, false, nil
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
	_, err := m.Runner.Run(ctx, run.Spec{Name: "ssh-add", Args: []string{path}, Interactive: true})
	return err
}

// ConfigureGitHub atomically adds a first-match Include and owns an isolated Host block.
func (m Manager) ConfigureGitHub(ctx context.Context) error {
	if err := secureDir(m.dir()); err != nil {
		return err
	}
	configPath := filepath.Join(m.dir(), "config")
	includePath := filepath.Join(m.dir(), "ops_config")
	managed := []byte("Host github.com\n    HostName github.com\n    User git\n    IdentityFile ~/.ssh/ops\n    IdentitiesOnly yes\n")
	if err := atomicOwnedWrite(includePath, managed, 0o600); err != nil {
		return err
	}
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
	include := "Include ~/.ssh/ops_config"
	if !hasActiveLine(existing, include) {
		updated := append([]byte(include+"\n"), existing...)
		if err := atomicOwnedWrite(configPath, updated, 0o600); err != nil {
			return err
		}
	}
	result, err := m.Runner.Run(ctx, run.Spec{Name: "ssh", Args: []string{"-G", "github.com", "-F", configPath}})
	if err != nil {
		return fmt.Errorf("verify effective SSH configuration: %w", err)
	}
	effective := strings.ToLower(result.Stdout)
	if !strings.Contains(effective, "identitiesonly yes") || !containsIdentityFile(effective, filepath.Join(m.Home, ".ssh", "ops")) {
		return errors.New("effective SSH configuration does not select ~/.ssh/ops with IdentitiesOnly")
	}
	return nil
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

func atomicOwnedWrite(path string, data []byte, mode os.FileMode) error {
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

func hasActiveLine(data []byte, want string) bool {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") && line == want {
			return true
		}
	}
	return false
}

func containsIdentityFile(output, path string) bool {
	path = strings.ToLower(path)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "identityfile" && (fields[1] == path || fields[1] == "~/.ssh/ops") {
			return true
		}
	}
	return false
}
