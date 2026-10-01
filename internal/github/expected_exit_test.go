package github

import (
	"context"
	"errors"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
	"testing"
)

func TestPublicExpectedStatesPreserveCompoundFailures(t *testing.T) {
	for _, method := range []string{"Configured", "Authenticated", "VerifySSH", "Scope", "Login", "Refresh"} {
		expected := &run.Error{Name: "gh", Args: []string{"api", "--paginate", "user/keys"}, Stderr: `This API operation needs the "admin:public_key" scope`, Err: configExit(1)}
		for _, tc := range testpkg.ExpectedExitCases(expected) {
			t.Run(method+"/"+tc.Name, func(t *testing.T) {
				f := &fakeRunner{fn: func(s run.Spec) (run.Result, error) {
					if len(s.Args) > 1 && (s.Args[1] == "login" || s.Args[1] == "refresh") {
						return run.Result{}, nil
					}
					stderr := `could not find key "user"`
					if method == "VerifySSH" {
						stderr = "successfully authenticated"
					}
					return run.Result{Stderr: stderr}, tc.Err
				}}
				m := Manager{Runner: f}
				var err error
				switch method {
				case "Configured":
					_, err = m.Configured(context.Background())
				case "Authenticated":
					_, err = m.Authenticated(context.Background())
				case "VerifySSH":
					err = m.VerifySSH(context.Background())
				case "Login":
					err = m.Login(context.Background())
				case "Refresh":
					err = m.RefreshSSHKeyScope(context.Background())
				case "Scope":
					if got := IsSSHKeyScopeError(tc.Err); got != (tc.Cause == nil) {
						t.Fatalf("scope=%v error=%v", got, tc.Err)
					}
					return
				}
				if tc.Cause == nil {
					if (method == "Login" || method == "Refresh") && err == nil {
						t.Fatal("unverified auth accepted")
					}
					if method != "Login" && method != "Refresh" && err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, tc.Cause) {
					t.Fatalf("lost cause: %v", err)
				}
			})
		}
	}
}
