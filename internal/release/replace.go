package release

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/luigiverona/ops/internal/run"
)

// Replace atomically installs a pre-verified binary and restores the prior target on postcondition failure.
func Replace(ctx context.Context, runner run.Runner, verified, target, version string) error {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	suffix := hex.EncodeToString(random)
	staged, backup := target+".ops-new-"+suffix, target+".ops-backup-"+suffix
	defer func() {
		_, _ = runner.Run(context.Background(), run.Spec{Name: "sudo", Args: []string{"-n", "rm", "-f", "--", staged, backup}})
	}()
	if _, err := runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"install", "-m", "0755", "-o", "root", "-g", "root", "--", verified, staged}}); err != nil {
		return err
	}
	result, err := runner.Run(ctx, run.Spec{Name: staged, Args: []string{"--version"}})
	if err != nil || strings.TrimSpace(result.Stdout) != "ops "+version {
		return errors.New("staged update version verification failed")
	}
	hadTarget := false
	if _, err := os.Lstat(target); err == nil {
		hadTarget = true
		if _, err := runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"cp", "--preserve=mode,ownership,timestamps", "--", target, backup}}); err != nil {
			return fmt.Errorf("backup installed binary: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"mv", "--", staged, target}}); err != nil {
		return err
	}
	result, err = runner.Run(ctx, run.Spec{Name: target, Args: []string{"--version"}})
	if err == nil && strings.TrimSpace(result.Stdout) == "ops "+version {
		return nil
	}
	if hadTarget {
		if _, restoreErr := runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"mv", "--", backup, target}}); restoreErr != nil {
			return fmt.Errorf("update verification failed and prior binary restoration failed: %v", restoreErr)
		}
	} else {
		_, _ = runner.Run(ctx, run.Spec{Name: "sudo", Args: []string{"rm", "-f", "--", target}})
	}
	return errors.New("installed update verification failed; prior binary was restored")
}
