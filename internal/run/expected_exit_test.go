package run_test

import (
	"context"
	"errors"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
	"os/exec"
	"testing"
)

func TestOnlyExit(t *testing.T) {
	actual := exec.Command("/bin/sh", "-c", "exit 3").Run()
	var exit *exec.ExitError
	if !errors.As(actual, &exit) || !run.OnlyExit(actual, 3) {
		t.Fatalf("exact ExitError: %v", actual)
	}
	expected := &run.Error{Name: "fixture", Err: testpkg.ExitStatus(3)}
	for _, tc := range testpkg.ExpectedExitCases(expected) {
		t.Run(tc.Name, func(t *testing.T) {
			if got := run.OnlyExit(tc.Err, 3); got != (tc.Cause == nil) {
				t.Fatalf("OnlyExit(%v)=%v", tc.Err, got)
			}
		})
	}
	for _, err := range []error{nil, testpkg.ExitStatus(4), errors.Join(expected, expected), errors.Join(expected), context.Canceled, context.DeadlineExceeded, &run.OwnershipError{Err: expected}} {
		if run.OnlyExit(err, 3) {
			t.Fatalf("certified inconclusive error: %v", err)
		}
	}
}
