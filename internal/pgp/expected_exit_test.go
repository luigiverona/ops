package pgp

import (
	"context"
	"errors"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type pgpExit = testpkg.ExitStatus

type queryFailureRunner struct {
	keyRunner
	target  string
	failure error
	hit     bool
	later   int
}

func (r *queryFailureRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if r.hit {
		r.later++
	}
	if strings.Contains(strings.Join(s.Args, " "), r.target) {
		r.hit = true
		return run.Result{}, r.failure
	}
	return r.keyRunner.Run(ctx, s)
}
func TestPublicGPGQueriesPreserveCompoundFailures(t *testing.T) {
	missing := &run.Error{Name: "gpg", Stderr: "gpg: error reading key: No public key", Err: pgpExit(2)}
	for _, path := range []struct {
		name, target    string
		keyboxd, absent bool
	}{
		{"classic-list", "--list-keys", false, true},
		{"configuration", "--gpgconf-list", false, false},
		{"keyboxd-export", "--export-options", true, false},
		{"keyboxd-import", "--import", true, false},
		{"keyboxd-list", "--list-keys", true, false},
	} {
		for _, tc := range testpkg.ExpectedExitCases(missing) {
			t.Run(path.name+"/"+tc.Name, func(t *testing.T) {
				home := t.TempDir()
				if err := os.Chmod(home, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, "pubring.kbx"), []byte("public fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				r := &queryFailureRunner{target: path.target, failure: tc.Err}
				if path.keyboxd {
					r.configOutput = "use_keyboxd:16:1:\n"
					if err := os.Mkdir(filepath.Join(home, "public-keys.d"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				present, err := (Manager{Home: home, Runner: r}).Has(context.Background(), testFingerprint)
				if !r.hit || present || r.later != 0 {
					t.Fatalf("query continued/was missed: present=%v hit=%v later=%d", present, r.hit, r.later)
				}
				if tc.Cause == nil && path.absent {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, tc.Err) || tc.Cause != nil && !errors.Is(err, tc.Cause) {
					t.Fatalf("lost query cause: %v", err)
				}
			})
		}
	}
	// Import shares Has and also queries downloaded public material before mutation.
	for _, stage := range []string{"existing", "downloaded"} {
		for _, tc := range testpkg.ExpectedExitCases(missing) {
			t.Run("Import/"+stage+"/"+tc.Name, func(t *testing.T) {
				home := t.TempDir()
				if err := os.Chmod(home, 0700); err != nil {
					t.Fatal(err)
				}
				if stage == "existing" {
					if err := os.WriteFile(filepath.Join(home, "pubring.kbx"), []byte("public"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				r := &queryFailureRunner{target: "--list-keys", failure: tc.Err}
				err := (Manager{Home: home, Runner: r}).Import(context.Background(), testFingerprint)
				if !r.hit || err == nil {
					t.Fatalf("missing key accepted for import: %v", err)
				}
				if tc.Cause != nil && (!errors.Is(err, tc.Cause) || r.later != 0) {
					t.Fatalf("lost import cause: %v later=%d", err, r.later)
				}
				for _, s := range r.calls {
					if strings.Contains(strings.Join(s.Args, " "), "--import") {
						t.Fatal("mutation after inconclusive query")
					}
				}
			})
		}
	}
}
