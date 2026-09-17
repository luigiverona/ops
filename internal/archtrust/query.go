package archtrust

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/luigiverona/ops/internal/run"
)

// Query uses libalpm's native dependency/provider resolver with only independent
// HTTPS database bytes. It never refreshes or reads the live sync databases.
// The live local database supplies installed constraints, not official identity.
func (s *Source) Query(ctx context.Context, runner run.Runner, args []string) (run.Result, error) {
	if len(args) == 0 || (args[0] != "-Si" && args[0] != "-Sl" && args[0] != "-Sp" && args[0] != "-Sup") {
		return run.Result{}, fmt.Errorf("unsupported official metadata query")
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "--config") || strings.HasPrefix(arg, "--dbpath") || strings.HasPrefix(arg, "--root") || strings.HasPrefix(arg, "--sysroot") || strings.HasPrefix(arg, "--gpgdir") {
			return run.Result{}, fmt.Errorf("official source override forbidden")
		}
	}
	s.mu.Lock()
	err := s.load(ctx)
	database := s.database
	s.mu.Unlock()
	if err != nil {
		return run.Result{}, &SourceError{Err: err}
	}
	dir, err := os.MkdirTemp("", "ops-arch-query-*")
	if err != nil {
		return run.Result{}, &SourceError{Err: err}
	}
	defer os.RemoveAll(dir)
	if err := os.Mkdir(filepath.Join(dir, "sync"), 0700); err != nil {
		return run.Result{}, &SourceError{Err: err}
	}
	if err := os.Symlink("/var/lib/pacman/local", filepath.Join(dir, "local")); err != nil {
		return run.Result{}, &SourceError{Err: err}
	}
	for repo, data := range database {
		if err := os.WriteFile(filepath.Join(dir, "sync", repo+".db"), data, 0600); err != nil {
			return run.Result{}, &SourceError{Err: err}
		}
	}
	config := "[options]\nArchitecture = x86_64\nSigLevel = Required DatabaseOptional\n"
	for _, repo := range []string{"core", "extra", "multilib"} {
		config += "[" + repo + "]\nServer = " + Endpoint + "/" + repo + "/os/x86_64\n"
	}
	configPath := filepath.Join(dir, "pacman.conf")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		return run.Result{}, &SourceError{Err: err}
	}
	query := append([]string{"--config", configPath, "--dbpath", dir, "--root", "/"}, args...)
	result, err := runner.Run(ctx, run.Spec{Name: "pacman", Args: query, FailureOutput: run.FailureStderr})
	if err != nil {
		// Only an exact -Si miss confirmed by the independent snapshot may
		// consult supplemental API metadata. Native failures are inconclusive.
		if len(args) == 3 && args[0] == "-Si" && args[1] == "--" {
			_, found, lookupErr := s.Lookup(ctx, args[2])
			if lookupErr == nil && !found {
				return result, err
			}
		}
		return result, &SourceError{Err: err}
	}
	return result, nil
}

// CachedInstalled authenticates installed content using the ordinary cache when
// available, otherwise a disposable read-only download. Missing cache evidence
// never requires a package transaction. Source/key/read failures are inconclusive.
func (s *Source) CachedInstalled(ctx context.Context, runner run.Runner, target string) (bool, error) {
	p, found, err := s.Lookup(ctx, target)
	if err != nil || !found {
		return false, err
	}
	root, err := os.Open("/")
	if err != nil {
		return false, err
	}
	defer root.Close()
	keys, err := systemKeys()
	if err != nil {
		return false, err
	}
	match, err := s.installed(ctx, runner, p, root, keys)
	if err != nil {
		return false, err
	}
	after, err := systemKeys()
	if err != nil || !sameKeys(keys, after) {
		return false, fmt.Errorf("official trust material changed during inspection")
	}
	return match, nil
}

// root and keys are explicit so isolated tests exercise the production evidence
// path without touching the workstation's filesystem or distribution keyring.
func (s *Source) installed(ctx context.Context, runner run.Runner, p Package, root *os.File, keys keyMaterial) (bool, error) {
	var a authenticatedArchive
	cache, err := openPath(root, "var/cache/pacman/pkg/"+p.filename)
	if err == nil {
		defer cache.Close()
		f, e := regularReader(cache)
		if e != nil {
			return false, e
		}
		defer f.Close()
		a, err = authenticateArchive(ctx, runner, p, f, keys)
	}
	if os.IsNotExist(err) || errors.Is(err, ErrArchiveMismatch) {
		// Authenticate against the SAME snapshot identity, including its embedded
		// signature. Never re-resolve a filename/version from a newer generation.
		f, e := s.download(ctx, p)
		if e != nil {
			return false, e
		}
		defer func() { f.Close(); os.Remove(f.Name()) }()
		a, err = authenticateArchive(ctx, runner, p, f, keys)
	}
	if err != nil {
		return false, err
	}
	inventory, err := runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Qlq", "--", p.name}, FailureOutput: run.FailureStderr})
	if err != nil {
		return false, err
	}
	if !a.inventoryMatches(inventory.Stdout) {
		return false, nil
	}
	return a.matches(ctx, root)
}

// Local ownership inventory is supporting evidence only; every expected object
// and digest comes from the authenticated archive. Extra declared owned paths
// are rejected rather than hidden by a copied installed metadata tuple.
func (a authenticatedArchive) inventoryMatches(output string) bool {
	expected := map[string]bool{}
	for _, e := range a.entries {
		expected[e.name] = true
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if !strings.HasPrefix(line, "/") {
			return false
		}
		name := strings.TrimSuffix(strings.TrimPrefix(line, "/"), "/")
		if !safeManifestPath(name) || seen[name] || !expected[name] {
			return false
		}
		seen[name] = true
	}
	return len(seen) == len(expected)
}
