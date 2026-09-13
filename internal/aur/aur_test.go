package aur

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
)

type bootstrapRunner struct {
	commit   string
	srcinfo  string
	calls    []run.Spec
	artifact string
	repo     string
	files    map[string]string
}

func (f *bootstrapRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, spec)
	if spec.Name == "git" {
		switch {
		case len(spec.Args) >= 2 && spec.Args[0] == "init":
			return run.Result{}, os.MkdirAll(spec.Args[len(spec.Args)-1], 0o700)
		case len(spec.Args) >= 3 && spec.Args[0] == "-C" && spec.Args[2] == "checkout":
			repo := spec.Args[1]
			f.repo = repo
			if f.files != nil {
				for name, contents := range f.files {
					if err := os.WriteFile(filepath.Join(repo, name), []byte(contents), 0o600); err != nil {
						return run.Result{}, err
					}
				}
				return run.Result{}, nil
			}
			if err := os.WriteFile(filepath.Join(repo, ".SRCINFO"), []byte(f.srcinfo), 0o600); err != nil {
				return run.Result{}, err
			}
			if err := os.WriteFile(filepath.Join(repo, "PKGBUILD"), []byte("pkgname=paru\n"), 0o600); err != nil {
				return run.Result{}, err
			}
			return run.Result{}, nil
		case len(spec.Args) >= 3 && spec.Args[0] == "-C" && spec.Args[2] == "rev-parse":
			return run.Result{Stdout: f.commit + "\n"}, nil
		case len(spec.Args) >= 3 && spec.Args[0] == "-C" && spec.Args[2] == "ls-files":
			if f.files != nil {
				var names []string
				for name := range f.files {
					names = append(names, name)
				}
				sort.Strings(names)
				return run.Result{Stdout: strings.Join(names, "\x00") + "\x00"}, nil
			}
			return run.Result{Stdout: ".SRCINFO\x00PKGBUILD\x00"}, nil
		default:
			return run.Result{}, nil
		}
	}
	if spec.Name == "makepkg" && len(spec.Args) == 0 {
		f.artifact = filepath.Join(spec.Dir, "paru-1-1-x86_64.pkg.tar.zst")
		return run.Result{}, os.WriteFile(f.artifact, []byte("fixture"), 0o600)
	}
	if spec.Name == "makepkg" && strings.Join(spec.Args, " ") == "--packagelist" {
		return run.Result{Stdout: f.artifact + "\n"}, nil
	}
	return run.Result{}, errors.New("unexpected command")
}

func paruMetadata(t *testing.T, srcinfo string) aurmeta.Metadata {
	t.Helper()
	metadata, err := aurmeta.Parse([]byte(srcinfo))
	if err != nil {
		t.Fatal(err)
	}
	return metadata
}

func TestBuildRejectsUnsafePlannedSourceIdentityBeforeFilesystemUse(t *testing.T) {
	tempParent := t.TempDir()
	t.Setenv("TMPDIR", tempParent)
	for _, name := range []string{"../escape", ".", ".."} {
		for _, field := range []string{"base", "target", "output"} {
			t.Run(field+"/"+name, func(t *testing.T) {
				runner := &bootstrapRunner{}
				source := plan.AURSource{
					Commit:   "0123456789012345678901234567890123456789",
					Metadata: aurmeta.Metadata{PackageBase: "example", Packages: []aurmeta.Package{{Name: "example"}}},
				}
				target, output := "example", "example"
				switch field {
				case "base":
					source.Metadata.PackageBase = name
				case "target":
					target = name
				case "output":
					output = name
				}
				reviewed, mutated := false, false
				manager := Manager{Runner: runner, Review: func(string, map[string]string) error { reviewed = true; return nil }}
				err := manager.Build(context.Background(), source, target, []string{output}, func() error { mutated = true; return nil }, func(string, []string) error { mutated = true; return nil })
				if err == nil || !strings.Contains(err.Error(), "invalid planned AUR") || len(runner.calls) != 0 || reviewed || mutated {
					t.Fatalf("unsafe planned identity reached build work: err=%v calls=%#v reviewed=%v mutated=%v", err, runner.calls, reviewed, mutated)
				}
			})
		}
	}
	if entries, err := os.ReadDir(tempParent); err != nil || len(entries) != 0 {
		t.Fatalf("invalid source left filesystem changes: entries=%v err=%v", entries, err)
	}
}

func TestBuildUsesFixedCheckoutBelowTemporaryRoot(t *testing.T) {
	const commit = "0123456789012345678901234567890123456789"
	tempParent := t.TempDir()
	t.Setenv("TMPDIR", tempParent)
	for _, base := range []string{"paru", "suite-base", "..pkg", "..."} {
		t.Run(base, func(t *testing.T) {
			srcinfo := "pkgbase = " + base + "\npkgver = 1\npkgrel = 1\npkgname = paru\n"
			runner := &bootstrapRunner{commit: commit, srcinfo: srcinfo}
			var repo, root string
			reviewed, installed := false, false
			manager := Manager{Runner: runner, Review: func(name string, files map[string]string) error {
				reviewed = true
				repo = runner.repo
				root = filepath.Dir(repo)
				if filepath.Dir(root) != tempParent || !strings.HasPrefix(filepath.Base(root), "ops-aur-") {
					t.Fatalf("checkout is outside fresh temporary root: %q", repo)
				}
				if relative, err := filepath.Rel(root, repo); err != nil || relative != "checkout" {
					t.Fatalf("checkout is not the fixed child: relative=%q err=%v", relative, err)
				}
				data, err := os.ReadFile(filepath.Join(repo, ".SRCINFO"))
				if err != nil || string(data) != srcinfo || files[".SRCINFO"] != srcinfo || name != "paru" {
					t.Fatalf("review did not use checked-out metadata: files=%v err=%v", files, err)
				}
				return nil
			}}
			err := manager.Build(context.Background(), plan.AURSource{Commit: commit, Metadata: paruMetadata(t, srcinfo)}, "paru", []string{"paru"}, func() error { return nil }, func(buildDir string, artifacts []string) error {
				installed = true
				if buildDir != repo || len(artifacts) != 1 || filepath.Dir(artifacts[0]) != repo {
					t.Fatalf("artifact handoff left checkout: dir=%q artifacts=%v", buildDir, artifacts)
				}
				return nil
			})
			if err != nil || !reviewed || !installed {
				t.Fatalf("build did not complete: err=%v reviewed=%v installed=%v", err, reviewed, installed)
			}
			operations := make(map[string]int)
			for _, call := range runner.calls {
				var path, operation string
				switch call.Name {
				case "git":
					if call.Args[0] == "init" {
						path, operation = call.Args[len(call.Args)-1], "init"
					} else if call.Args[0] == "-C" {
						path, operation = call.Args[1], call.Args[2]
					} else {
						t.Fatalf("git command lacks checkout path: %+v", call)
					}
					if operation == "fetch" && call.Args[len(call.Args)-2] != "https://aur.archlinux.org/"+base+".git" {
						t.Fatalf("package base was not preserved in source URL: %+v", call)
					}
				case "makepkg":
					path, operation = call.Dir, "makepkg"
				default:
					t.Fatalf("unexpected build command: %+v", call)
				}
				if path != repo || path == root || path == filepath.Dir(root) {
					t.Fatalf("command escaped checkout: %+v", call)
				}
				operations[operation]++
			}
			for _, operation := range []string{"init", "fetch", "checkout", "ls-files", "rev-parse", "makepkg"} {
				if operations[operation] == 0 {
					t.Errorf("missing operation %q", operation)
				}
			}
			if operations["makepkg"] != 2 {
				t.Fatalf("build and packagelist were not both checked: %v", operations)
			}
			if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary root was not cleaned: %v", err)
			}
		})
	}
}

func TestReviewProvenanceMustMatchBeforeApprovalAndBeforeMutation(t *testing.T) {
	const commit = "0123456789012345678901234567890123456789"
	const srcinfo = "pkgbase = paru\npkgver = 1\npkgrel = 1\npkgname = paru\n"
	for _, phase := range []string{"before review", "during review"} {
		t.Run(phase, func(t *testing.T) {
			runner := &bootstrapRunner{commit: commit, srcinfo: srcinfo}
			if phase == "before review" {
				runner.srcinfo = strings.Replace(srcinfo, "pkgbase = paru", "pkgbase = other", 1)
			}
			reviewed, mutated := false, false
			manager := Manager{Runner: runner, Review: func(string, map[string]string) error {
				reviewed = true
				runner.commit = strings.Repeat("f", 40)
				return nil
			}}
			err := manager.Build(context.Background(), plan.AURSource{Commit: commit, Metadata: paruMetadata(t, srcinfo)}, "paru", []string{"paru"}, func() error { mutated = true; return nil }, func(string, []string) error { mutated = true; return nil })
			if err == nil || mutated || reviewed != (phase == "during review") {
				t.Fatalf("reviewed=%v mutated=%v err=%v", reviewed, mutated, err)
			}
		})
	}
}

func TestPinnedBuildReviewDriftBuildAndInstallOrder(t *testing.T) {
	const commit = "0123456789012345678901234567890123456789"
	const srcinfo = "pkgbase = paru\n\tpkgver = 1\n\tpkgrel = 1\n\tmakedepends = cargo\n\npkgname = paru\n"

	t.Run("identical metadata continues", func(t *testing.T) {
		runner := &bootstrapRunner{commit: commit, srcinfo: srcinfo}
		var order []string
		manager := Manager{Runner: runner, Review: func(_ string, _ map[string]string) error {
			order = append(order, "review")
			return nil
		}}
		err := manager.Build(context.Background(), plan.AURSource{Commit: commit, Metadata: paruMetadata(t, srcinfo)}, "paru", []string{"paru"}, func() error {
			order = append(order, "dependencies")
			return nil
		}, func(buildDir string, artifacts []string) error {
			order = append(order, "install")
			if buildDir == "" || len(artifacts) != 1 || artifacts[0] != runner.artifact {
				t.Fatalf("buildDir=%q artifacts=%v", buildDir, artifacts)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(order, ",") != "review,dependencies,install" {
			t.Fatalf("order=%v", order)
		}
		for _, call := range runner.calls {
			if call.Name != "makepkg" {
				continue
			}
			if call.Stdin == nil {
				t.Fatalf("makepkg inherited runner stdin: %#v", call)
			}
			input, err := io.ReadAll(call.Stdin)
			if err != nil || len(input) != 0 {
				t.Fatalf("makepkg stdin=%q err=%v", input, err)
			}
			if len(call.Args) == 0 {
				if call.Interactive {
					t.Fatal("makepkg build leaked a normal-user interactive stream")
				}
				continue
			}
			if strings.Join(call.Args, " ") != "--packagelist" {
				t.Fatalf("makepkg received dependency/install flags: %#v", call.Args)
			}
		}
	})

	t.Run("declined review mutates nothing", func(t *testing.T) {
		runner := &bootstrapRunner{commit: commit, srcinfo: srcinfo}
		mutated := false
		manager := Manager{Runner: runner, Review: func(string, map[string]string) error { return errors.New("declined") }}
		err := manager.Build(context.Background(), plan.AURSource{Commit: commit, Metadata: paruMetadata(t, srcinfo)}, "paru", []string{"paru"}, func() error {
			mutated = true
			return nil
		}, func(string, []string) error {
			mutated = true
			return nil
		})
		if err == nil || mutated {
			t.Fatalf("err=%v mutated=%v", err, mutated)
		}
		for _, call := range runner.calls {
			if call.Name == "makepkg" {
				t.Fatalf("build ran after declined review: %#v", call)
			}
		}
	})

	t.Run("metadata drift stops before mutation", func(t *testing.T) {
		runner := &bootstrapRunner{commit: commit, srcinfo: strings.Replace(srcinfo, "cargo", "go", 1)}
		mutated := false
		manager := Manager{Runner: runner, Review: func(string, map[string]string) error { return nil }}
		err := manager.Build(context.Background(), plan.AURSource{Commit: commit, Metadata: paruMetadata(t, srcinfo)}, "paru", []string{"paru"}, func() error {
			mutated = true
			return nil
		}, func(string, []string) error {
			mutated = true
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "metadata changed") || mutated {
			t.Fatalf("err=%v mutated=%v", err, mutated)
		}
	})

	t.Run("signing key drift stops before mutation", func(t *testing.T) {
		planned := strings.Replace(srcinfo, "makedepends = cargo", "makedepends = cargo\n\tvalidpgpkeys = 0123456789ABCDEF0123456789ABCDEF01234567", 1)
		changed := strings.Replace(planned, "0123456789ABCDEF0123456789ABCDEF01234567", "FEDCBA9876543210FEDCBA9876543210FEDCBA98", 1)
		runner := &bootstrapRunner{commit: commit, srcinfo: changed}
		mutated := false
		manager := Manager{Runner: runner, Review: func(string, map[string]string) error { return nil }}
		err := manager.Build(context.Background(), plan.AURSource{Commit: commit, Metadata: paruMetadata(t, planned)}, "paru", []string{"paru"}, func() error {
			mutated = true
			return nil
		}, func(string, []string) error {
			mutated = true
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "metadata changed") || mutated {
			t.Fatalf("err=%v mutated=%v", err, mutated)
		}
	})
}

func TestMetadataAndAuxiliaryFilesRemainAuthoritativeAfterReview(t *testing.T) {
	const commit = "0123456789012345678901234567890123456789"
	const metadata = "pkgbase = paru\n\tpkgver = 1\n\tpkgrel = 1\npkgname = paru\n"
	for _, mode := range []string{"valid", "missing metadata", "malformed metadata", "changed metadata", "auxiliary drift"} {
		t.Run(mode, func(t *testing.T) {
			files := map[string]string{"PKGBUILD": "source helper.sh\n", ".SRCINFO": metadata, "helper.sh": "echo \x1b\n"}
			switch mode {
			case "missing metadata":
				delete(files, ".SRCINFO")
			case "malformed metadata":
				files[".SRCINFO"] = "not metadata"
			case "changed metadata":
				files[".SRCINFO"] = strings.Replace(metadata, "pkgver = 1", "pkgver = 2", 1)
			}
			runner := &bootstrapRunner{commit: commit, files: files}
			reviewed, dependencies, installed := false, false, false
			manager := Manager{Runner: runner, Review: func(_ string, presented map[string]string) error {
				reviewed = true
				if !strings.Contains(presented["helper.sh"], "\\x1b") || strings.Contains(presented["helper.sh"], "\x1b") {
					t.Fatal("unsafe auxiliary review")
				}
				// Presentation may omit metadata without changing authoritative input.
				delete(presented, ".SRCINFO")
				return nil
			}}
			err := manager.Build(context.Background(), plan.AURSource{Commit: commit, Metadata: paruMetadata(t, metadata)}, "paru", []string{"paru"}, func() error {
				dependencies = true
				if mode == "auxiliary drift" {
					// These bytes sanitize to the same display as the original:
					// equality must compare raw tracked bytes, not rendered text.
					return os.WriteFile(filepath.Join(runner.repo, "helper.sh"), []byte("echo \\x1b\n"), 0o600)
				}
				return nil
			}, func(string, []string) error { installed = true; return nil })
			if mode == "valid" {
				if err != nil || !reviewed || !dependencies || !installed {
					t.Fatalf("err=%v reviewed=%v dependencies=%v installed=%v", err, reviewed, dependencies, installed)
				}
				return
			}
			if err == nil || installed {
				t.Fatalf("invalid source accepted: %v", err)
			}
			if mode == "missing metadata" && reviewed {
				t.Fatal("missing required metadata reached review")
			}
			if mode != "auxiliary drift" && dependencies {
				t.Fatal("invalid metadata authorized dependencies")
			}
			for _, call := range runner.calls {
				if call.Name == "makepkg" {
					t.Fatal("invalid source reached build")
				}
			}
		})
	}
}

type failingBuildRunner struct {
	*bootstrapRunner
	packageList bool
}

func (r failingBuildRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if s.Name == "makepkg" && (len(s.Args) > 0) == r.packageList {
		// A bare secret has no marker that heuristic filtering can recognize.
		s.Name = "sh"
		s.Args = []string{"-c", "printf 'bare-private-value'; printf 'other-private-value' >&2; exit 4"}
		s.Dir = ""
		return (run.Exec{}).Run(ctx, s)
	}
	return r.bootstrapRunner.Run(ctx, s)
}

func TestBuildCodeOutputDoesNotOptIntoDiagnosticReplay(t *testing.T) {
	const commit = "0123456789012345678901234567890123456789"
	const metadata = "pkgbase = paru\npkgver = 1\npkgrel = 1\npkgname = paru\n"
	for _, packageList := range []bool{false, true} {
		runner := failingBuildRunner{bootstrapRunner: &bootstrapRunner{commit: commit, srcinfo: metadata}, packageList: packageList}
		installed := false
		manager := Manager{Runner: runner, Review: func(string, map[string]string) error { return nil }}
		err := manager.Build(context.Background(), plan.AURSource{Commit: commit, Metadata: paruMetadata(t, metadata)}, "paru", []string{"paru"}, func() error { return nil }, func(string, []string) error { installed = true; return nil })
		var failure *run.Error
		if installed || !errors.As(err, &failure) || !run.Exited(err, 4) || failure.Evidence != "" || failure.Presented || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("packageList=%v installed=%v err=%v", packageList, installed, err)
		}
	}
}
