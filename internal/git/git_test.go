package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type fakeRunner struct {
	name, email string
	writes      int
}

func (f *fakeRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	last := spec.Args[len(spec.Args)-1]
	if len(spec.Args) >= 4 && spec.Args[2] != "--get" {
		if spec.Args[2] == "user.name" {
			f.name = last
		} else if spec.Args[2] == "user.email" {
			f.email = last
		}
		f.writes++
		return run.Result{}, nil
	}
	if last == "user.name" {
		if f.name == "" {
			return run.Result{}, &run.Error{Name: "git", Err: testExit(1)}
		}
		return run.Result{Stdout: f.name + "\n"}, nil
	}
	if last == "user.email" {
		if f.email == "" {
			return run.Result{}, &run.Error{Name: "git", Err: testExit(1)}
		}
		return run.Result{Stdout: f.email + "\n"}, nil
	}
	return run.Result{}, errors.New("unexpected")
}
func TestSetOnlyMissingOrInvalidValues(t *testing.T) {
	f := &fakeRunner{name: "Existing", email: "invalid"}
	m := Manager{Runner: f}
	current, err := m.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetMissing(context.Background(), current, "Replacement", "valid@example.com"); err != nil {
		t.Fatal(err)
	}
	if f.name != "Existing" || f.email != "valid@example.com" || f.writes != 1 {
		t.Fatalf("name=%q email=%q writes=%d", f.name, f.email, f.writes)
	}
}
func TestIdentityValidation(t *testing.T) {
	if !ValidName("User") || ValidName("\n") || !ValidEmail("a@b.example") || ValidEmail("not-an-email") {
		t.Fatal("validation mismatch")
	}
}

type testExit int

func (e testExit) Error() string { return fmt.Sprintf("exit status %d", e) }
func (e testExit) ExitCode() int { return int(e) }

type runnerFunc func(context.Context, run.Spec) (run.Result, error)

func (f runnerFunc) Run(ctx context.Context, spec run.Spec) (run.Result, error) {
	return f(ctx, spec)
}

func TestInspectOptionalIdentity(t *testing.T) {
	for _, want := range []Identity{
		{Email: "user@example.invalid"}, {Name: "Test User"}, {},
		{Name: "Test User", Email: "user@example.invalid"},
		{Name: "invalid\nname", Email: "invalid"},
	} {
		t.Run(fmt.Sprintf("%q", want), func(t *testing.T) {
			got, err := (Manager{Runner: &fakeRunner{name: want.Name, email: want.Email}}).Inspect(context.Background())
			if err != nil || got != want {
				t.Fatalf("identity=%#v err=%v", got, err)
			}
		})
	}
}

func TestInspectRejectsInconclusiveReads(t *testing.T) {
	for _, field := range []string{"user.name", "user.email"} {
		for _, test := range []struct {
			name   string
			result run.Result
			err    error
		}{
			{"runner failure", run.Result{}, errors.New("runner unavailable")},
			{"execution failure", run.Result{}, exec.ErrNotFound},
			{"cancelled", run.Result{}, context.Canceled},
			{"deadline", run.Result{}, context.DeadlineExceeded},
			{"config failure", run.Result{}, &run.Error{Name: "git", Err: testExit(128)}},
			{"read diagnostic", run.Result{Stderr: "permission denied\n"}, &run.Error{Name: "git", Err: testExit(1)}},
			{"error diagnostic", run.Result{}, &run.Error{Name: "git", Stderr: "permission denied", Err: testExit(1)}},
			{"retained evidence", run.Result{}, &run.Error{Name: "git", Evidence: "\n", Err: testExit(1)}},
			{"truncated evidence", run.Result{}, &run.Error{Name: "git", EvidenceTruncated: true, Err: testExit(1)}},
			{"nested diagnostic", run.Result{}, &run.Error{Name: "git", Err: &run.Error{Stderr: "permission denied", Err: testExit(1)}}},
			{"joined runner failure", run.Result{}, errors.Join(testExit(1), errors.New("runner unavailable"))},
			{"wrapped joined capture failure", run.Result{}, fmt.Errorf("runner: %w", &run.Error{Name: "git", Err: errors.Join(testExit(1), errors.New("incomplete capture"))})},
			{"joined cancellation", run.Result{}, errors.Join(testExit(1), context.Canceled)},
			{"joined deadline", run.Result{}, &run.Error{Name: "git", Err: errors.Join(testExit(1), context.DeadlineExceeded)}},
			{"partial value", run.Result{Stdout: "value\n"}, testExit(1)},
			{"whitespace value", run.Result{Stdout: "\n"}, testExit(1)},
			{"whitespace diagnostic", run.Result{Stderr: "\n"}, testExit(1)},
		} {
			t.Run(field+"/"+test.name, func(t *testing.T) {
				runner := runnerFunc(func(_ context.Context, spec run.Spec) (run.Result, error) {
					if spec.Args[len(spec.Args)-1] == field {
						return test.result, test.err
					}
					return run.Result{Stdout: "Test User\n"}, nil
				})
				got, err := (Manager{Runner: runner}).Inspect(context.Background())
				if !errors.Is(err, test.err) || !strings.Contains(err.Error(), field) || got != (Identity{}) {
					t.Fatalf("identity=%#v err=%v", got, err)
				}
			})
		}
	}
}

func TestInspectWrappedMissingValue(t *testing.T) {
	runner := runnerFunc(func(context.Context, run.Spec) (run.Result, error) {
		return run.Result{}, fmt.Errorf("runner: %w", &run.Error{Name: "git", Err: testExit(1)})
	})
	got, err := (Manager{Runner: runner}).Inspect(context.Background())
	if err != nil || got != (Identity{}) {
		t.Fatalf("identity=%#v err=%v", got, err)
	}
}

func TestInspectRejectsSuccessfulReadWithDiagnostics(t *testing.T) {
	for _, field := range []string{"user.name", "user.email"} {
		for _, diagnostic := range []string{"permission denied\n", " \n"} {
			t.Run(field+"/"+diagnostic, func(t *testing.T) {
				runner := runnerFunc(func(_ context.Context, spec run.Spec) (run.Result, error) {
					if spec.Args[3] == field {
						return run.Result{Stdout: "fallback\n", Stderr: diagnostic}, nil
					}
					return run.Result{Stdout: "Existing\n"}, nil
				})
				got, err := (Manager{Runner: runner}).Inspect(context.Background())
				if err == nil || got != (Identity{}) || !strings.Contains(err.Error(), field) {
					t.Fatalf("identity=%#v err=%v", got, err)
				}
			})
		}
	}
}

func TestInspectCancellationIsNotAbsence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := runnerFunc(func(context.Context, run.Spec) (run.Result, error) {
		cancel()
		return run.Result{}, testExit(1)
	})
	if _, err := (Manager{Runner: runner}).Inspect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSetMissingPreservesVerificationReadFailure(t *testing.T) {
	for _, field := range []string{"user.name", "user.email"} {
		t.Run(field, func(t *testing.T) {
			base := &fakeRunner{}
			cause := errors.New("verification read unavailable")
			runner := runnerFunc(func(ctx context.Context, spec run.Spec) (run.Result, error) {
				if spec.Args[2] == "--get" && spec.Args[3] == field {
					return run.Result{}, cause
				}
				return base.Run(ctx, spec)
			})
			err := (Manager{Runner: runner}).SetMissing(context.Background(), Identity{}, "Test User", "user@example.invalid")
			if !errors.Is(err, cause) || !strings.Contains(err.Error(), "verify Git identity: inspect Git "+field) || base.writes != 2 {
				t.Fatalf("writes=%d err=%v", base.writes, err)
			}
		})
	}
}

func TestInspectIsolatedGitConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	home := t.TempDir()
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	path := filepath.Join(home, ".gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", path)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, test := range []struct {
		name, content       string
		want                Identity
		unreadable, wantErr bool
		xdgFallback         bool
	}{
		{name: "both absent"},
		{name: "name absent", content: "[user]\n email = user@example.invalid\n", want: Identity{Email: "user@example.invalid"}},
		{name: "email absent", content: "[user]\n name = Test User\n", want: Identity{Name: "Test User"}},
		{name: "both present", content: "[user]\n name = Test User\n email = user@example.invalid\n", want: Identity{Name: "Test User", Email: "user@example.invalid"}},
		{name: "invalid value", content: "[user]\n email = invalid\n", want: Identity{Email: "invalid"}},
		{name: "malformed", content: "[user\n", wantErr: true},
		{name: "unreadable", content: "[user]\n name = Test User\n", unreadable: true, wantErr: true},
		{name: "unreadable with XDG fallback", content: "[user]\n name = Preferred User\n email = preferred@example.invalid\n", unreadable: true, xdgFallback: true, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.unreadable && os.Geteuid() == 0 {
				t.Skip("root can read mode-000 files")
			}
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if test.xdgFallback {
				t.Setenv("GIT_CONFIG_GLOBAL", "")
				if err := os.Unsetenv("GIT_CONFIG_GLOBAL"); err != nil {
					t.Fatal(err)
				}
				xdg := filepath.Join(home, "xdg", "git")
				if err := os.MkdirAll(xdg, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(xdg, "config"), []byte("[user]\n name = Fallback User\n email = fallback@example.invalid\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.unreadable {
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(path, 0o600); err != nil {
						t.Error(err)
					}
				})
			}
			got, err := (Manager{Runner: run.Exec{}}).Inspect(context.Background())
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("identity=%#v err=%v", got, err)
			}
		})
	}
}
