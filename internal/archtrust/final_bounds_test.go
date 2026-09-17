package archtrust

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

func TestFinalHashCancellationDoesNotConsumeInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	input := bytes.NewBufferString("must not be hashed after cancellation")
	_, err := io.Copy(io.Discard, contextReader{ctx, input})
	if !errors.Is(err, context.Canceled) || input.Len() == 0 {
		t.Fatalf("cancellation ignored: remaining=%d err=%v", input.Len(), err)
	}
}

type emptyTransactionRunner struct{}

func (emptyTransactionRunner) Run(context.Context, run.Spec) (run.Result, error) {
	return run.Result{}, nil
}

func TestFinalTransactionArchiveBudgetPrecedesAcquisition(t *testing.T) {
	s := NewSource()
	s.database = map[string][]byte{}
	s.packages = map[string]Package{
		"one": {repository: "extra", name: "one", filename: "one-1-1-any.pkg.tar.zst", size: 5 << 30},
		"two": {repository: "extra", name: "two", filename: "two-1-1-any.pkg.tar.zst", size: 5 << 30},
	}
	downloaded := false
	s.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		downloaded = true
		return nil, errors.New("must not acquire an oversized transaction")
	})
	p, err := s.Prepare(context.Background(), emptyTransactionRunner{}, []string{"extra/one", "extra/two"})
	if p != nil {
		p.Close()
	}
	if downloaded || err == nil || !strings.Contains(err.Error(), "transaction archive budget") {
		t.Fatalf("D-F6: aggregate limit did not precede acquisition: downloaded=%v err=%v", downloaded, err)
	}
}

func TestFinalEvidenceDownloadBoundsAndCleanup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	p := Package{repository: "extra", filename: "fixture-1-1-any.pkg.tar.zst", size: 7}
	for _, tc := range []struct {
		name   string
		status int
		length int64
		body   string
	}{
		{"short", 200, -1, "short"},
		{"oversized", 200, -1, "oversized"},
		{"declared oversize", 200, 8, "oversize"},
		{"source moved", 404, 0, ""},
		{"redirect", 302, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSource()
			s.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, ContentLength: tc.length, Body: io.NopCloser(bytes.NewBufferString(tc.body)), Header: http.Header{"Location": {"http://untrusted.invalid/archive"}}}, nil
			})
			f, err := s.download(context.Background(), p)
			if f != nil || err == nil {
				t.Fatalf("unbounded or changed download accepted: %v", err)
			}
			left, err := os.ReadDir(dir)
			if err != nil || len(left) != 0 {
				t.Fatalf("failed download leaked temporary archive: %v %v", left, err)
			}
		})
	}
}

func TestFinalDistributionEvidenceFileBoundary(t *testing.T) {
	for _, kind := range []string{"normal", "oversized", "leaf symlink", "ancestor symlink", "directory", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(os.Mkdir(filepath.Join(dir, "keys"), 0700))
			file := filepath.Join(dir, "keys/public")
			must(os.WriteFile(file, []byte("keyring"), 0600))
			switch kind {
			case "oversized":
				must(os.WriteFile(file, bytes.Repeat([]byte{'x'}, 65), 0600))
			case "leaf symlink":
				must(os.Rename(file, file+"-real"))
				must(os.Symlink("public-real", file))
			case "ancestor symlink":
				must(os.Rename(filepath.Join(dir, "keys"), filepath.Join(dir, "real")))
				must(os.Symlink("real", filepath.Join(dir, "keys")))
			case "directory":
				must(os.Remove(file))
				must(os.Mkdir(file, 0700))
			case "hardlink":
				must(os.Link(file, file+"-link"))
			}
			root, err := os.Open(dir)
			must(err)
			defer root.Close()
			data, err := readRegular(root, "keys/public", 64)
			if kind == "normal" {
				if err != nil || string(data) != "keyring" {
					t.Fatal(string(data), err)
				}
			} else if err == nil {
				t.Fatal("unsafe trust evidence accepted")
			}
		})
	}
}
