package arch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/run"
)

// runOfficial keeps configured custom repositories out of package mutations,
// including implicit dependencies. The general full upgrade uses the user's
// complete configuration so available custom rebuilds are included. The independent
// source configuration is always streamed into existing protected staging;
// pacman never consumes a user-writable configuration path as root.
func (m Manager) runOfficial(ctx context.Context, spec run.Spec) (returnErr error) {
	result, err := m.Runner.Run(ctx, run.Spec{Name: "pacman-conf", FailureOutput: run.FailureStderr})
	if err != nil {
		return fmt.Errorf("inspect package repository configuration: %w", err)
	}
	configuration, _, err := archrepo.OfficialConfig(result.Stdout)
	if err != nil {
		return err
	}

	var prepared *archtrust.Prepared
	operation := ""
	var targets []string
	for i, arg := range spec.Args {
		if arg == "-S" || arg == "-U" {
			operation = arg
		}
		if arg == "--" {
			targets = spec.Args[i+1:]
			break
		}
	}
	if operation == "-S" {
		transaction, err := archrepo.Transaction(ctx, m.Runner, targets)
		if err != nil {
			return err
		}
		prepared, err = archrepo.Prepare(ctx, m.Runner, transaction)
		if err != nil {
			return err
		}
		defer prepared.Close()
	} else if operation == "-U" {
		// Approved AUR artifacts may use already verified installed dependencies,
		// but cannot introduce an unplanned implicit repository transaction.
		configuration = strings.Split(configuration, "[core]")[0]
	} else {
		return fmt.Errorf("unsupported official package mutation")
	}

	if err := m.validateStageParent(ctx); err != nil {
		return err
	}
	result, err = m.Runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"-n", "mktemp", "--directory", "--tmpdir=" + artifactStageParent, "ops-paru-OFFICIALXXXXXXXXXXXX"}, FailureOutput: run.FailureStderr})
	if err != nil {
		return err
	}
	dir, err := validatedStageDir(result.Stdout)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "pacman.conf")
	staged := []string{path}
	syncDir := filepath.Join(dir, "sync")
	var syncFiles []string
	defer func() {
		if syncFiles != nil {
			returnErr = errors.Join(returnErr, m.cleanupArtifactStage(syncDir, syncFiles))
		}
		returnErr = errors.Join(returnErr, m.cleanupArtifactStage(dir, staged))
	}()

	if err := m.validateProtectedPath(ctx, dir, true); err != nil {
		return err
	}
	_, err = m.Runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"-n", "install", "--mode=0600", "--", "/dev/stdin", path}, Stdin: strings.NewReader(configuration), FailureOutput: run.FailureStderr})
	if err != nil {
		return err
	}
	if err := m.validateProtectedPath(ctx, path, false); err != nil {
		return err
	}

	if prepared != nil {
		if _, err = m.Runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"-n", "install", "-d", "--mode=0700", "--", syncDir}, FailureOutput: run.FailureStderr}); err != nil {
			return err
		}
		syncFiles = []string{}
		if err := m.validateProtectedPath(ctx, syncDir, true); err != nil {
			return err
		}
		stage := func(path string, input io.Reader, digest [32]byte) error {
			if _, err := m.Runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"-n", "install", "--mode=0600", "--", "/dev/stdin", path}, Stdin: input, FailureOutput: run.FailureStderr}); err != nil {
				return err
			}
			if err := m.validateProtectedPath(ctx, path, false); err != nil {
				return err
			}
			got, err := m.Runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"-n", "sha256sum", "--", path}, FailureOutput: run.FailureStderr})
			if err != nil {
				return err
			}
			if strings.TrimSuffix(got.Stdout, "\n") != fmt.Sprintf("%x  %s", digest, path) {
				return fmt.Errorf("protected official evidence digest mismatch")
			}
			return nil
		}
		for _, repo := range archrepo.Repositories() {
			data, ok := prepared.Databases[repo]
			if !ok {
				return fmt.Errorf("incomplete official source snapshot")
			}
			destination := filepath.Join(syncDir, repo+".db")
			syncFiles = append(syncFiles, destination)
			if err := stage(destination, bytes.NewReader(data), sha256.Sum256(data)); err != nil {
				return err
			}
		}
		for _, parent := range []string{"/var", "/var/cache", "/var/cache/pacman", "/var/cache/pacman/pkg"} {
			if err := m.validateProtectedPath(ctx, parent, true); err != nil {
				return err
			}
		}
		for i, archive := range prepared.Archives {
			if _, _, err := archrepo.Split(archive.Target); err != nil {
				return err
			}
			if filepath.Base(archive.Filename) != archive.Filename || strings.ContainsAny(archive.Filename, "\\\r\n") {
				return fmt.Errorf("unsafe authenticated archive filename")
			}
			destination := filepath.Join(dir, fmt.Sprintf("package-%03d.pkg.tar", i))
			staged = append(staged, destination)
			if err := stage(destination, archive.File, archive.Digest); err != nil {
				return err
			}
			// This is an authenticated archive cache, not an installation receipt.
			// install -T replaces a same-name cache entry without following its leaf
			// symlink; all parent directories are protected and were checked above.
			cache := filepath.Join("/var/cache/pacman/pkg", archive.Filename)
			if _, err := m.Runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"-n", "install", "--mode=0644", "-T", "--", destination, cache}, FailureOutput: run.FailureStderr}); err != nil {
				return err
			}
			if err := m.validateProtectedPath(ctx, cache, false); err != nil {
				return err
			}
		}
	}
	// Insert before the option terminator, retaining the transaction's terminal
	// and install-reason policy.
	args := append([]string(nil), spec.Args...)
	index := len(args)
	for i, arg := range args {
		if arg == "--" {
			index = i
			break
		}
	}
	spec.Args = append(append(append([]string(nil), args[:index]...), "--config", path), args[index:]...)

	if prepared != nil {
		// A private mount namespace gives pacman the authenticated sync snapshot
		// while preserving its real local database and normal database lock. A
		// separate DBPath plus a local-db symlink would use the wrong lock.

		if len(spec.Args) < 2 || spec.Args[0] != "-n" || spec.Args[1] != "pacman" {
			return fmt.Errorf("invalid official transaction command")
		}
		spec.Args = append([]string{"-n", "unshare", "--mount", "--propagation", "private", "--", "/bin/sh", "-c", officialMountScript, "ops-official", syncDir, "pacman"}, spec.Args[2:]...)
	}
	_, err = m.Runner.Run(ctx, spec)
	return err
}

const officialMountScript = `set -eu
mount --bind "$1" /var/lib/pacman/sync
mount -o remount,bind,ro /var/lib/pacman/sync
shift
exec "$@"`
