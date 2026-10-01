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

type causeRunner func(context.Context, run.Spec) (run.Result, error)

func (r causeRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) { return r(ctx, s) }

type verificationExit int

func (e verificationExit) Error() string { return "ordinary exit failure" }
func (e verificationExit) ExitCode() int { return int(e) }

func TestReleaseVerificationRetainsExecutionCauses(t *testing.T) {
	causes := []error{verificationExit(7), &run.OwnershipError{Err: errors.New("private ownership detail")}, context.Canceled, context.DeadlineExceeded, &run.Error{Name: "/private/staged/path", Args: []string{"private argument"}, Stderr: "private stderr", Err: verificationExit(9)}}
	trust, artifacts, _ := signedArtifacts(t, "1.2.3")
	server := artifactServer(artifacts)
	defer server.Close()
	for _, stage := range []string{"signature", "downloaded-version", "staged-version", "installed-version"} {
		for _, cause := range causes {
			t.Run(stage+"/"+cause.Error(), func(t *testing.T) {
				failed, later := false, 0
				dir := t.TempDir()
				target := filepath.Join(dir, "ops")
				var workspace string
				runner := causeRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
					if failed {
						later++
						if s.Name != "sudo" || s.Args[1] != "rm" {
							t.Fatalf("verification continued with %+v", s)
						}
						return run.Result{}, nil
					}
					args := strings.Join(s.Args, " ")
					if s.Name == "gpg" {
						if strings.Contains(args, "--show-keys") {
							workspace = filepath.Dir(s.Args[len(s.Args)-1])
							return run.Result{Stdout: "sub:-::::::::::s:\nfpr:::::::::" + trust.Fingerprint + ":\n"}, nil
						}
						if strings.Contains(args, "--verify") {
							if stage == "signature" {
								failed = true
								return run.Result{Stdout: "[GNUPG:] BADSIG private-user-details\n"}, cause
							}
							return run.Result{Stdout: "[GNUPG:] VALIDSIG " + trust.Fingerprint + "\n"}, nil
						}
						return run.Result{}, nil
					}
					if args == "--version" {
						if stage != "installed-version" || s.Name == target {
							failed = true
							return run.Result{Stdout: "private output"}, cause
						}
						return run.Result{Stdout: "ops 1.2.3\n"}, nil
					}
					if s.Name == "sudo" {
						return run.Result{}, nil
					}
					t.Fatalf("unexpected command %+v", s)
					return run.Result{}, nil
				})
				var err error
				var expected string
				if stage == "signature" || stage == "downloaded-version" {
					verified, e := (Client{Runner: runner, Trust: trust, BaseURL: server.URL}).DownloadVerified(context.Background(), "1.2.3")
					err = e
					if verified != nil {
						verified.Close()
						t.Fatal("verification succeeded")
					}
					expected = "verified release binary reports an unexpected version"
					if stage == "signature" {
						expected = "release signature verification failed: GnuPG reported BADSIG"
					}
					if later != 0 {
						t.Fatal("download verification continued")
					}
					if _, e := os.Stat(workspace); !errors.Is(e, os.ErrNotExist) {
						t.Fatal("verification workspace remains", workspace, e)
					}
				} else {
					err = Replace(context.Background(), runner, "unused-verified", target, "1.2.3")
					expected = "staged update version verification failed"
					if stage == "installed-version" {
						expected = "installed update verification failed; new binary was removed"
					}
				}
				if !failed || !errors.Is(err, cause) || err.Error() != expected {
					t.Fatalf("failed=%v error=%v want=%q", failed, err, expected)
				}
				var own *run.OwnershipError
				if errors.As(cause, &own) && !run.OwnershipFailed(err) {
					t.Fatal("ownership lost")
				}
				var command *run.Error
				if errors.As(cause, &command) {
					var got *run.Error
					if !errors.As(err, &got) || got != command {
						t.Fatal("structured error lost")
					}
				}
				if code, ok := cause.(verificationExit); ok && !run.Exited(err, int(code)) {
					t.Fatal("ordinary exit lost")
				}
			})
		}
	}
}
