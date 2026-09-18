package app

import (
	"context"
	"fmt"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(testpkg.IsolateFlatpakTests(m)) }

// Exercise the real native inventory behind Doctor while keeping every other
// workstation query in the existing isolated test fixture.
func TestDoctorNativeFlatpakPersistentStateReadOnly(t *testing.T) {
	for _, state := range []string{"canonical", "legacy", "subset", "summary", "malformed", "missing"} {
		t.Run(state, func(t *testing.T) {
			a, out, base := noActionPrepareRuntime(t, false)
			if err := os.WriteFile(config.Path(a.Home), []byte("version=2\nflatpak=[\"org.example.App\"]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			for k, sub := range map[string]string{"FLATPAK_USER_DIR": "user", "XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "XDG_CONFIG_HOME": "config"} {
				t.Setenv(k, filepath.Join(dir, sub))
			}
			if state != "missing" {
				for _, sub := range []string{"objects", "tmp", "refs/heads", "refs/remotes", "extensions", "state"} {
					if err := os.MkdirAll(filepath.Join(dir, "user/repo", sub), 0700); err != nil {
						t.Fatal(err)
					}
				}
				data := string(testpkg.FlatpakConfig())
				switch state {
				case "legacy":
					data = strings.Replace(data, "min-free-space-size=500MB\n", "", 1)
				case "subset":
					data += "xa.subset=verified\n"
				case "summary":
					data = strings.Replace(data, "gpg-verify-summary=true", "gpg-verify-summary=false", 1)
				case "malformed":
					data += "gpg-verify-summary=perhaps\n"
				}
				if err := os.WriteFile(filepath.Join(dir, "user/repo/config"), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "user/repo/flathub.trustedkeys.gpg"), testpkg.FlatpakKeyring(), 0600); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := func() string {
				var result strings.Builder
				err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					info, err := entry.Info()
					if err != nil {
						return err
					}
					fmt.Fprintf(&result, "%s %s %v\n", path, info.Mode(), info.ModTime())
					if info.Mode().IsRegular() {
						data, err := os.ReadFile(path)
						if err != nil {
							return err
						}
						fmt.Fprintf(&result, "%x\n", data)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				return result.String()
			}
			before := snapshot()
			queries := 0
			a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				if s.Name == "flatpak" {
					if !s.ReadOnlyFilesystem || (s.Args[0] != "remotes" && s.Args[0] != "list") {
						t.Fatalf("unsafe Doctor query: %+v", s)
					}
					queries++
					return (run.Exec{}).Run(ctx, s)
				}
				if s.Name == "sudo" || s.Interactive {
					t.Fatalf("Doctor mutation: %+v", s)
				}
				return base.Run(ctx, s)
			})
			code := a.Doctor(context.Background())
			if queries == 0 || code == Success || strings.Contains(out.String(), "Workstation healthy") {
				t.Fatalf("code=%d queries=%d %s", code, queries, out)
			}
			if snapshot() != before {
				t.Fatal("Doctor changed persistent Flatpak state")
			}
		})
	}
}
