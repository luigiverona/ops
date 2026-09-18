package archtrust

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFinalDirectoryMode(t *testing.T) {
	d := t.TempDir()
	os.Mkdir(filepath.Join(d, "private"), 0700)
	root, err := os.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a := authenticatedArchive{entries: []entry{{name: "private", kind: "dir", mode: 0700, uid: uint32(os.Getuid()), gid: uint32(os.Getgid())}}}
	if ok, err := a.matches(context.Background(), root); err != nil || !ok {
		t.Fatalf("control %v %v", ok, err)
	}
	if err := os.Chmod(filepath.Join(d, "private"), 0777); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.matches(context.Background(), root); err != nil || ok {
		t.Fatalf("unsafe directory accepted: match=%v err=%v", ok, err)
	}
}
