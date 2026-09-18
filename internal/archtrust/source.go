package archtrust

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Endpoint is an Arch DevOps operated HTTPS geomirror. Redirects are forbidden;
// no configured user mirror or Server value can replace this source.
const Endpoint = "https://geo.mirror.pkgbuild.com"

// SourceError is inconclusive evidence, never package absence. Callers must not
// mask it with a secondary metadata service or an installed metadata match.
type SourceError struct{ Err error }

func (e *SourceError) Error() string { return e.Err.Error() }
func (e *SourceError) Unwrap() error { return e.Err }

var packageName = regexp.MustCompile(`^[A-Za-z0-9@._+][A-Za-z0-9@._+-]*$`)

// Package is an identity from an independently fetched repository snapshot.
// Its authentication fields cannot be constructed by callers.
type Package struct {
	repository, name, version, architecture, filename string
	digest                                            [32]byte
	signature                                         []byte
	size                                              int64
}

func (p Package) Target() string  { return p.repository + "/" + p.name }
func (p Package) Name() string    { return p.name }
func (p Package) Version() string { return p.version }

// Source owns an immutable in-memory snapshot. A new observation creates a new
// Source; installed payload results are never cached in it.
type Source struct {
	mu       sync.Mutex
	client   *http.Client
	database map[string][]byte
	packages map[string]Package
	previous map[string]Package
}

func NewSource() *Source {
	return &Source{client: &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("official source redirects are unsupported")
		},
	}}
}

func (s *Source) load(ctx context.Context) error {
	if s.database != nil {
		return nil
	}
	database, packages := map[string][]byte{}, map[string]Package{}
	for _, repo := range []string{"core", "extra", "multilib"} {
		data, err := s.fetch(ctx, Endpoint+"/"+repo+"/os/x86_64/"+repo+".db", 32<<20)
		if err != nil {
			return fmt.Errorf("official %s source unavailable: %w", repo, err)
		}
		parsed, err := parseDatabase(repo, data)
		if err != nil {
			return fmt.Errorf("invalid official %s database: %w", repo, err)
		}
		for name, pkg := range parsed {
			if _, exists := packages[name]; exists {
				return fmt.Errorf("ambiguous official package %s", name)
			}
			packages[name] = pkg
		}
		database[repo] = data
	}
	s.database, s.packages = database, packages
	return nil
}

func (s *Source) Lookup(ctx context.Context, target string) (Package, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, name, qualified := strings.Cut(target, "/")
	if !qualified {
		name, repo = target, ""
	}
	if !packageName.MatchString(name) || (qualified && repo != "core" && repo != "extra" && repo != "multilib") {
		return Package{}, false, fmt.Errorf("invalid official target")
	}
	if err := s.load(ctx); err != nil {
		return Package{}, false, err
	}
	p, ok := s.packages[name]
	if ok && qualified && repo != p.repository {
		return Package{}, false, fmt.Errorf("official package repository changed for %s", target)
	}
	return p, ok, nil
}

func (s *Source) fetch(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("official source HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return nil, fmt.Errorf("official source response too large")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("official source response too large")
	}
	return data, nil
}

func parseDatabase(repo string, data []byte) (map[string]Package, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	// Bound the expanded database as well as each individual record.
	limited := &io.LimitedReader{R: gz, N: 256 << 20}
	archive := tar.NewReader(limited)
	packages := map[string]Package{}
	seen := map[string]bool{}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == "" || path.Clean(name) != name || path.IsAbs(name) || strings.HasPrefix(name, "../") || seen[name] {
			return nil, fmt.Errorf("unsafe or duplicate database entry")
		}
		seen[name] = true
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > 1<<20 || strings.Count(name, "/") != 1 || path.Base(name) != "desc" {
			return nil, fmt.Errorf("unsupported database entry")
		}
		body, err := io.ReadAll(archive)
		if err != nil {
			return nil, err
		}
		pkg, err := parseDescription(repo, body)
		if err != nil {
			return nil, err
		}
		if path.Dir(name) != pkg.name+"-"+pkg.version {
			return nil, fmt.Errorf("database directory does not match package")
		}
		if _, exists := packages[pkg.name]; exists {
			return nil, fmt.Errorf("duplicate database package")
		}
		packages[pkg.name] = pkg
	}
	if limited.N == 0 || len(packages) == 0 {
		return nil, fmt.Errorf("empty or oversized official database")
	}
	// Consume the gzip trailer, including its checksum, instead of accepting an
	// archive whose tar end marker hides truncated or corrupted compressed data.
	padding := make([]byte, 32<<10)
	for {
		n, err := limited.Read(padding)
		for _, b := range padding[:n] {
			if b != 0 {
				return nil, fmt.Errorf("data after official database end marker")
			}
		}
		if err == io.EOF && limited.N > 0 {
			break
		}
		if err != nil || limited.N == 0 {
			return nil, fmt.Errorf("invalid compressed official database")
		}
	}
	return packages, nil
}

func parseDescription(repo string, data []byte) (Package, error) {
	fields := map[string][]string{}
	key := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.ContainsAny(line, "\x00\r") {
			return Package{}, fmt.Errorf("invalid database field")
		}
		if line == "" {
			key = ""
			continue
		}
		if key == "" {
			if len(line) < 3 || line[0] != '%' || line[len(line)-1] != '%' {
				return Package{}, fmt.Errorf("malformed database field")
			}
			if _, exists := fields[line]; exists {
				return Package{}, fmt.Errorf("duplicate database field")
			}
			key, fields[line] = line, []string{}
		} else {
			fields[key] = append(fields[key], line)
		}
	}
	one := func(key string) string {
		if len(fields[key]) != 1 {
			return ""
		}
		return fields[key][0]
	}
	p := Package{repository: repo, name: one("%NAME%"), version: one("%VERSION%"), architecture: one("%ARCH%"), filename: one("%FILENAME%")}
	digest, err := hex.DecodeString(one("%SHA256SUM%"))
	if err != nil || len(digest) != 32 {
		return Package{}, fmt.Errorf("missing official archive digest")
	}
	copy(p.digest[:], digest)
	p.signature, err = base64.StdEncoding.Strict().DecodeString(one("%PGPSIG%"))
	if err != nil || len(p.signature) == 0 || len(p.signature) > 64<<10 {
		return Package{}, fmt.Errorf("missing official archive signature")
	}
	p.size, err = strconv.ParseInt(one("%CSIZE%"), 10, 64)
	if err != nil || p.size <= 0 || p.size > 8<<30 || !packageName.MatchString(p.name) || p.version == "" || strings.ContainsAny(p.version, "/\\ \t") || (p.architecture != "x86_64" && p.architecture != "any") || path.Base(p.filename) != p.filename || !strings.HasPrefix(p.filename, p.name+"-") || !strings.Contains(p.filename, ".pkg.tar.") || strings.ContainsAny(p.filename, "?#%\\ \t\n") {
		return Package{}, fmt.Errorf("malformed official archive identity")
	}
	return p, nil
}

// Only the immediately preceding independent snapshot is retained. This bounds
// memory and is evidence, not a readiness receipt: keys and payload are rechecked.
func (s *Source) Next() *Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := NewSource()
	next.previous = s.packages
	return next
}

var ErrExactVersionUnavailable = errors.New("authenticated exact installed-version evidence unavailable")

func (s *Source) LookupVersion(ctx context.Context, target, version string) (Package, error) {
	current, found, err := s.Lookup(ctx, target)
	if err != nil {
		return Package{}, err
	}
	if !found {
		return Package{}, ErrExactVersionUnavailable
	}
	if current.version == version {
		return current, nil
	}
	s.mu.Lock()
	previous, ok := s.previous[current.name]
	s.mu.Unlock()
	if ok && previous.Target() == target && previous.version == version {
		return previous, nil
	}
	return Package{}, ErrExactVersionUnavailable
}
