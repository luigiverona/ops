package arch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/luigiverona/ops/internal/run"
)

// Manager performs privileged Arch mutations through sudo only.
type Manager struct {
	Runner     run.Runner
	PacmanConf string
}

// EnableMultilib atomically replaces pacman.conf after local and pacman-conf validation.
func (m Manager) EnableMultilib(ctx context.Context) error {
	path := m.PacmanConf
	if path == "" {
		path = "/etc/pacman.conf"
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("pacman.conf is not a regular file")
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	updated, err := EnableMultilib(original)
	if err != nil {
		return err
	}
	if err := ValidatePacmanConf(updated); err != nil {
		return err
	}
	local, err := os.CreateTemp("", "ops-pacman.conf-*")
	if err != nil {
		return err
	}
	localPath := local.Name()
	defer os.Remove(localPath)
	if err := local.Chmod(0o600); err != nil {
		_ = local.Close()
		return err
	}
	if _, err := local.Write(updated); err != nil {
		_ = local.Close()
		return err
	}
	if err := local.Close(); err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(current) != sha256.Sum256(original) {
		return errors.New("pacman.conf changed during planning; rerun ops")
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	remoteTemp := filepath.Join(filepath.Dir(path), ".pacman.conf.ops-"+hex.EncodeToString(random))
	defer func() {
		_, _ = m.Runner.Run(context.Background(), run.Spec{Name: "sudo", Args: []string{"-n", "rm", "-f", "--", remoteTemp}})
	}()
	if _, err := m.Runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"-n", "install", "-m", "0644", "-o", "root", "-g", "root", "--", localPath, remoteTemp}}); err != nil {
		return fmt.Errorf("stage pacman.conf: %w", err)
	}
	if _, err := m.Runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"-n", "pacman-conf", "--config", remoteTemp}}); err != nil {
		return fmt.Errorf("validate pacman.conf with pacman-conf: %w", err)
	}
	if _, err := m.Runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"-n", "mv", "--", remoteTemp, path}}); err != nil {
		return fmt.Errorf("replace pacman.conf: %w", err)
	}
	verified, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	enabled, err := MultilibEnabled(verified)
	if err != nil || !enabled {
		return errors.New("multilib postcondition verification failed")
	}
	return nil
}

func (m Manager) FullUpgrade(ctx context.Context) error {
	_, err := m.Runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"-n", "pacman", "-Syu"}, Interactive: true})
	return err
}

func (m Manager) Install(ctx context.Context, packages []string, asDeps bool) error {
	if len(packages) == 0 {
		return nil
	}
	args := []string{"-n", "pacman", "-S", "--needed", "--noconfirm"}
	if asDeps {
		args = append(args, "--asdeps")
	}
	args = append(args, "--")
	args = append(args, packages...)
	_, err := m.Runner.Run(ctx, run.Spec{Name: "sudo", Args: args})
	return err
}
