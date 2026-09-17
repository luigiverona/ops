package testpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/run"
)

// PacmanFixture runs real libalpm queries against synthetic databases. It never
// invokes sudo, refreshes a database, downloads, or runs a package transaction.
type PacmanFixture struct {
	Dir      string
	Conf     string
	official map[string]FixturePackage
	history  map[string]FixturePackage
}

type FixturePackage struct {
	Name, Version, Packager, Payload, Depends, Provides string
}

func NewPacmanFixture(t *testing.T) *PacmanFixture {
	t.Helper()
	if _, err := exec.LookPath("pacman"); err != nil {
		t.Skip("pacman unavailable")
	}
	f := &PacmanFixture{Dir: t.TempDir(), official: map[string]FixturePackage{}, history: map[string]FixturePackage{}}
	f.Conf = filepath.Join(f.Dir, "pacman.conf")
	f.Write(t, "db/local/ALPM_DB_VERSION", []byte("9\n"))
	f.Configure(t, "custom", "core", "extra")
	return f
}

func (f *PacmanFixture) Write(t *testing.T, path string, data []byte) {
	t.Helper()
	path = filepath.Join(f.Dir, path)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func (f *PacmanFixture) Configure(t *testing.T, repos ...string) {
	t.Helper()
	text := "[options]\nArchitecture = x86_64\nSigLevel = Never\n"
	for _, repo := range repos {
		text += "[" + repo + "]\nServer = file://" + filepath.Join(f.Dir, "packages", repo) + "\n"
	}
	f.Write(t, "pacman.conf", []byte(text))
}

func fixtureArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (p FixturePackage) desc() string {
	text := "%NAME%\n" + p.Name + "\n\n%VERSION%\n" + p.Version + "\n\n%BASE%\n" + p.Name + "\n\n%DESC%\nfixture\n\n%ARCH%\nany\n\n%BUILDDATE%\n1700000000\n\n%PACKAGER%\n" + p.Packager + "\n\n%ISIZE%\n1\n\n"
	if p.Depends != "" {
		text += "%DEPENDS%\n" + p.Depends + "\n\n"
	}
	if p.Provides != "" {
		text += "%PROVIDES%\n" + p.Provides + "\n\n"
	}
	return text
}

func (f *PacmanFixture) Sync(t *testing.T, repo string, packages ...FixturePackage) map[string]string {
	t.Helper()
	for target := range f.official {
		if strings.HasPrefix(target, repo+"/") {
			delete(f.official, target)
		}
	}
	files, digests := map[string]string{}, map[string]string{}
	for _, p := range packages {
		if repo == "core" || repo == "extra" || repo == "multilib" {
			f.official[repo+"/"+p.Name] = p
			f.history[repo+"/"+p.Name+"@"+p.Version] = p
		}
		pkginfo := fmt.Sprintf("pkgname = %s\npkgver = %s\npkgdesc = fixture\nbuilddate = 1700000000\npackager = %s\nsize = 1\narch = any\n", p.Name, p.Version, p.Packager)
		archive := fixtureArchive(t, map[string]string{".PKGINFO": pkginfo, "usr/share/" + p.Name: p.Payload})
		filename := p.Name + "-" + p.Version + "-any.pkg.tar.gz"
		f.Write(t, filepath.Join("packages", repo, filename), archive)
		digest := fmt.Sprintf("%x", sha256.Sum256(archive))
		digests[p.Name] = digest
		files[p.Name+"-"+p.Version+"/desc"] = p.desc() + "%FILENAME%\n" + filename + "\n\n%SHA256SUM%\n" + digest + "\n\n%CSIZE%\n" + fmt.Sprint(len(archive)) + "\n\n"
	}
	f.Write(t, "db/sync/"+repo+".db", fixtureArchive(t, files))
	return digests
}

func (f *PacmanFixture) Local(t *testing.T, p FixturePackage) {
	t.Helper()
	dir := "db/local/" + p.Name + "-" + p.Version
	f.Write(t, dir+"/desc", []byte(strings.Replace(p.desc(), "%ISIZE%", "%SIZE%", 1)+"%REASON%\n0\n\n%VALIDATION%\nsha256\n\n"))
	f.Write(t, dir+"/files", []byte("%FILES%\nusr/\nusr/share/\nusr/share/"+p.Name+"\n\n"))
	f.Write(t, "usr/share/"+p.Name, []byte(p.Payload))
}

func (f *PacmanFixture) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if s.Name == "vercmp" {
		return (run.Exec{}).Run(ctx, s)
	}
	if s.Name != "pacman" || len(s.Args) == 0 || !(strings.HasPrefix(s.Args[0], "-Q") || s.Args[0] == "-Si" || s.Args[0] == "-Sl" || s.Args[0] == "-Sp" || s.Args[0] == "-Sup" || s.Args[0] == "-T") {
		return run.Result{}, fmt.Errorf("fixture rejects non-query command: %s %v", s.Name, s.Args)
	}
	s.Args = append([]string{"--config", f.Conf, "--root", f.Dir, "--dbpath", filepath.Join(f.Dir, "db"), "--logfile", filepath.Join(f.Dir, "pacman.log")}, s.Args...)
	return (run.Exec{}).Run(ctx, s)
}

// OfficialQuery runs native libalpm over the explicitly designated synthetic
// official fixtures, excluding the separately configured custom repository.
func (f *PacmanFixture) OfficialQuery(ctx context.Context, args []string) (run.Result, error) {
	conf := "[options]\nArchitecture = x86_64\nSigLevel = Never\n"
	for _, repo := range []string{"core", "extra", "multilib"} {
		if _, err := os.Stat(filepath.Join(f.Dir, "db/sync", repo+".db")); err == nil {
			conf += "[" + repo + "]\nServer = file://" + filepath.Join(f.Dir, "packages", repo) + "\n"
		}
	}
	name := filepath.Join(f.Dir, "official.conf")
	if err := os.WriteFile(name, []byte(conf), 0600); err != nil {
		return run.Result{}, err
	}
	native := append([]string{"--config", name, "--root", f.Dir, "--dbpath", filepath.Join(f.Dir, "db")}, args...)
	return (run.Exec{}).Run(ctx, run.Spec{Name: "pacman", Args: native})
}
func (f *PacmanFixture) OfficialInstalled(_ context.Context, target string) (bool, error) {
	p, ok := f.official[target]
	if !ok {
		return false, fmt.Errorf("missing official fixture")
	}
	data, err := os.ReadFile(filepath.Join(f.Dir, "usr/share", p.Name))
	if err != nil {
		return false, err
	}
	return string(data) == p.Payload, nil
}

func (f *PacmanFixture) OfficialInstalledVersion(_ context.Context, target, version string) (bool, error) {
	p, ok := f.history[target+"@"+version]
	if !ok {
		return false, archtrust.ErrExactVersionUnavailable
	}
	data, err := os.ReadFile(filepath.Join(f.Dir, "usr/share", p.Name))
	if err != nil {
		return false, err
	}
	return string(data) == p.Payload, nil
}

// ForgetEvidence simulates losing independent exact-version evidence, not content.
func (f *PacmanFixture) ForgetEvidence(target, version string) { delete(f.history, target+"@"+version) }
