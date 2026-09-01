// Package github delegates authentication and account SSH key management to gh.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/luigiverona/ops/internal/run"
	sshops "github.com/luigiverona/ops/internal/ssh"
)

// Key is a GitHub account SSH key with a locally computed fingerprint.
type Key struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Key         string `json:"key"`
	Fingerprint string `json:"-"`
}

// Manager invokes gh without handling tokens itself.
type Manager struct{ Runner run.Runner }

const sshKeyScope = "admin:public_key"

func (m Manager) Authenticated(ctx context.Context) bool {
	_, err := m.Runner.Run(ctx, run.Spec{Name: "gh", Args: []string{"auth", "status", "--hostname", "github.com", "--active"}})
	return err == nil
}

func (m Manager) Login(ctx context.Context) error {
	_, err := m.Runner.Run(ctx, run.Spec{Name: "gh", Args: []string{"auth", "login", "--hostname", "github.com", "--git-protocol", "ssh", "--skip-ssh-key", "--web", "--scopes", sshKeyScope}, Interactive: true, Interaction: "GitHub device authentication"})
	if err != nil {
		return err
	}
	if !m.Authenticated(ctx) {
		return errors.New("GitHub authentication verification failed")
	}
	return nil
}

// RefreshSSHKeyScope adds only the authorization needed to enumerate and
// manage account SSH keys. gh continues to own all credential storage.
func (m Manager) RefreshSSHKeyScope(ctx context.Context) error {
	_, err := m.Runner.Run(ctx, run.Spec{Name: "gh", Args: []string{"auth", "refresh", "--hostname", "github.com", "--scopes", sshKeyScope}, Interactive: true, Interaction: "GitHub SSH-key authorization"})
	if err != nil {
		return err
	}
	if !m.Authenticated(ctx) {
		return errors.New("GitHub authentication verification failed")
	}
	return nil
}

// IsSSHKeyScopeError identifies GitHub's documented insufficient-scope API
// response without inspecting a token or credential store.
func IsSSHKeyScopeError(err error) bool {
	var commandErr *run.Error
	if !errors.As(err, &commandErr) || commandErr.Name != "gh" || !sshKeyAPI(commandErr.Args) {
		return false
	}
	return strings.Contains(strings.ToLower(commandErr.Stderr), `this api operation needs the "admin:public_key" scope`)
}

func sshKeyAPI(args []string) bool {
	return len(args) == 3 && args[0] == "api" && args[1] == "--paginate" && args[2] == "user/keys"
}

func (m Manager) Keys(ctx context.Context) ([]Key, error) {
	result, err := m.Runner.Run(ctx, run.Spec{Name: "gh", Args: []string{"api", "--paginate", "user/keys"}})
	if err != nil {
		return nil, err
	}
	var keys []Key
	decoder := json.NewDecoder(strings.NewReader(result.Stdout))
	for {
		var page []Key
		if err := decoder.Decode(&page); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("parse GitHub SSH keys: %w", err)
		}
		keys = append(keys, page...)
	}
	for i := range keys {
		fingerprint, err := sshops.PublicFingerprint(keys[i].Key)
		if err != nil {
			return nil, fmt.Errorf("validate GitHub SSH key %d: %w", keys[i].ID, err)
		}
		keys[i].Fingerprint = fingerprint
	}
	return keys, nil
}

func (m Manager) Delete(ctx context.Context, key Key) error {
	if key.ID <= 0 {
		return errors.New("invalid GitHub SSH key ID")
	}
	_, err := m.Runner.Run(ctx, run.Spec{Name: "gh", Args: []string{"ssh-key", "delete", strconv.FormatInt(key.ID, 10), "--yes"}})
	return err
}

// AddManaged adds the managed public key unless its fingerprint is already registered.
func (m Manager) AddManaged(ctx context.Context, path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false, errors.New("managed SSH public key is unavailable or unsafe")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	fingerprint, err := sshops.PublicFingerprint(strings.TrimSpace(string(data)))
	if err != nil {
		return false, err
	}
	keys, err := m.Keys(ctx)
	if err != nil {
		return false, err
	}
	for _, key := range keys {
		if key.Fingerprint == fingerprint {
			return false, nil
		}
	}
	suffix := strings.TrimPrefix(fingerprint, "SHA256:")
	if len(suffix) > 12 {
		suffix = suffix[:12]
	}
	title := "ops-workstation-" + suffix
	if _, err := m.Runner.Run(ctx, run.Spec{Name: "gh", Args: []string{"ssh-key", "add", path, "--type", "authentication", "--title", title}}); err != nil {
		return false, err
	}
	return true, nil
}

// VerifySSH accepts GitHub's intentional exit 1 when its success message proves authentication.
func (m Manager) VerifySSH(ctx context.Context) error {
	result, err := m.Runner.Run(ctx, run.Spec{
		Name:  "ssh",
		Args:  []string{"-o", "BatchMode=yes", "-T", "git@github.com"},
		Stdin: strings.NewReader(""),
	})
	combined := strings.ToLower(result.Stdout + "\n" + result.Stderr)
	if strings.Contains(combined, "successfully authenticated") {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("GitHub SSH authentication was not confirmed")
}
