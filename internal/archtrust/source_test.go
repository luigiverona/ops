package archtrust

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func description(name string) string {
	return "%NAME%\n" + name + "\n\n%VERSION%\n1-1\n\n%ARCH%\nany\n\n%FILENAME%\n" + name + "-1-1-any.pkg.tar.zst\n\n%SHA256SUM%\n" + strings.Repeat("0", 64) + "\n\n%PGPSIG%\nZml4dHVyZQ==\n\n%CSIZE%\n7\n\n"
}

func databaseFixture(t *testing.T, name, desc string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(desc))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, desc); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestIndependentSourceAndMultilib(t *testing.T) {
	s := NewSource()
	var requested []string
	s.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		requested = append(requested, r.URL.String())
		parts := strings.Split(r.URL.Path, "/")
		if r.URL.Scheme != "https" || r.URL.Host != "geo.mirror.pkgbuild.com" || len(parts) != 5 || parts[2] != "os" || parts[3] != "x86_64" || parts[4] != parts[1]+".db" {
			t.Fatalf("untrusted source %s", r.URL)
		}
		name := "package-" + parts[1]
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(databaseFixture(t, name+"-1-1/desc", description(name))))}, nil
	})
	for _, repo := range []string{"core", "extra", "multilib"} {
		p, found, err := s.Lookup(context.Background(), "package-"+repo)
		if err != nil || !found || p.Target() != repo+"/package-"+repo {
			t.Fatalf("%+v %v %v", p, found, err)
		}
	}
	if len(requested) != 3 {
		t.Fatal("source snapshot not retained", requested)
	}
	if _, found, err := s.Lookup(context.Background(), "absent"); err != nil || found {
		t.Fatal("complete source could not establish absence", err)
	}
	if _, _, err := s.Lookup(context.Background(), "extra/package-core"); err == nil {
		t.Fatal("repository mismatch accepted")
	}
}

func TestSourceFailureIsInconclusive(t *testing.T) {
	for _, scenario := range []string{"network", "404", "redirect", "malformed", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			s := NewSource()
			s.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				response := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("invalid database")), Header: http.Header{}}
				switch scenario {
				case "network":
					return nil, fmt.Errorf("source unavailable")
				case "404":
					response.StatusCode = 404
				case "redirect":
					response.StatusCode = 302
					response.Header.Set("Location", "https://custom.example/core.db")
				case "duplicate":
					response.Body = io.NopCloser(bytes.NewReader(databaseFixture(t, "same-1-1/desc", description("same"))))
				}
				return response, nil
			})
			if _, found, err := s.Lookup(context.Background(), "git"); err == nil || found {
				t.Fatal("source failure classified as absence or presence")
			}
		})
	}
}

func TestDatabaseIdentityValidation(t *testing.T) {
	base := description("package")
	for _, data := range []string{base + "%NAME%\npackage\n\n", strings.Replace(base, "%ARCH%\nany", "%ARCH%\naarch64", 1), strings.Replace(base, "Zml4dHVyZQ==", "", 1), strings.Replace(base, strings.Repeat("0", 64), "bad", 1), strings.Replace(base, "package-1-1-any.pkg.tar.zst", "../escape.pkg.tar.zst", 1), strings.Replace(base, "%CSIZE%\n7", "%CSIZE%\n-1", 1)} {
		if _, err := parseDatabase("core", databaseFixture(t, "package-1-1/desc", data)); err == nil {
			t.Fatal("malformed database accepted")
		}
	}
	for _, name := range []string{"../escape", "/absolute", "package-1-1/../desc", "other-1-1/desc"} {
		if _, err := parseDatabase("core", databaseFixture(t, name, base)); err == nil {
			t.Fatal("unsafe database archive accepted")
		}
	}
}

// Explicit read-only integration: downloads current public official metadata
// and one small public package. No key import, package installation, or sudo.
func TestOfficialArchIntegration(t *testing.T) {
	if os.Getenv("OPS_ARCH_INTEGRATION") != "1" {
		t.Skip("set OPS_ARCH_INTEGRATION=1 for explicit network validation")
	}
	s := NewSource()
	for _, target := range []string{"core/acl", "extra/git", "core/openssh", "extra/github-cli", "extra/flatpak"} {
		t.Run(target, func(t *testing.T) {
			p, found, err := s.Lookup(context.Background(), target)
			if err != nil || !found {
				t.Fatal("lookup", found, err)
			}
			data, err := s.fetch(context.Background(), Endpoint+"/"+p.repository+"/os/x86_64/"+p.filename, p.size)
			if err != nil {
				t.Fatal(err)
			}
			f, err := os.CreateTemp(t.TempDir(), "archive-*")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.Write(data); err != nil {
				t.Fatal(err)
			}
			keys, err := systemKeys()
			if err != nil {
				t.Fatal(err)
			}
			a, err := authenticateArchive(context.Background(), run.Exec{}, p, f, keys)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("authenticated %s %s sha256=%x entries=%d", p.Target(), p.Version(), p.digest, len(a.entries))
		})
	}
}
