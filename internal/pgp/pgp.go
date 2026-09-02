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
	"syscall"

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
func (m Manager) Has(ctx context.Context, fingerprint string) (present bool, returnErr error) {
	if !aurmeta.ValidFingerprint(fingerprint) {
		return false, errors.New("invalid OpenPGP fingerprint")
	}
	home, err := m.keyringHome()
	if err != nil {
		return false, err
	}
	if info, err := os.Lstat(home); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect GnuPG home: %w", err)
	} else if err := validateGnuPGHome(info); err != nil {
		return false, fmt.Errorf("inspect GnuPG home: %w", err)
	}
	inspection, keyrings, err := copyPublicKeyrings(home)
	if err != nil {
		return false, err
	}
	defer func() {
		if err := os.RemoveAll(inspection); err != nil {
			present = false
			returnErr = errors.Join(returnErr, fmt.Errorf("clean isolated GnuPG inspection home: %w", err))
		}
	}()
	if len(keyrings) == 0 {
		return false, nil
	}
	return hasInHome(ctx, m.Runner, inspection, fingerprint, keyrings...)
}

// Import retrieves exactly fingerprint from the fixed HKPS keyserver and
// verifies the imported primary fingerprint before returning.
func (m Manager) Import(ctx context.Context, fingerprint string) (returnErr error) {
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
	defer func() {
		if err := os.RemoveAll(isolation); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("clean isolated GnuPG home: %w", err))
		}
	}()
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
	if err := ensureGnuPGHome(home); err != nil {
		return fmt.Errorf("prepare GnuPG home for signing key import: %w", err)
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
	var home string
	if m.Home != "" {
		home = m.Home
	} else if configured := os.Getenv("GNUPGHOME"); configured != "" {
		home = configured
	} else {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate GnuPG home: %w", err)
		}
		home = filepath.Join(userHome, ".gnupg")
	}
	if !filepath.IsAbs(home) {
		absolute, err := filepath.Abs(home)
		if err != nil {
			return "", fmt.Errorf("resolve GnuPG home: %w", err)
		}
		home = absolute
	}
	home = filepath.Clean(home)
	if home == string(filepath.Separator) {
		return "", errors.New("GnuPG home must not be the filesystem root")
	}
	if err := validateGnuPGParents(filepath.Dir(home)); err != nil {
		return "", err
	}
	return home, nil
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

func hasInHome(ctx context.Context, runner run.Runner, home, fingerprint string, keyrings ...string) (bool, error) {
	args := make([]string, 0, 4+len(keyrings)*2)
	if len(keyrings) > 0 {
		args = append(args, "--no-default-keyring")
		for _, keyring := range keyrings {
			args = append(args, "--keyring", keyring)
		}
	}
	args = append(args, "--with-colons", "--fingerprint", "--list-keys", "--", fingerprint)
	result, err := runner.Run(ctx, gpgSpec(home, args))
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

// copyPublicKeyrings creates an isolated, public-key-only view of an existing
// GnuPG home. GnuPG is never invoked with the real home during inspection:
// --list-keys can initialize an otherwise empty home.
func copyPublicKeyrings(home string) (string, []string, error) {
	fd, err := syscall.Open(home, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, fmt.Errorf("open GnuPG home without following links: %w", err)
	}
	defer syscall.Close(fd)
	inspection, err := os.MkdirTemp("", "ops-gpg-inspect-*")
	if err != nil {
		return "", nil, fmt.Errorf("create isolated GnuPG inspection home: %w", err)
	}
	keyrings := make([]string, 0, 2)
	for _, name := range [...]string{"pubring.kbx", "pubring.gpg"} {
		keyring, err := copyPublicKeyring(fd, inspection, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", nil, discardInspection(inspection, err)
		}
		keyrings = append(keyrings, keyring)
	}
	if _, err := os.Lstat(filepath.Join(home, "public-keys.d")); err == nil {
		return "", nil, discardInspection(inspection, errors.New("GnuPG keyboxd public-key storage cannot be inspected safely"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, discardInspection(inspection, fmt.Errorf("inspect GnuPG public-key storage: %w", err))
	}
	return inspection, keyrings, nil
}

func discardInspection(path string, cause error) error {
	if err := os.RemoveAll(path); err != nil {
		return errors.Join(cause, fmt.Errorf("clean isolated GnuPG inspection home: %w", err))
	}
	return cause
}

func copyPublicKeyring(homeFD int, inspection, name string) (string, error) {
	fd, err := syscall.Openat(homeFD, name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return "", os.ErrNotExist
		}
		return "", fmt.Errorf("open GnuPG public keyring %q: %w", name, err)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		_ = syscall.Close(fd)
		return "", fmt.Errorf("inspect GnuPG public keyring %q: %w", name, err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		_ = syscall.Close(fd)
		return "", fmt.Errorf("GnuPG public keyring %q is not a regular file", name)
	}
	source := os.NewFile(uintptr(fd), name)
	if source == nil {
		_ = syscall.Close(fd)
		return "", fmt.Errorf("open GnuPG public keyring %q", name)
	}
	defer source.Close()
	destinationPath := filepath.Join(inspection, name)
	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create isolated GnuPG public keyring %q: %w", name, err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		return "", fmt.Errorf("copy isolated GnuPG public keyring %q: %w", name, err)
	}
	if err := destination.Close(); err != nil {
		return "", fmt.Errorf("close isolated GnuPG public keyring %q: %w", name, err)
	}
	return destinationPath, nil
}

func ensureGnuPGHome(home string) error {
	if err := validateGnuPGParents(filepath.Dir(home)); err != nil {
		return err
	}
	info, err := os.Lstat(home)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(home, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create GnuPG home: %w", err)
		}
		info, err = os.Lstat(home)
	}
	if err != nil {
		return fmt.Errorf("inspect GnuPG home: %w", err)
	}
	return validateGnuPGHome(info)
}

func validateGnuPGHome(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("GnuPG home must not be a symlink")
	}
	if !info.IsDir() {
		return errors.New("GnuPG home is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("GnuPG home is not owned by the normal user")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("GnuPG home permissions are not user-only")
	}
	return nil
}

func validateGnuPGParents(path string) error {
	path = filepath.Clean(path)
	for {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect GnuPG home parent: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("GnuPG home parent must be a real directory")
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func primaryFingerprintPresent(output, want string) (bool, error) {
	expectFingerprint := false
	var primary []string
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		switch fields[0] {
		case "pub":
			if len(fields) < 10 {
				return false, errors.New("gpg returned malformed key metadata")
			}
			if expectFingerprint {
				return false, errors.New("gpg returned malformed primary key metadata")
			}
			expectFingerprint = true
		case "fpr":
			if !expectFingerprint {
				continue
			}
			if len(fields) < 10 {
				return false, errors.New("gpg returned malformed key metadata")
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
