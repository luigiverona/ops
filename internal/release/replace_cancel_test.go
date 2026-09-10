package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type interruptedReplaceRunner struct {
	*replaceRunner
	cancel       context.CancelFunc
	phase        string
	failRecovery bool
}

func (r interruptedReplaceRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if err := ctx.Err(); err != nil {
		return run.Result{}, err
	}
	if s.Name == r.target && r.phase == "verify" {
		r.cancel()
		return run.Result{}, context.Canceled
	}
	if r.failRecovery && s.Name == "sudo" && len(s.Args) > 1 && (s.Args[1] == "mv" && strings.Contains(s.Args[len(s.Args)-2], ".ops-backup-") || s.Args[1] == "rm" && s.Args[len(s.Args)-1] == r.target) {
		return run.Result{}, errors.New("recovery denied")
	}
	result, err := r.replaceRunner.Run(ctx, s)
	if s.Name == "sudo" && len(s.Args) > 1 && s.Args[1] == "mv" && strings.Contains(s.Args[len(s.Args)-2], ".ops-new-") && r.phase == "rename" {
		r.cancel()
		return result, context.Canceled
	}
	return result, err
}

func TestInterruptedReplacementReportsActualRecovery(t *testing.T) {
	for _, phase := range []string{"rename", "verify"} {
		for _, failRecovery := range []bool{false, true} {
			t.Run(phase+"/"+map[bool]string{false: "restore", true: "retain"}[failRecovery], func(t *testing.T) {
				dir := t.TempDir()
				source, target := filepath.Join(dir, "verified"), filepath.Join(dir, "ops")
				if err := os.WriteFile(source, []byte("new"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				r := interruptedReplaceRunner{replaceRunner: &replaceRunner{version: "2.0.0", target: target}, cancel: cancel, phase: phase, failRecovery: failRecovery}
				err := Replace(ctx, r, source, target, "2.0.0")
				if err == nil {
					t.Fatal("interrupted replacement succeeded")
				}
				data, _ := os.ReadFile(target)
				backups, _ := filepath.Glob(target + ".ops-backup-*")
				if phase == "verify" && !failRecovery {
					if string(data) != "old" || len(backups) != 0 || !strings.Contains(err.Error(), "prior binary was restored") {
						t.Fatalf("data=%q backups=%v err=%v", data, backups, err)
					}
				} else {
					if string(data) != "new" || len(backups) != 1 || !strings.Contains(err.Error(), backups[0]) || strings.Contains(err.Error(), "was restored") {
						t.Fatalf("data=%q backups=%v err=%v", data, backups, err)
					}
					if old, _ := os.ReadFile(backups[0]); string(old) != "old" {
						t.Fatal("prior binary lost")
					}
				}
			})
		}
	}
}

func TestCancelledReplacementDoesNotCreateStaging(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Replace(ctx, nil, "unused", "unused", "2.0.0"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestInterruptedFirstInstallationDoesNotClaimRestoration(t *testing.T) {
	for _, failRecovery := range []bool{false, true} {
		dir := t.TempDir()
		source, target := filepath.Join(dir, "verified"), filepath.Join(dir, "ops")
		if err := os.WriteFile(source, []byte("new"), 0o755); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		r := interruptedReplaceRunner{replaceRunner: &replaceRunner{version: "2.0.0", target: target}, cancel: cancel, phase: "verify", failRecovery: failRecovery}
		err := Replace(ctx, r, source, target, "2.0.0")
		cancel()
		if err == nil || strings.Contains(err.Error(), "restored") {
			t.Fatalf("false restoration: %v", err)
		}
		_, statErr := os.Stat(target)
		if failRecovery {
			if statErr != nil || !strings.Contains(err.Error(), "removal of the new binary failed") {
				t.Fatalf("stat=%v err=%v", statErr, err)
			}
		} else if !os.IsNotExist(statErr) || !strings.Contains(err.Error(), "new binary was removed") {
			t.Fatalf("stat=%v err=%v", statErr, err)
		}
	}
}
