package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/release"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
)

func TestUpdateVerificationCauseAbortsBeforeSudo(t *testing.T) {
	for _, stage := range []string{"signature", "version"} {
		for _, cause := range []error{errors.New("private ordinary failure"), &run.OwnershipError{Err: errors.New("private cleanup failure")}, context.Canceled, context.DeadlineExceeded, &run.Error{Name: "private executable", Args: []string{"secret argument"}, Stderr: "secret stderr", Err: errors.New("private execution detail")}} {
			t.Run(stage+"/"+cause.Error(), func(t *testing.T) {
				owner := &gateOwner{}
				base := &ownedUpdateRunner{owner: owner}
				failed, later := false, 0
				runner := diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
					if failed {
						later++
						t.Fatalf("command after verification failure: %+v", s)
					}
					if s.Name == "sudo" {
						t.Fatal("sudo after incomplete verification")
					}
					args := strings.Join(s.Args, " ")
					if stage == "signature" && strings.Contains(args, "--verify") {
						failed = true
						return run.Result{Stdout: "[GNUPG:] BADSIG private-user\n"}, cause
					}
					if stage == "version" && args == "--version" {
						failed = true
						return run.Result{}, cause
					}
					return base.Run(ctx, s)
				})
				binary := []byte("fixture; never executed")
				manifest := fmt.Sprintf("%x  %s\n", sha256.Sum256(binary), release.BinaryName)
				client := release.Client{Trust: release.DefaultTrust(), HTTP: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					body := "signature fixture"
					switch filepath.Base(req.URL.Path) {
					case release.BinaryName:
						body = string(binary)
					case release.ChecksumsName:
						body = manifest
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}}
				var out strings.Builder
				a := Runtime{Ownership: owner, Runner: runner, Out: &out, Err: &out}
				code := a.installUpdate(context.Background(), client, "9.0.0", ui.UI{In: strings.NewReader("y\n"), Out: &out})
				if code != Fatal || !failed || later != 0 || base.sudo || base.replaced {
					t.Fatalf("code=%d later=%d output=%s", code, later, &out)
				}
				for _, private := range []string{"private", "secret"} {
					if strings.Contains(out.String(), private) {
						t.Fatal("private execution text exposed", out.String())
					}
				}
			})
		}
	}
}
