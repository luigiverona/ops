package arch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/run"
)

// runOfficial keeps configured custom repositories out of package mutations,
// including implicit dependencies and the full system upgrade. When filtering
// is needed, the expanded config is streamed into existing protected staging;
// pacman never consumes a user-writable configuration path as root.
func (m Manager) runOfficial(ctx context.Context, spec run.Spec) (returnErr error) {
	result, err := m.Runner.Run(ctx, run.Spec{Name: "pacman-conf", FailureOutput: run.FailureStderr})
	if err != nil {
		return fmt.Errorf("inspect package repository configuration: %w", err)
	}
	configuration, custom, err := archrepo.OfficialConfig(result.Stdout)
	if err != nil {
		return err
	}
	if !custom {
		_, err = m.Runner.Run(ctx, spec)
		return err
	}
	if err := m.validateStageParent(ctx); err != nil {
		return err
	}
	result, err = m.Runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"-n", "mktemp", "--directory", "--tmpdir=" + artifactStageParent, "ops-paru-XXXXXXXXXXXX"}, FailureOutput: run.FailureStderr})
	if err != nil {
		return err
	}
	dir, err := validatedStageDir(result.Stdout)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "pacman.conf")
	defer func() { returnErr = errors.Join(returnErr, m.cleanupArtifactStage(dir, []string{path})) }()
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
	_, err = m.Runner.Run(ctx, spec)
	return err
}
