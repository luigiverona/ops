package archrepo

import (
	"context"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

type queryFunc func(run.Spec) (run.Result, error)

func (f queryFunc) Run(_ context.Context, s run.Spec) (run.Result, error) { return f(s) }
func TestInstalledMatchIsMetadataNotNativeStatus(t *testing.T) {
	for _, tc := range []struct {
		name, inventory, local string
		match                  bool
		invalid                bool
	}{
		{"official", "extra firefox 1-1\n", testpkg.Info("firefox"), true, false},
		{"custom native", "custom firefox 1-1 [installed]\n", testpkg.Info("firefox"), false, false},
		{"custom shadows official", "custom firefox 1-1 [installed]\nextra firefox 1-1 [installed]\n", strings.Replace(testpkg.Info("firefox"), "Arch fixture", "Custom packager", 1), false, false},
		{"same version different build", "extra firefox 1-1 [installed]\n", strings.Replace(testpkg.Info("firefox"), "2026", "2025", 1), false, false},
		{"missing metadata", "extra firefox 1-1\n", "Name : firefox\n", false, true},
		{"duplicate metadata", "extra firefox 1-1\n", testpkg.Info("firefox") + "Version : 1-1\n", false, true},
		{"duplicate inventory", "extra firefox 1-1\nextra firefox 1-1\n", testpkg.Info("firefox"), false, true},
		{"ambiguous official", "extra firefox 1-1\ncore firefox 1-1\n", testpkg.Info("firefox"), false, true},
		{"malformed inventory", "extra firefox\n", testpkg.Info("firefox"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := queryFunc(func(s run.Spec) (run.Result, error) {
				switch s.Args[0] {
				case "-Sl":
					return run.Result{Stdout: tc.inventory}, nil
				case "-Qi":
					return run.Result{Stdout: tc.local}, nil
				case "-Si":
					if s.Args[len(s.Args)-1] != "extra/firefox" {
						t.Fatalf("unqualified lookup: %v", s.Args)
					}
					return run.Result{Stdout: testpkg.Info("firefox")}, nil
				}
				t.Fatalf("unexpected query: %v", s)
				return run.Result{}, nil
			})
			matches, err := InstalledMatches(context.Background(), runner, []string{"firefox"})
			if (err != nil) != tc.invalid || (matches["firefox"] == "extra/firefox") != tc.match {
				t.Fatalf("matches=%v err=%v", matches, err)
			}
		})
	}
}
func TestTransactionIdentityGrammar(t *testing.T) {
	for _, output := range []string{"custom/firefox\n", "extra/firefox\ncustom/lib\n", "extra/firefox\ncore/firefox\n", "extra/firefox\nextra/firefox\n", "firefox\n", "unknown/firefox\n", "extra/\n", "extra/firefox\n\n", "extra/firefox\tdata\n", "extra/firefox\r\n"} {
		if _, err := ParseTransaction(output); err == nil {
			t.Errorf("accepted %q", output)
		}
	}
	got, err := ParseTransaction("multilib/steam\ncore/glibc\nextra/gtk3\n")
	if err != nil || strings.Join(got, ",") != "core/glibc,extra/gtk3,multilib/steam" {
		t.Fatalf("%v %v", got, err)
	}
}
func TestInfoRejectsDuplicateEmptyFields(t *testing.T) {
	if _, err := ParseInfo("Name : firefox\nRepository :\nRepository : extra\n"); err == nil {
		t.Fatal("duplicate empty field accepted")
	}
}
