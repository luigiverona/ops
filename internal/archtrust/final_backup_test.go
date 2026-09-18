package archtrust

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestFinalBackupPolicyFollowsAuthenticatedVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	manifest := compressed(t, fmt.Sprintf("./config type=file mode=600 uid=%d gid=%d size=6 sha256digest=%x\n", os.Getuid(), os.Getgid(), sha256.Sum256([]byte("signed"))))
	for _, version := range []string{"1-1", "2-1"} {
		p := Package{name: "fixture", version: version, architecture: "any"}
		info := "pkgname = fixture\npkgver = " + version + "\narch = any\n"
		if version == "1-1" {
			info += "backup = config\n"
		}
		backups, err := packageInfo(p, []byte(info))
		if err != nil {
			t.Fatal(err)
		}
		entries, err := parseManifest(manifest, backups)
		if err != nil {
			t.Fatal(err)
		}
		// Even if a forged local database still marks this file as a backup,
		// only the backup list obtained after archive authentication is consulted.
		local := filepath.Join(dir, "var/lib/pacman/local/fixture-"+version)
		if err := os.MkdirAll(local, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(local, "files"), []byte("%BACKUP%\nconfig\tforged-checksum\n"), 0600); err != nil {
			t.Fatal(err)
		}
		match, err := (authenticatedArchive{entries: entries}).matches(context.Background(), root)
		if err != nil || match != (version == "1-1") {
			t.Fatalf("version=%s match=%v err=%v", version, match, err)
		}
	}
}
