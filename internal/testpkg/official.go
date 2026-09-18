package testpkg

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/run"
)

func FakePrepared(targets []string) (*archtrust.Prepared, error) {
	p := &archtrust.Prepared{Databases: map[string][]byte{}}
	for _, repo := range []string{"core", "extra", "multilib"} {
		p.Databases[repo] = []byte("authenticated fixture database " + repo)
	}
	for _, target := range targets {
		_, name, _ := strings.Cut(target, "/")
		f, err := os.CreateTemp("", "ops-test-official-*")
		if err != nil {
			p.Close()
			return nil, err
		}
		data := []byte("authenticated fixture package " + target)
		if _, err := f.Write(data); err != nil {
			f.Close()
			os.Remove(f.Name())
			p.Close()
			return nil, err
		}
		f.Seek(0, io.SeekStart)
		p.Archives = append(p.Archives, archtrust.Archive{Target: target, Filename: name + "-1-1-any.pkg.tar.zst", Digest: sha256.Sum256(data), File: f})
	}
	return p, nil
}

var officialStage = struct {
	sync.Mutex
	sequence uint64
	files    map[string][]byte
}{files: map[string][]byte{}}

// TransactionSpec lets orchestration fakes apply the native pacman operation
// inside the private namespace. Namespace/staging policy has separate tests.
func TransactionSpec(s run.Spec) run.Spec {
	if s.Name != "sudo" || len(s.Args) < 3 {
		return s
	}
	start := -1
	for i, arg := range s.Args {
		if arg == "pacman" {
			start = i
			break
		}
	}
	if start < 0 {
		return s
	}
	args := []string{"-n", "pacman"}
	for i := start + 1; i < len(s.Args); i++ {
		if s.Args[i] == "--config" {
			i++
			continue
		}
		args = append(args, s.Args[i])
	}
	s.Args = args
	return s
}

// OfficialStage simulates the new protected official staging commands without
// writing any privileged path. Existing adversarial staging fakes still handle
// their deliberately invalid paths and failures themselves.
func OfficialStage(s run.Spec) (run.Result, bool) {
	if s.Name != "sudo" || len(s.Args) < 2 {
		return run.Result{}, false
	}
	officialStage.Lock()
	defer officialStage.Unlock()
	last := s.Args[len(s.Args)-1]
	owned := strings.HasPrefix(last, "/var/tmp/ops-paru-OFFICIAL")
	switch s.Args[1] {
	case "mktemp":
		if !strings.HasPrefix(last, "ops-paru-OFFICIAL") {
			break
		}
		officialStage.sequence++
		return run.Result{Stdout: fmt.Sprintf("/var/tmp/ops-paru-OFFICIAL%012d\n", officialStage.sequence)}, true
	case "stat":
		if last == "/var/tmp" {
			return run.Result{Stdout: "0\t43ff\t2\n"}, true
		}
		if owned {
			if _, ok := officialStage.files[last]; ok {
				return run.Result{Stdout: "0\t8180\t1\n"}, true
			}
			return run.Result{Stdout: "0\t41c0\t2\n"}, true
		}
		if last == "/var" || last == "/var/cache" || last == "/var/cache/pacman" || last == "/var/cache/pacman/pkg" {
			return run.Result{Stdout: "0\t41ed\t2\n"}, true
		}
		if strings.HasPrefix(last, "/var/cache/pacman/pkg/") {
			return run.Result{Stdout: "0\t81a4\t1\n"}, true
		}
	case "install":
		if owned {
			if s.Stdin != nil {
				data, err := io.ReadAll(s.Stdin)
				if err != nil {
					panic(err)
				}
				officialStage.files[last] = data
			}
			return run.Result{}, true
		}
		if strings.HasPrefix(last, "/var/cache/pacman/pkg/") {
			return run.Result{}, true
		}
	case "sha256sum":
		if data, ok := officialStage.files[last]; ok {
			return run.Result{Stdout: fmt.Sprintf("%x  %s\n", sha256.Sum256(data), last)}, true
		}
	case "rm", "rmdir":
		if owned {
			for name := range officialStage.files {
				if filepath.Dir(name) == last || name == last {
					delete(officialStage.files, name)
				}
			}
			return run.Result{}, true
		}
	}
	return run.Result{}, false
}
