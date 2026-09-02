// Package pgp manages only exact, public signing keys needed by reviewed AUR sources.
package pgp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/run"
)

const keyserver = "hkps://keyserver.ubuntu.com"

// Manager inspects and imports exact public keys in the normal user's keyring.
type Manager struct {
	Runner run.Runner
	// Home is only for selecting the normal user's existing GnuPG home. An
	// empty value follows GNUPGHOME, then the standard ~/.gnupg location.
	Home string
}

// Has reports whether fingerprint is present as an exact primary fingerprint.
func (m Manager) Has(ctx context.Context, fingerprint string) (bool, error) {
	if !aurmeta.ValidFingerprint(fingerprint) {
		return false, errors.New("invalid OpenPGP fingerprint")
	}
	home, err := m.keyringHome()
	if err != nil {
		return false, err
	}
	if info, err := os.Stat(home); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect GnuPG home: %w", err)
	} else if !info.IsDir() {
		return false, errors.New("GnuPG home is not a directory")
	}
	return hasInHome(ctx, m.Runner, home, fingerprint)
}

// Import retrieves exactly fingerprint from the fixed HKPS keyserver and
// verifies the imported primary fingerprint before returning.
func (m Manager) Import(ctx context.Context, fingerprint string) error {
	if !aurmeta.ValidFingerprint(fingerprint) {
		return errors.New("invalid OpenPGP fingerprint")
	}
	present, err := m.Has(ctx, fingerprint)
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	home, err := m.keyringHome()
	if err != nil {
		return err
	}
	isolation, err := os.MkdirTemp("", "ops-gpg-key-*")
	if err != nil {
		return fmt.Errorf("create isolated GnuPG home: %w", err)
	}
	defer os.RemoveAll(isolation)
	if _, err := m.Runner.Run(ctx, gpgSpec(isolation, []string{"--keyserver", keyserver, "--recv-keys", fingerprint})); err != nil {
		return fmt.Errorf("retrieve exact signing key %s: %w", fingerprint, err)
	}
	present, err = hasInHome(ctx, m.Runner, isolation, fingerprint)
	if err != nil {
		return err
	}
	if !present {
		return errors.New("keyserver did not provide the planned signing-key fingerprint")
	}
	key, err := m.Runner.Run(ctx, gpgSpec(isolation, []string{"--export", "--", fingerprint}))
	if err != nil || key.Stdout == "" {
		if err != nil {
			return fmt.Errorf("export verified signing key: %w", err)
		}
		return errors.New("export verified signing key: no public key data")
	}
	if _, err := m.Runner.Run(ctx, gpgSpec(home, []string{"--import"}, strings.NewReader(key.Stdout))); err != nil {
		return fmt.Errorf("import verified signing key %s: %w", fingerprint, err)
	}
	present, err = m.Has(ctx, fingerprint)
	if err != nil {
		return err
	}
	if !present {
		return errors.New("imported signing key does not match the planned fingerprint")
	}
	return nil
}

func (m Manager) keyringHome() (string, error) {
	if m.Home != "" {
		return m.Home, nil
	}
	if home := os.Getenv("GNUPGHOME"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate GnuPG home: %w", err)
	}
	return filepath.Join(home, ".gnupg"), nil
}

func gpgSpec(home string, args []string, stdin ...io.Reader) run.Spec {
	base := []string{"--no-options", "--batch", "--no-tty", "--homedir", home}
	base = append(base, args...)
	input := io.Reader(strings.NewReader(""))
	if len(stdin) == 1 {
		input = stdin[0]
	}
	return run.Spec{Name: "gpg", Args: base, Env: []string{"LC_ALL=C"}, Stdin: input}
}

func hasInHome(ctx context.Context, runner run.Runner, home, fingerprint string) (bool, error) {
	result, err := runner.Run(ctx, gpgSpec(home, []string{"--with-colons", "--fingerprint", "--list-keys", "--", fingerprint}))
	if err != nil {
		if missingKey(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect signing key: %w", err)
	}
	present, err := primaryFingerprintPresent(result.Stdout, fingerprint)
	if err != nil {
		return false, err
	}
	return present, nil
}

func primaryFingerprintPresent(output, want string) (bool, error) {
	expectFingerprint := false
	var primary []string
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 10 {
			return false, errors.New("gpg returned malformed key metadata")
		}
		switch fields[0] {
		case "pub":
			if expectFingerprint {
				return false, errors.New("gpg returned malformed primary key metadata")
			}
			expectFingerprint = true
		case "fpr":
			if !expectFingerprint {
				continue
			}
			expectFingerprint = false
			fingerprint := strings.ToUpper(fields[9])
			if !aurmeta.ValidFingerprint(fingerprint) {
				return false, errors.New("gpg returned an invalid primary fingerprint")
			}
			primary = append(primary, fingerprint)
		}
	}
	if expectFingerprint {
		return false, errors.New("gpg returned incomplete primary key metadata")
	}
	if len(primary) == 0 {
		return false, nil
	}
	if len(primary) != 1 {
		return false, errors.New("gpg returned ambiguous primary key metadata")
	}
	return primary[0] == want, nil
}

func missingKey(err error) bool {
	var commandErr *run.Error
	return errors.As(err, &commandErr) && strings.Contains(commandErr.Stderr, "error reading key: No public key")
}
