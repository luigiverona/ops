package testpkg

import (
	"context"
	"errors"
	"fmt"

	"github.com/luigiverona/ops/internal/run"
)

// ExitStatus is a deterministic command-exit fixture, without a subprocess.
type ExitStatus int

func (e ExitStatus) Error() string { return fmt.Sprintf("exit status %d", e) }
func (e ExitStatus) ExitCode() int { return int(e) }

type ExitCase struct {
	Name  string
	Err   error
	Cause error
}

// ExpectedExitCases includes wrapping and both join orders; Cause is nil only
// for the ordinary product state. Callers must retain every nonnil Cause.
func ExpectedExitCases(expected error) []ExitCase {
	ownership := &run.OwnershipError{Err: errors.New("fixture cleanup failed")}
	arbitrary := errors.New("fixture execution failed")
	return []ExitCase{
		{"expected", expected, nil},
		{"wrapped-expected", fmt.Errorf("command: %w", expected), nil},
		{"ownership", errors.Join(expected, ownership), ownership},
		{"reverse", errors.Join(ownership, expected), ownership},
		{"nested", fmt.Errorf("outer: %w", errors.Join(fmt.Errorf("inner: %w", expected), errors.Join(ownership))), ownership},
		{"cancel", errors.Join(expected, context.Canceled), context.Canceled},
		{"deadline", errors.Join(context.DeadlineExceeded, expected), context.DeadlineExceeded},
		{"arbitrary", errors.Join(expected, arbitrary), arbitrary},
	}
}
