package archtrust

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

func TestArchiveReadFailuresRemainInconclusive(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(fmt.Sprint("closed=", closed), func(t *testing.T) {
			f, err := os.OpenFile(filepath.Join(t.TempDir(), "archive"), os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			payload := []byte("unreadable evidence")
			if _, err := f.Write(payload); err != nil {
				t.Fatal(err)
			}
			if closed {
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			p := Package{size: int64(len(payload)), digest: sha256.Sum256(payload)}
			_, err = authenticateArchive(context.Background(), run.Exec{}, p, f, keyMaterial{})
			if err == nil || errors.Is(err, ErrArchiveMismatch) {
				t.Fatalf("archive I/O failure would plan repair instead of failing inspection: %v", err)
			}
		})
	}
}

func compressed(t *testing.T, data string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestInstalledContentEquivalence(t *testing.T) {
	for _, scenario := range []string{"ready", "empty", "large", "executable", "library", "missing", "symlink target", "symlink for file", "file for symlink", "fifo", "hard link", "managed hard link", "mode", "ownership", "backup", "backup symlink", "ancestor symlink"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "usr/lib"), 0755); err != nil {
				t.Fatal(err)
			}
			payload := "official executable and library bytes"
			if scenario == "empty" {
				payload = ""
			}
			if scenario == "large" {
				payload = strings.Repeat("large file ", 1<<20)
			}
			write := func(name, data string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0755); err != nil {
					t.Fatal(err)
				}
			}
			write("usr/lib/program", payload)
			write("usr/lib/library", payload)
			write("usr/lib/config", payload)
			if err := os.Symlink("program", filepath.Join(dir, "usr/lib/link")); err != nil {
				t.Fatal(err)
			}
			var manifest strings.Builder
			fmt.Fprintf(&manifest, "/set uid=%d gid=%d mode=755 type=file\n", os.Getuid(), os.Getgid())
			for _, name := range []string{"program", "library", "config"} {
				fmt.Fprintf(&manifest, "./usr/lib/%s size=%d sha256digest=%x\n", name, len(payload), sha256.Sum256([]byte(payload)))
			}
			manifest.WriteString("./usr/lib/link type=link mode=777 link=program\n")
			entries, err := parseManifest(compressed(t, manifest.String()), map[string]bool{"usr/lib/config": true})
			if err != nil {
				t.Fatal(err)
			}
			program := filepath.Join(dir, "usr/lib/program")
			link := filepath.Join(dir, "usr/lib/link")
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "executable":
				write("usr/lib/program", "substituted executable")
			case "library":
				write("usr/lib/library", "substituted library")
			case "missing":
				must(os.Remove(program))
			case "symlink target":
				must(os.Remove(link))
				must(os.Symlink("library", link))
			case "symlink for file":
				must(os.Remove(program))
				must(os.Symlink("library", program))
			case "file for symlink":
				must(os.Remove(link))
				write("usr/lib/link", payload)
			case "fifo":
				must(os.Remove(program))
				must(syscall.Mkfifo(program, 0755))
			case "hard link":
				must(os.Link(program, filepath.Join(dir, "outside")))
			case "managed hard link":
				must(os.Remove(filepath.Join(dir, "usr/lib/library")))
				must(os.Link(program, filepath.Join(dir, "usr/lib/library")))
			case "ownership":
				entries[0].uid++
			case "mode":
				must(os.Chmod(program, 0755|os.ModeSetuid))
			case "backup":
				write("usr/lib/config", "legitimate modified config")
			case "backup symlink":
				must(os.Remove(filepath.Join(dir, "usr/lib/config")))
				must(os.Symlink("program", filepath.Join(dir, "usr/lib/config")))
			case "ancestor symlink":
				must(os.Rename(filepath.Join(dir, "usr/lib"), filepath.Join(dir, "other")))
				must(os.Symlink("../other", filepath.Join(dir, "usr/lib")))
			}
			root, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			archive := authenticatedArchive{entries: entries}
			match, err := archive.matches(context.Background(), root)
			want := scenario == "ready" || scenario == "empty" || scenario == "large" || scenario == "backup" || scenario == "managed hard link"
			if err != nil || match != want {
				t.Fatalf("match=%v want=%v err=%v", match, want, err)
			}
			if want {
				again, err := archive.matches(context.Background(), root)
				if err != nil || !again {
					t.Fatal("equivalence is not idempotent", err)
				}
				write("usr/lib/program", "later custom replacement")
				if match, err := archive.matches(context.Background(), root); err != nil || match {
					t.Fatal("later replacement did not invalidate readiness", err)
				}
			}
		})
	}
}

func TestManifestRejectsUnsafeAndIncompleteEvidence(t *testing.T) {
	base := "/set uid=0 gid=0 mode=755 type=file\n"
	file := " size=0 sha256digest=" + fmt.Sprintf("%x", sha256.Sum256(nil)) + "\n"
	for _, body := range []string{"", "./../escape" + file, "./usr/../../escape" + file, "/absolute" + file, "./usr/\\056\\056/escape" + file, "./usr/file" + file + "./usr/file" + file, "./usr/file type=socket\n", "./usr/file size=0\n", "./usr/file size=0 size=1\n", "./usr/\\000file" + file} {
		if _, err := parseManifest(compressed(t, base+body), nil); err == nil {
			t.Errorf("accepted unsafe manifest %q", body)
		}
	}
}

// Mutate an already hashed entry at a deterministic boundary, without relying
// on scheduler timing or adding a hook to the production verifier.
type changingContext struct {
	context.Context
	beforeCheck func()
}

func (c changingContext) Err() error { c.beforeCheck(); return nil }

func TestInstalledReplacementDuringVerification(t *testing.T) {
	for _, inPlace := range []bool{false, true} {
		t.Run(fmt.Sprint("in-place=", inPlace), func(t *testing.T) {
			dir := t.TempDir()
			payload := []byte("official")
			var entries []entry
			for _, name := range []string{"first", "second"} {
				if err := os.WriteFile(filepath.Join(dir, name), payload, 0600); err != nil {
					t.Fatal(err)
				}
				entries = append(entries, entry{name: name, kind: "file", mode: 0600, uid: uint32(os.Getuid()), gid: uint32(os.Getgid()), size: int64(len(payload)), digest: sha256.Sum256(payload)})
			}
			checks := 0
			ctx := changingContext{Context: context.Background(), beforeCheck: func() {
				checks++
				if checks != 2 {
					return
				}
				file := filepath.Join(dir, "first")
				if !inPlace {
					if err := os.Remove(file); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(file, []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			}}
			root, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if match, err := (authenticatedArchive{entries: entries}).matches(ctx, root); match || err == nil {
				t.Fatalf("replacement accepted: %v %v", match, err)
			}
		})
	}
}

func TestCacheEvidenceDescriptorAndType(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "hardlink", "directory", "replacement"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "archive")
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(os.WriteFile(file, []byte("authenticated bytes"), 0600))
			switch kind {
			case "symlink":
				must(os.Rename(file, filepath.Join(dir, "target")))
				must(os.Symlink("target", file))
			case "fifo":
				must(os.Remove(file))
				must(syscall.Mkfifo(file, 0600))
			case "hardlink":
				must(os.Link(file, filepath.Join(dir, "other")))
			case "directory":
				must(os.Remove(file))
				must(os.Mkdir(file, 0700))
			}
			root, err := os.Open(dir)
			must(err)
			defer root.Close()
			path, err := openPath(root, "archive")
			must(err)
			defer path.Close()
			reader, err := regularReader(path)
			if kind != "replacement" {
				if err == nil {
					reader.Close()
					t.Fatal("unsafe archive input opened")
				}
				return
			}
			must(err)
			defer reader.Close()
			must(os.Remove(file))
			must(os.WriteFile(file, []byte("forged bytes"), 0600))
			data := make([]byte, 19)
			_, err = reader.Read(data)
			must(err)
			if string(data) != "authenticated bytes" {
				t.Fatal("pathname replacement switched descriptor", string(data))
			}
		})
	}
}

func TestOwnershipInventoryIsOnlySupportingEvidence(t *testing.T) {
	a := authenticatedArchive{entries: []entry{{name: "usr", kind: "dir"}, {name: "usr/program", kind: "file"}}}
	if !a.inventoryMatches("/usr/\n/usr/program\n") {
		t.Fatal("valid inventory rejected")
	}
	for _, output := range []string{"", "/usr/\n", "/usr/\n/usr/program\n/usr/extra\n", "/usr/\n/usr/program\n/usr/program\n", "/usr/../usr\n/usr/program\n"} {
		if a.inventoryMatches(output) {
			t.Fatal("ambiguous inventory accepted", output)
		}
	}
}
