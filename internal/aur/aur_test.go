package aur

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
)

type fakeRunner struct{ spec run.Spec }

func (f *fakeRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.spec = spec
	return run.Result{}, nil
}
func TestInstallUsesParuAsNormalUserWithReview(t *testing.T) {
	f := &fakeRunner{}
	m := Manager{Runner: f}
	if err := m.Install(context.Background(), "example-bin"); err != nil {
		t.Fatal(err)
	}
	if f.spec.Name != "paru" || !f.spec.Interactive {
		t.Fatalf("unsafe AUR invocation: %#v", f.spec)
	}
	for _, arg := range f.spec.Args {
		if arg == "--skipreview" || arg == "--noconfirm" {
			t.Fatalf("review bypassed: %#v", f.spec.Args)
		}
	}
}

/*type artifactRunner struct {
	names map[string]string
	calls []run.Spec
}

func (f *artifactRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, spec)
	if spec.Name != "pacman" || len(spec.Args) != 3 || spec.Args[0] != "-Qpq" {
		return run.Result{}, errors.New("unexpected artifact inspection command")
	}
	name, ok := f.names[spec.Args[2]]
	if !ok {
		return run.Result{}, errors.New("unknown artifact")
	}
	return run.Result{Stdout: name + "\n"}, nil
}

func artifact(t *testing.T, dir, filename string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSelectArtifactsUsesExactInternalPackageIdentity(t *testing.T) {
	dir := t.TempDir()
	paru := artifact(t, dir, "paru-2.1.0-2-x86_64.pkg.tar.zst")
	debug := artifact(t, dir, "paru-debug-2.1.0-2-x86_64.pkg.tar.zst")
	misleading := artifact(t, dir, "paru-looks-right.pkg.tar.zst")
	runner := &artifactRunner{names: map[string]string{paru: "paru", debug: "paru-debug", misleading: "other-package"}}
	selected, err := selectArtifacts(context.Background(), runner, dir, strings.Join([]string{debug, misleading, paru}, "\n"), []string{"paru"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0] != paru {
		t.Fatalf("selected=%v", selected)
	}
}

func TestSelectArtifactsSupportsExactSplitPackageClosure(t *testing.T) {
	dir := t.TempDir()
	cli := artifact(t, dir, "suite-cli-1.2-3-x86_64.pkg.tar.zst")
	libs := artifact(t, dir, "suite-libs-1.2-3-x86_64.pkg.tar.zst")
	docs := artifact(t, dir, "suite-docs-1.2-3-any.pkg.tar.zst")
	runner := &artifactRunner{names: map[string]string{cli: "suite-cli", libs: "suite-libs", docs: "suite-docs"}}
	selected, err := selectArtifacts(context.Background(), runner, dir, strings.Join([]string{docs, libs, cli}, "\n"), []string{"suite-cli", "suite-libs"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(selected, "\n") != strings.Join([]string{cli, libs}, "\n") {
		t.Fatalf("selected=%v", selected)
	}
}

func TestSelectArtifactsFailsClosed(t *testing.T) {
	t.Run("zero match", func(t *testing.T) {
		dir := t.TempDir()
		debug := artifact(t, dir, "paru-debug.pkg.tar.zst")
		_, err := selectArtifacts(context.Background(), &artifactRunner{names: map[string]string{debug: "paru-debug"}}, dir, debug, []string{"paru"})
		if err == nil {
			t.Fatal("missing planned artifact was accepted")
		}
	})

	t.Run("duplicate internal match", func(t *testing.T) {
		dir := t.TempDir()
		first := artifact(t, dir, "first.pkg.tar.zst")
		second := artifact(t, dir, "second.pkg.tar.zst")
		_, err := selectArtifacts(context.Background(), &artifactRunner{names: map[string]string{first: "paru", second: "paru"}}, dir, first+"\n"+second, []string{"paru"})
		if err == nil {
			t.Fatal("duplicate planned artifacts were accepted")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := artifact(t, dir, "target.pkg.tar.zst")
		link := filepath.Join(dir, "link.pkg.tar.zst")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := selectArtifacts(context.Background(), &artifactRunner{}, dir, link, []string{"paru"}); err == nil {
			t.Fatal("symlink artifact was accepted")
		}
	})

	t.Run("non regular", func(t *testing.T) {
		dir := t.TempDir()
		candidate := filepath.Join(dir, "directory.pkg.tar.zst")
		if err := os.Mkdir(candidate, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := selectArtifacts(context.Background(), &artifactRunner{}, dir, candidate, []string{"paru"}); err == nil {
			t.Fatal("non-regular artifact was accepted")
		}
	})

	t.Run("outside build tree", func(t *testing.T) {
		dir := t.TempDir()
		outside := artifact(t, t.TempDir(), "paru.pkg.tar.zst")
		if _, err := selectArtifacts(context.Background(), &artifactRunner{}, dir, outside, []string{"paru"}); err == nil {
			t.Fatal("out-of-tree artifact was accepted")
		}
	})
}*/

type bootstrapRunner struct {
	commit   string
	srcinfo  string
	calls    []run.Spec
	artifact string
}

func (f *bootstrapRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, spec)
	if spec.Name == "git" {
		switch {
		case len(spec.Args) >= 2 && spec.Args[0] == "init":
			return run.Result{}, os.MkdirAll(spec.Args[len(spec.Args)-1], 0o700)
		case len(spec.Args) >= 3 && spec.Args[0] == "-C" && spec.Args[2] == "checkout":
			repo := spec.Args[1]
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

func TestBootstrapParuReviewDriftBuildAndInstallOrder(t *testing.T) {
	const commit = "0123456789012345678901234567890123456789"
	const srcinfo = "pkgbase = paru\n\tpkgver = 1\n\tpkgrel = 1\n\tmakedepends = cargo\n\npkgname = paru\n"

	t.Run("identical metadata continues", func(t *testing.T) {
		runner := &bootstrapRunner{commit: commit, srcinfo: srcinfo}
		var order []string
		manager := Manager{Runner: runner, Review: func(_ string, _ map[string]string) error {
			order = append(order, "review")
			return nil
		}}
		err := manager.BootstrapParu(context.Background(), plan.AURSource{Commit: commit, Metadata: paruMetadata(t, srcinfo)}, []string{"paru"}, func() error {
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
			if len(call.Args) == 0 {
				if !call.Interactive {
					t.Fatal("makepkg build lost normal-user interactive stream")
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
		err := manager.BootstrapParu(context.Background(), plan.AURSource{Commit: commit, Metadata: paruMetadata(t, srcinfo)}, []string{"paru"}, func() error {
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
		err := manager.BootstrapParu(context.Background(), plan.AURSource{Commit: commit, Metadata: paruMetadata(t, srcinfo)}, []string{"paru"}, func() error {
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
