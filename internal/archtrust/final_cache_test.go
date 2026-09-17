package archtrust

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type inventoryRunner struct {
	run.Runner
	inventory string
}

func (r inventoryRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if s.Name == "pacman" {
		if strings.Join(s.Args, " ") != "-Qlq -- fixture" {
			return run.Result{}, fmt.Errorf("unexpected package operation: %v", s.Args)
		}
		return run.Result{Stdout: r.inventory}, nil
	}
	if s.Name != "gpg" && s.Name != "bsdtar" {
		return run.Result{}, fmt.Errorf("unexpected verifier command: %s", s.Name)
	}
	return r.Runner.Run(ctx, s)
}

// This regression uses the unchanged public entry point and fails at cd0a6c3:
// the old ENOENT branch reports repair instead of attempting evidence retrieval.
func TestFinalMissingCacheIsNotRepairEvidence(t *testing.T) {
	s := NewSource()
	p := Package{repository: "extra", name: "ops-final-review-missing-evidence", filename: "ops-final-review-no-such-archive-61983.pkg.tar.zst", size: 1}
	s.database = map[string][]byte{}
	s.packages = map[string]Package{p.name: p}
	requested := false
	s.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		requested = true
		return nil, errors.New("isolated source unavailable")
	})
	match, err := s.CachedInstalled(context.Background(), run.Exec{}, p.Target())
	if match || err == nil {
		t.Fatalf("missing cache classified as repair: match=%v err=%v", match, err)
	}
	if !requested {
		t.Fatalf("source was not consulted: %v", err)
	}
}

// Called with genuine disposable OpenPGP signatures by the native trust test.
func checkFinalCacheEviction(t *testing.T, p Package, keys keyMaterial, archive, payload []byte) {
	t.Helper()
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "var/cache/pacman/pkg")
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	p.filename = "fixture-1-1-any.pkg.tar.gz"
	cache := filepath.Join(cacheDir, p.filename)
	if err := os.WriteFile(cache, archive, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "program"), payload, 0755); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	s := NewSource()
	requests := 0
	body := archive
	status := 200
	s.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.String() != Endpoint+"/extra/os/x86_64/"+p.filename {
			t.Fatalf("changed snapshot identity: %s", req.URL)
		}
		return &http.Response{StatusCode: status, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
	})
	runner := inventoryRunner{run.Exec{}, "/program\n"}
	check := func(want bool, wantErr bool) {
		t.Helper()
		match, err := s.installed(context.Background(), runner, p, root, keys)
		if match != want || (err != nil) != wantErr {
			t.Fatalf("match=%v err=%v requests=%d", match, err, requests)
		}
	}
	check(true, false)
	check(true, false)
	if requests != 0 {
		t.Fatal("valid cache triggered downloads")
	}
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	check(true, false)
	check(true, false)
	if requests != 2 {
		t.Fatalf("missing cache not reconstructed read-only: %d", requests)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("inspection populated package cache: %v", err)
	}
	// Bad local evidence is replaceable without reinstalling installed content.
	if err := os.WriteFile(cache, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	check(true, false)
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	status = 404
	check(false, true)
	status = 200
	body = bytes.Repeat([]byte{'x'}, len(archive))
	check(false, true)
	body = archive
	original := p.signature
	p.signature = []byte("malformed signature")
	check(false, true)
	p.signature = original
	if err := os.WriteFile(filepath.Join(dir, "program"), bytes.Repeat([]byte{'x'}, len(payload)), 0755); err != nil {
		t.Fatal(err)
	}
	check(false, false)
	if err := os.WriteFile(filepath.Join(dir, "program"), payload, 0755); err != nil {
		t.Fatal(err)
	}
	runner.inventory += "/malicious-extra\n"
	check(false, false)
}
