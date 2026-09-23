//go:build ownership_integration

package run

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNativeMakepkgHelpers(t *testing.T) {
	o := nativeOwner(t)
	for _, mode := range []string{"before-build", "build", "packagelist", "unexpected-helper"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			home := filepath.Join(dir, "gpg")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			write := func(name, value string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write("gpg/common.conf", "use-keyboxd\n")
			write("makepkg.conf", "CARCH=x86_64\nCHOST=x86_64-pc-linux-gnu\nBUILDENV=(!distcc !color !ccache !check !sign)\nOPTIONS=(!strip !debug !lto !zipman !purge)\nPKGEXT='.pkg.tar'\nSRCEXT='.src.tar'\n")
			// Exercise keyboxd without signing or importing any private key. Sourcing
			// PKGBUILD happens for --packagelist too. Record helpers inside the owned
			// command before makepkg exits; no external helper can satisfy the assertion.
			build := `pkgname=ops-ownership-fixture
pkgver=1
pkgrel=1
arch=('any')
gpg --batch --list-keys >/dev/null
python3 "$OPS_RECORD_SCRIPT" "$OPS_RECORD_DIR"
package() { mkdir -p "$pkgdir/usr/share/ops-fixture"; printf fixture > "$pkgdir/usr/share/ops-fixture/data"; }
`
			if mode == "unexpected-helper" {
				write("unexpected.py", `import os,pathlib,signal,time
signal.alarm(10)
os.register_at_fork(after_in_child=lambda: signal.alarm(10))
r,w=os.pipe()
p=os.fork()
if p:
 os.close(w)
 assert os.read(r,1)==b'1'
 os._exit(0)
pathlib.Path(os.environ['OPS_RECORD_DIR']+'/unexpected.pid').write_text(str(os.getpid()))
os.write(w,b'1')
os.close(1)
os.close(2)
time.sleep(8)
`)
				build = strings.Replace(build, "gpg --batch --list-keys >/dev/null", `python3 "$OPS_RECORD_DIR/unexpected.py"`, 1)
			}
			write("PKGBUILD", build)
			recordScript := `import json,os,pathlib,sys
root=pathlib.Path(sys.argv[1])
cg=next(x[3:] for x in pathlib.Path('/proc/self/cgroup').read_text().splitlines() if x.startswith('0::'))
for pid in (pathlib.Path('/sys/fs/cgroup'+cg)/'cgroup.procs').read_text().split():
 try:
  role=pathlib.Path('/proc/'+pid+'/comm').read_text().strip()
  (root/(pid+'.json')).write_text(json.dumps(dict(pid=int(pid),role=role,cgroup='0::'+cg+'\n')))
 except FileNotFoundError: pass
`
			write("record.py", recordScript)
			spec := Spec{Name: "makepkg", Dir: dir, Args: []string{"--config", filepath.Join(dir, "makepkg.conf")}, Env: []string{"HOME=" + dir, "GNUPGHOME=" + home, "OPS_RECORD_SCRIPT=" + filepath.Join(dir, "record.py"), "OPS_RECORD_DIR=" + dir}, EphemeralHelpers: mode != "before-build"}
			if mode == "packagelist" {
				spec.Args = append(spec.Args, "--packagelist")
			}
			base := o.scope
			audit := &helperCleanupScope{ownedScope: base}
			o.scope = audit
			result, err := (Exec{Owner: o}).Run(context.Background(), spec)
			o.scope = base
			if audit.group == nil || !audit.group.killed || !audit.group.emptyAfterKill {
				t.Fatalf("helper kill/population-zero not exercised: %v", err)
			}
			if mode == "before-build" {
				var exit *exec.ExitError
				if err == nil || errors.As(err, &exit) || OwnershipFailed(err) || !strings.Contains(err.Error(), "background descendants") {
					t.Fatalf("original failure not reproduced: %v\n%s", err, result.Stderr)
				}
			} else if err != nil {
				t.Fatalf("makepkg: %v\n%s", err, result.Stderr)
			}
			if mode != "packagelist" {
				if _, err := os.Stat(filepath.Join(dir, "ops-ownership-fixture-1-1-any.pkg.tar")); err != nil {
					t.Fatal("package not created", err)
				}
			}
			records := fixtureRecords(t, dir)
			found := false
			for _, r := range records {
				if r.Role == "keyboxd" {
					found = true
				}
			}
			if mode != "unexpected-helper" && !found {
				t.Fatal("keyboxd lifetime not exercised")
			}
			if mode == "unexpected-helper" {
				b, err := os.ReadFile(filepath.Join(dir, "unexpected.pid"))
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(string(b))
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, r := range records {
					if r.PID == pid {
						found = true
					}
				}
				if !found || !audit.group.killedPIDs[strconv.Itoa(pid)] {
					t.Fatal("unexpected helper was not live at cgroup.kill")
				}
			}
			cg := checkContained(t, o, records, false)
			reapRecorded(t, records)
			if _, err := os.Stat("/sys/fs/cgroup" + cg); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("makepkg group remains", err)
			}
			t.Log("package result verified; recorded helpers reaped; populated=0/removal complete")
		})
	}
}

// Observe real interfaces without changing production cleanup decisions.
type helperCleanupScope struct {
	ownedScope
	group *helperCleanupGroup
}

func (s *helperCleanupScope) New(name string) (commandGroup, error) {
	g, err := s.ownedScope.New(name)
	if err != nil {
		return nil, err
	}
	cg := g.(*cgroupCommand)
	s.group = &helperCleanupGroup{commandGroup: g, path: "/sys/fs/cgroup" + cg.scope.path + "/" + name, killedPIDs: make(map[string]bool)}
	return s.group, nil
}

type helperCleanupGroup struct {
	commandGroup
	path                   string
	killed, emptyAfterKill bool
	killedPIDs             map[string]bool
}

func (g *helperCleanupGroup) Kill() error {
	b, err := os.ReadFile(g.path + "/cgroup.procs")
	if err != nil {
		return err
	}
	for _, pid := range strings.Fields(string(b)) {
		g.killedPIDs[pid] = true
	}
	g.killed = true
	return g.commandGroup.Kill()
}
func (g *helperCleanupGroup) Empty() (bool, error) {
	empty, err := g.commandGroup.Empty()
	if g.killed && empty && err == nil {
		g.emptyAfterKill = true
	}
	return empty, err
}
