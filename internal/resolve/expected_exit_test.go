package resolve

import (
	"context"
	"errors"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
	"io"
	"net/http"
	"strings"
	"testing"
)

type expectedPacmanRunner struct {
	dependencyRunner
	operation string
	failure   error
	hit       bool
	later     int
}

func (r *expectedPacmanRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if r.hit {
		r.later++
	}
	if s.Name == "pacman" && s.Args[0] == r.operation {
		r.hit = true
		output := ""
		if r.operation == "-T" {
			output = "cargo\n"
		}
		return run.Result{Stdout: output}, r.failure
	}
	return r.dependencyRunner.Run(ctx, s)
}
func (r *expectedPacmanRunner) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	return r.Run(ctx, run.Spec{Name: "pacman", Args: args})
}
func TestDependencyExpectedExitsPreserveFailures(t *testing.T) {
	for _, op := range []struct {
		name string
		code int
	}{{"-T", 127}, {"-Qq", 1}} {
		for _, tc := range testpkg.ExpectedExitCases(&run.Error{Name: "pacman", Err: testpkg.ExitStatus(op.code)}) {
			t.Run(op.name+"/"+tc.Name, func(t *testing.T) {
				r := &expectedPacmanRunner{dependencyRunner: dependencyRunner{transaction: "extra/rust\tcargo\n"}, operation: op.name, failure: tc.Err}
				binding, err := (Resolver{Runner: r}).OfficialDependency(context.Background(), "cargo")
				if !r.hit {
					t.Fatal("injection not reached")
				}
				if tc.Cause == nil {
					if err != nil || binding.Satisfied || binding.Provider != "extra/rust" {
						t.Fatalf("missing dependency changed: %+v %v", binding, err)
					}
				} else if !errors.Is(err, tc.Cause) || !errors.Is(err, tc.Err) || r.later != 0 || run.OwnershipFailed(err) != run.OwnershipFailed(tc.Cause) {
					t.Fatalf("lost failure or continued: %v later=%d", err, r.later)
				}
			})
		}
	}
}

func TestPacmanFallbackPreservesCompoundFailures(t *testing.T) {
	for _, tc := range testpkg.ExpectedExitCases(&run.Error{Name: "pacman", Err: testpkg.ExitStatus(1)}) {
		t.Run(tc.Name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":2,"valid":true,"count":0,"page":1,"num_pages":1,"results":[]}`))}, nil
			})}
			_, found, err := (Resolver{Runner: diagnosticPacmanRunner{tc.Err}, Client: client}).Pacman(context.Background(), "absent")
			if found {
				t.Fatal("unexpected package")
			}
			if tc.Cause == nil {
				if err != nil || calls != 1 {
					t.Fatalf("expected miss: %v calls=%d", err, calls)
				}
			} else if !errors.Is(err, tc.Cause) || calls != 0 {
				t.Fatalf("lost failure: %v calls=%d", err, calls)
			}
		})
	}
}
