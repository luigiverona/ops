package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/flatpak"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
	"github.com/luigiverona/ops/internal/ui"
)

func TestAURRepositoryDriftIsRejectedBeforeDependencyMutation(t *testing.T) {
	for _, drift := range []struct{ name, from, to string }{
		{"provider", "extra/rust\t", "core/rust\t"},
		{"additional member", "extra/llvm-libs\t", "core/llvm-libs\t"},
		{"custom provider", "extra/rust\t", "custom/rust\t"},
		{"custom additional member", "extra/llvm-libs\t", "custom/llvm-libs\t"},
	} {
		t.Run(drift.name, func(t *testing.T) {
			p := declaredParuPlan(t)
			var out bytes.Buffer
			base := &aurOrderRunner{output: &out}
			runner := diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				result, err := base.Run(ctx, s)
				if s.Name == "pacman" && s.Args[0] == "-Sp" {
					result.Stdout = strings.ReplaceAll(result.Stdout, drift.from, drift.to)
				}
				return result, err
			})
			code := (Runtime{Runner: runner, Out: &out, Err: &out}).executeForTest(context.Background(), p, ui.UI{In: strings.NewReader("y\n\ny\n"), Out: &out})
			if code != Issues {
				t.Fatalf("code=%d %s", code, &out)
			}
			for _, event := range base.events {
				if event == "dependencies" || event == "makepkg" || event == "artifact" {
					t.Fatalf("source drift reached mutation: %v", base.events)
				}
			}
		})
	}
}
func TestFlathubEnablementRequiresVisibleApprovalAndVerifies(t *testing.T) {
	for _, approve := range []bool{false, true} {
		for _, badPost := range []bool{false, true} {
			state := readyExecutionState()
			state.Flathub.Enabled = false
			declaration := config.Application{Source: config.Flatpak, Identifier: "org.example.App"}
			state.Flatpaks[declaration.Identifier] = "flathub"
			p := plan.Build(config.Config{Applications: []config.Application{declaration}}, state, nil)
			if !p.EnableFlathub || p.AddFlathub {
				t.Fatalf("%+v", p)
			}
			var out bytes.Buffer
			base := &prepareRunner{}
			enabled, mutations, reads := false, 0, 0
			runner := diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				if s.Name == "flatpak" && s.Args[0] == "remotes" {
					reads++
					value := testpkg.Flathub
					if !enabled {
						value = strings.Replace(value, `"options":""`, `"options":"disabled"`, 1)
					}
					return testpkg.FlatpakRemotes(value), nil
				}
				if s.Name == "flatpak" && s.Args[0] == "remote-modify" {
					if !strings.Contains(out.String(), "Continue?") {
						t.Fatal("mutation before approval")
					}
					mutations++
					enabled = !badPost
					return run.Result{}, nil
				}
				return base.Run(ctx, s)
			})
			answer := "n\n"
			if approve {
				answer = "y\n"
			}
			code := (Runtime{Runner: runner, Out: &out, Err: &out}).executeForTest(context.Background(), p, ui.UI{In: strings.NewReader(answer), Out: &out})
			disclosure := strings.Index(out.String(), "Enable existing user flathub remote at "+flatpak.FlathubRepositoryURL)
			if disclosure < 0 || disclosure > strings.Index(out.String(), "Continue?") {
				t.Fatalf("undisclosed correction: %s", &out)
			}
			if !approve {
				if code != Success || mutations != 0 || reads != 0 {
					t.Fatalf("decline mutated: %d %d %d", code, mutations, reads)
				}
			} else {
				want := Success
				if badPost {
					want = Fatal
				}
				if code != want || mutations != 1 || reads < 2 {
					t.Fatalf("unverified correction: %d %d %d %s", code, mutations, reads, &out)
				}
			}
		}
	}
}
func TestDoctorProvenanceMismatchesAreReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name, remote, apps string
		fatal              bool
	}{
		{"wrong origin", testpkg.Flathub, `[{"application_id":"org.example.App","origin":"other"}]`, false},
		{"wrong URL", strings.Replace(testpkg.Flathub, flatpak.FlathubRepositoryURL, "https://evil/", 1), testpkg.FlatpakApps("org.example.App"), false},
		{"disabled", strings.Replace(testpkg.Flathub, `"options":""`, `"options":"disabled"`, 1), testpkg.FlatpakApps("org.example.App"), false},
		{"missing", "[]", testpkg.FlatpakApps("org.example.App"), false},
		{"malformed remote", "[{}]", testpkg.FlatpakApps("org.example.App"), true},
		{"ambiguous app", testpkg.Flathub, testpkg.FlatpakApps("org.example.App", "org.example.App"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime, out, base := noActionPrepareRuntime(t, false)
			if err := os.WriteFile(config.Path(runtime.Home), []byte("version=2\nflatpak=[\"org.example.App\"]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			runtime.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				if s.Name == "sudo" || s.Interactive {
					t.Fatalf("doctor mutated: %+v", s)
				}
				if s.Name == "flatpak" {
					switch s.Args[0] {
					case "remotes":
						return testpkg.FlatpakRemotes(tc.remote), nil
					case "list":
						return run.Result{Stdout: tc.apps}, nil
					default:
						t.Fatalf("doctor mutated Flatpak: %+v", s)
					}
				}
				return base.Run(ctx, s)
			})
			code := runtime.Doctor(context.Background())
			want := Issues
			if tc.fatal {
				want = Fatal
			}
			if code != want || strings.Contains(out.String(), "Workstation healthy") {
				t.Fatalf("%d %s", code, out)
			}
		})
	}
}
func TestCoreCustomShadowCannotPassVerification(t *testing.T) {
	var out bytes.Buffer
	base := &prepareRunner{}
	runner := diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
		result, err := base.Run(ctx, s)
		if s.Name == "pacman" && s.Args[0] == "-Qi" && s.Args[len(s.Args)-1] == "git" {
			result.Stdout = strings.Replace(result.Stdout, "Arch fixture", "Custom packager", 1)
		}
		return result, err
	})
	p := plan.Plan{Core: readyCore(), ConfigureGit: true}
	code := (Runtime{Runner: runner, Out: &out, Err: &out}).executeForTest(context.Background(), p, ui.UI{In: strings.NewReader("y\n"), Out: &out})
	if code != Fatal || !strings.Contains(out.String(), "does not match authenticated official package contents") {
		t.Fatalf("%d %s", code, &out)
	}
	for _, s := range base.calls {
		if s.Name == "git" {
			t.Fatal("custom core package used for managed setup")
		}
	}
}

func TestShrinkingAURTransactionReverifiesOmittedRepositoryIdentity(t *testing.T) {
	planned := plan.OfficialDependency{Provider: "extra/rust", Packages: []string{"extra/rust", "extra/llvm-libs"}}
	current := plan.OfficialDependency{Provider: "extra/rust", Packages: []string{"extra/rust"}, Satisfied: true}
	for _, drift := range []bool{false, true} {
		reads := 0
		runner := diagnosticRunner(func(_ context.Context, s run.Spec) (run.Result, error) {
			reads++
			result, ok := testpkg.Query(s)
			if !ok || s.Name != "pacman" {
				t.Fatalf("unexpected mutation: %+v", s)
			}
			if drift && s.Args[0] == "-Si" {
				result.Stdout = strings.Replace(result.Stdout, "Repository : extra", "Repository : core", 1)
			}
			return result, nil
		})
		err := (Runtime{Runner: runner}).revalidateOfficialBinding(context.Background(), current, planned)
		if (err != nil) != drift || reads != 2 {
			t.Fatalf("drift=%v reads=%d err=%v", drift, reads, err)
		}
	}
}

func TestFinalReinspectionRejectsFlatpakProvenanceDrift(t *testing.T) {
	for _, drift := range []string{"origin", "URL", "disabled", "summary", "subset", "filter", "content", "keyring"} {
		t.Run(drift, func(t *testing.T) {
			a, out, base := noActionPrepareRuntime(t, false)
			declaration := config.Application{Source: config.Flatpak, Identifier: "org.example.App"}
			cfg := config.Config{Applications: []config.Application{declaration}}
			p := plan.Build(cfg, readyExecutionState(), plan.Facts{declaration: {State: plan.Install}})
			installed, final := false, false
			a.Runner = diagnosticRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
				if s.Name == "pacman" && s.Args[0] == "-Qq" {
					final = true
				}
				if s.Name == "flatpak" {
					switch s.Args[0] {
					case "install":
						installed = true
						return run.Result{}, nil
					case "list":
						value := ""
						if installed {
							value = testpkg.FlatpakApps(declaration.Identifier)
						}
						if final && drift == "origin" {
							value = strings.Replace(value, "flathub", "other", 1)
						}
						return run.Result{Stdout: value}, nil
					case "remotes":
						value := testpkg.Flathub
						if final && drift == "URL" {
							value = strings.Replace(value, flatpak.FlathubRepositoryURL, "https://other/", 1)
						}
						if final && drift == "disabled" {
							value = strings.Replace(value, `"options":""`, `"options":"disabled"`, 1)
						}
						result := testpkg.FlatpakRemotes(value)
						if final {
							data := string(testpkg.FlatpakConfig())
							switch drift {
							case "summary":
								data = strings.Replace(data, "gpg-verify-summary=true", "gpg-verify-summary=false", 1)
							case "subset":
								data += "xa.subset=verified\n"
							case "filter":
								data += "xa.filter=/missing/filter\n"
							case "content":
								data += "contenturl=https://other/\n"
							case "keyring":
								if err := os.WriteFile(filepath.Join(os.Getenv("FLATPAK_USER_DIR"), "repo/flathub.trustedkeys.gpg"), []byte("replaced"), 0600); err != nil {
									t.Fatal(err)
								}
							}
							if drift != "URL" && drift != "disabled" {
								testpkg.WriteFlatpakConfig([]byte(data))
							}
						}
						return result, nil
					}
				}
				return base.Run(ctx, s)
			})
			code := a.preparePlan(context.Background(), cfg, p, ui.UI{In: strings.NewReader("y\n"), Out: out})
			if code != Issues || !installed || !final || strings.Contains(out.String(), "Workstation ready.") {
				t.Fatalf("code=%d installed=%v final=%v: %s", code, installed, final, out)
			}
		})
	}
}

func TestOfficialRepairsAreDisclosedBeforeApproval(t *testing.T) {
	state := readyExecutionState()
	delete(state.OfficialMatches, "git")
	declaration := config.Application{Source: config.Pacman, Identifier: "firefox"}
	state.Installed["firefox"] = true
	p := resolveAndPlan(context.Background(), config.Config{Applications: []config.Application{declaration}}, state, outputResolver{pacman: map[string]plan.Package{"firefox": {Name: "firefox", Repository: "extra"}}})
	p.Applications = append(p.Applications, plan.Application{Declaration: config.Application{Source: config.AUR, Identifier: "example"}, State: plan.Install, AURPackages: []plan.BuildPackage{{Name: "compiler", Repository: "extra", Repair: true}}})
	var out bytes.Buffer
	runner := diagnosticRunner(func(context.Context, run.Spec) (run.Result, error) {
		t.Fatal("declined repair issued a command")
		return run.Result{}, nil
	})
	code := (Runtime{Runner: runner, Out: &out, Err: &out}).executeForTest(context.Background(), p, ui.UI{In: strings.NewReader("n\n"), Out: &out})
	approval := strings.Index(out.String(), "Continue?")
	for _, text := range []string{"Repair/reverify existing official prerequisite: git", "Repair/reverify existing official build dependency: extra/compiler", "installed package requires authenticated official content repair/reverification"} {
		if index := strings.Index(out.String(), text); index < 0 || index > approval {
			t.Fatalf("repair not disclosed before approval: %s", &out)
		}
	}
	if code != Success {
		t.Fatalf("declined repair failed: %d %s", code, &out)
	}
}
