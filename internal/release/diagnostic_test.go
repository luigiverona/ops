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

type failedRecoveryRunner struct {
	*replaceRunner
	failure error
}

func (r failedRecoveryRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if s.Name == "sudo" && s.Args[1] == "mv" && strings.Contains(s.Args[len(s.Args)-2], ".ops-backup-") {
		return run.Result{}, r.failure
	}
	return r.replaceRunner.Run(ctx, s)
}

func TestReplacementRecoveryFailurePreservesDiagnostic(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "verified"), filepath.Join(dir, "ops")
	for path, data := range map[string]string{source: "new", target: "old"} {
		if err := os.WriteFile(path, []byte(data), 0755); err != nil {
			t.Fatal(err)
		}
	}
	failure := &run.Error{Name: "sudo", Err: errors.New("exit status 1"), Evidence: "mv: cannot restore backup: read-only file system"}
	runner := failedRecoveryRunner{&replaceRunner{version: "2.0.0", target: target, failFinal: true}, failure}
	err := Replace(context.Background(), runner, source, target, "2.0.0")
	var command *run.Error
	if !errors.Is(err, failure) || !errors.As(err, &command) || command.Evidence != failure.Evidence {
		t.Fatalf("recovery diagnostic lost: %v", err)
	}
	backups, _ := filepath.Glob(target + ".ops-backup-*")
	if len(backups) != 1 || !strings.Contains(err.Error(), backups[0]) {
		t.Fatalf("backup not preserved: %v %v", backups, err)
	}
	old, err := os.ReadFile(backups[0])
	if err != nil || string(old) != "old" {
		t.Fatalf("backup=%q err=%v", old, err)
	}
}
