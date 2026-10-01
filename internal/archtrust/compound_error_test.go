package archtrust

import (
	"context"
	"errors"
	"fmt"
	"github.com/luigiverona/ops/internal/run"
	"testing"
)

type queryExit int

func (e queryExit) Error() string { return "query exit" }
func (e queryExit) ExitCode() int { return int(e) }

type queryFailure struct{ err error }

func (r queryFailure) Run(context.Context, run.Spec) (run.Result, error) { return run.Result{}, r.err }
func TestSnapshotMissCannotBypassExecutionFailure(t *testing.T) {
	exit := queryExit(1)
	for _, cause := range []error{nil, &run.OwnershipError{Err: errors.New("cleanup")}, context.Canceled, context.DeadlineExceeded, errors.New("capture")} {
		for _, reverse := range []bool{false, true} {
			s := NewSource()
			s.database = map[string][]byte{}
			s.packages = map[string]Package{}
			var failure error = exit
			if cause != nil {
				if reverse {
					failure = errors.Join(cause, exit)
				} else {
					failure = fmt.Errorf("nested: %w", errors.Join(exit, cause))
				}
			}
			_, err := s.Query(context.Background(), queryFailure{failure}, []string{"-Si", "--", "absent"})
			var source *SourceError
			if errors.As(err, &source) != (cause != nil) || !errors.Is(err, failure) {
				t.Fatalf("query failure classification: %v", err)
			}
		}
	}
}
