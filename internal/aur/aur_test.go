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
	runner := &bootstrapRunner{}
	err := (Manager{Runner: runner}).Build(context.Background(), plan.AURSource{
		Commit:   "0123456789012345678901234567890123456789",
		Metadata: aurmeta.Metadata{PackageBase: "../escape", Packages: []aurmeta.Package{{Name: "example"}}},
	}, "example", []string{"example"}, func() error { return nil }, func(string, []string) error { return nil })
	if err == nil || len(runner.calls) != 0 {
		t.Fatalf("unsafe planned source reached filesystem work: err=%v calls=%#v", err, runner.calls)
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

type failingBuildRunner struct{ *bootstrapRunner }

func (r failingBuildRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if s.Name == "makepkg" {
		if len(s.Args) != 0 || s.FailureOutput != run.FailureCombined || s.Interactive || s.StreamOutput {
			return run.Result{}, errors.New("incorrect build evidence boundary")
		}
		// Exercise the real command boundary without running external build code.
		s.Name = "sh"
		s.Args = []string{"-c", "printf 'src/main.c:42: undefined reference to symbol\n'; printf '==> ERROR: A failure occurred in build().\n    Aborting...\n' >&2; exit 4"}
		s.Dir = ""
		return (run.Exec{}).Run(ctx, s)
	}
	return r.bootstrapRunner.Run(ctx, s)
}

func TestBuildFailureRetainsCompilerAndMakepkgEvidence(t *testing.T) {
	const commit = "0123456789012345678901234567890123456789"
	const metadata = "pkgbase = paru\npkgver = 1\npkgrel = 1\npkgname = paru\n"
	runner := failingBuildRunner{&bootstrapRunner{commit: commit, srcinfo: metadata}}
	installed := false
	manager := Manager{Runner: runner, Review: func(string, map[string]string) error { return nil }}
	err := manager.Build(context.Background(), plan.AURSource{Commit: commit, Metadata: paruMetadata(t, metadata)}, "paru", []string{"paru"}, func() error { return nil }, func(string, []string) error { installed = true; return nil })
	var failure *run.Error
	if installed || !errors.As(err, &failure) || !strings.Contains(failure.Evidence, "undefined reference") || !strings.Contains(failure.Evidence, "A failure occurred in build()") {
		t.Fatalf("installed=%v err=%v", installed, err)
	}
}
