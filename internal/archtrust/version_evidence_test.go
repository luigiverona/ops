package archtrust

import (
	"context"
	"errors"
	"github.com/luigiverona/ops/internal/run"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// Invoked by the native signature fixture: exact old membership, digest,
// signature, authenticated manifest and installed bytes all participate.
func checkHistoricalVersionEvidence(t *testing.T, p Package, keys keyMaterial, archive, payload []byte) {
	t.Helper()
	p.filename = "fixture-1-1-any.pkg.tar.gz"
	original := NewSource()
	original.database = map[string][]byte{}
	original.packages = map[string]Package{p.name: p}
	current := original.Next()
	newer := p
	newer.version = "2-1"
	newer.filename = "fixture-2-1-any.pkg.tar.gz"
	current.database = map[string][]byte{}
	current.packages = map[string]Package{p.name: newer}
	selected, err := current.LookupVersion(context.Background(), p.Target(), p.version)
	if err != nil || selected.version != p.version || selected.filename != p.filename {
		t.Fatalf("exact identity lost: %+v %v", selected, err)
	}
	rootDir := t.TempDir()
	cache := filepath.Join(rootDir, "var/cache/pacman/pkg", p.filename)
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, archive, 0600); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(rootDir, "program")
	if err := os.WriteFile(program, payload, 0755); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	runner := inventoryRunner{run.Exec{}, "/program\n"}
	for range 2 {
		match, err := current.installed(context.Background(), runner, selected, root, keys)
		if err != nil || !match {
			t.Fatalf("genuine historical content: %v %v", match, err)
		}
	}
	if err := os.WriteFile(program, []byte("forged old"), 0755); err != nil {
		t.Fatal(err)
	}
	if match, err := current.installed(context.Background(), runner, selected, root, keys); err != nil || match {
		t.Fatalf("forged old accepted: %v %v", match, err)
	}
	if err := os.WriteFile(program, payload, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	requests := 0
	current.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.Path != "/extra/os/x86_64/"+p.filename {
			t.Fatal("historical evidence changed to current archive", req.URL)
		}
		return nil, errors.New("historical archive unavailable")
	})
	if match, err := current.installed(context.Background(), runner, selected, root, keys); err == nil || match || requests != 1 {
		t.Fatalf("cache deletion misclassified: %v %v", match, err)
	}
	fresh := NewSource()
	fresh.database = current.database
	fresh.packages = current.packages
	if _, err := fresh.LookupVersion(context.Background(), p.Target(), p.version); !errors.Is(err, ErrExactVersionUnavailable) {
		t.Fatal("invented historical membership", err)
	}
	// A second refresh retains only the immediately preceding identity, bounding
	// memory instead of accumulating a persistent archive or readiness receipts.
	third := current.Next()
	third.database = current.database
	third.packages = current.packages
	if _, err := third.LookupVersion(context.Background(), p.Target(), p.version); !errors.Is(err, ErrExactVersionUnavailable) {
		t.Fatal("unbounded historical retention", err)
	}
}
