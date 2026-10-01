package release

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestReleaseFixtureIndependentExpiration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "testdata/ownership_supervisor.py", "expiration")
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "child independent SIGALRM expiration PASS") || !strings.Contains(string(out), "grandchild independent SIGALRM expiration PASS") || !strings.Contains(string(out), "RELEASE CLEANUP PASS") {
		t.Fatalf("release expiration: %v\n%s", err, out)
	}
	t.Log(string(out))
}
