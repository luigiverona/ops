// Package resolve performs exact, read-only package resolution.
package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/pgp"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
)

// Resolver resolves official, AUR, and Flathub identifiers without fallback.
type Resolver struct {
	Runner run.Runner
	Client *http.Client
}

// QueryError marks an inconclusive repository operation at its origin, before
// dependency/build context wraps it. Display text never determines recovery.
type QueryError struct{ Err error }

func (e *QueryError) Error() string { return "could not query official repositories: " + e.Err.Error() }
func (e *QueryError) Unwrap() error { return e.Err }

// UserPGPKey reports whether the normal user's keyring has exactly fingerprint.
func (r Resolver) UserPGPKey(ctx context.Context, fingerprint string) (bool, error) {
	return (pgp.Manager{Runner: r.Runner}).Has(ctx, fingerprint)
}

var gitObject = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)

func (r Resolver) Pacman(ctx context.Context, name string) (plan.Package, bool, error) {
	result, err := archrepo.Query(ctx, r.Runner, []string{"-Si", "--", name})
	if err != nil {
		var sourceErr *archtrust.SourceError
		if errors.As(err, &sourceErr) {
			return plan.Package{}, false, &QueryError{Err: err}
		}
		// A native query of the acquired snapshot may miss an exact package.
		// The Arch API can establish metadata presence/absence, but cannot
		// replace unavailable independent transaction or archive evidence.
		pkg, found, lookupErr := r.archPackage(ctx, name)
		if lookupErr != nil {
			return pkg, false, errors.Join(err, lookupErr)
		}
		return pkg, found, nil
	}
	fields, parseErr := archrepo.ParseInfo(result.Stdout)
	if parseErr != nil {
		return plan.Package{}, false, &QueryError{Err: parseErr}
	}
	if fields["Name"] != name || !archrepo.Official(fields["Repository"]) {
		return plan.Package{}, false, &QueryError{Err: errors.New("independent source returned an unexpected package identity")}
	}
	return plan.Package{Name: name, Repository: fields["Repository"]}, true, nil
}

func (r Resolver) archPackage(ctx context.Context, name string) (plan.Package, bool, error) {
	var response struct {
		Version int  `json:"version"`
		Count   *int `json:"count"`
		Page    int  `json:"page"`
		Pages   int  `json:"num_pages"`
		Valid   bool `json:"valid"`
		Results []struct {
			Name         string `json:"pkgname"`
			Repository   string `json:"repo"`
			Architecture string `json:"arch"`
		} `json:"results"`
	}
	endpoint := "https://archlinux.org/packages/search/json/?name=" + url.QueryEscape(name) + "&arch=x86_64&arch=any"
	for _, repo := range archrepo.Repositories() {
		endpoint += "&repo=" + strings.ToUpper(repo[:1]) + repo[1:]
	}
	data, status, err := r.getBytes(ctx, endpoint)
	if err != nil {
		return plan.Package{}, false, err
	}
	if err := unambiguousOfficialJSON(data); err != nil {
		return plan.Package{}, false, err
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return plan.Package{}, false, err
	}
	if status != http.StatusOK || response.Version != 2 || !response.Valid || response.Results == nil || response.Count == nil || *response.Count != len(response.Results) || response.Page != 1 || response.Pages != 1 {
		return plan.Package{}, false, errors.New("official repository query returned an invalid response")
	}
	if len(response.Results) > 1 {
		return plan.Package{}, false, errors.New("ambiguous official package metadata")
	}
	for _, result := range response.Results {
		if result.Name != name || (result.Architecture != "x86_64" && result.Architecture != "any") {
			return plan.Package{}, false, errors.New("official repository query returned unexpected package metadata")
		}
		if !archrepo.Official(result.Repository) {
			return plan.Package{}, false, errors.New("official repository query returned an unexpected repository")
		}
		return plan.Package{Name: name, Repository: result.Repository}, true, nil
	}
	return plan.Package{}, false, nil
}

func (r Resolver) AUR(ctx context.Context, name string) (plan.Package, bool, error) {
	if !aurmeta.ValidPackageName(name) {
		return plan.Package{}, false, errors.New("invalid AUR package name")
	}
	var response struct {
		Version     int    `json:"version"`
		Type        string `json:"type"`
		ResultCount *int   `json:"resultcount"`
		Results     []struct {
			Name, PackageBase string
		} `json:"results"`
	}
	endpoint := "https://aur.archlinux.org/rpc/v5/info?arg%5B%5D=" + url.QueryEscape(name)
	status, err := r.getJSON(ctx, endpoint, &response)
	if err != nil {
		return plan.Package{}, false, err
	}
	if status != http.StatusOK || response.Version != 5 || response.Type != "multiinfo" || response.Results == nil || response.ResultCount == nil {
		return plan.Package{}, false, errors.New("AUR query returned an invalid response")
	}
	if *response.ResultCount != len(response.Results) || *response.ResultCount > 1 {
		return plan.Package{}, false, errors.New("malformed AUR response count")
	}
	if *response.ResultCount == 0 {
		return plan.Package{}, false, nil
	}
	if response.Results[0].Name != name {
		return plan.Package{}, false, errors.New("AUR query returned a different identifier")
	}
	p := response.Results[0]
	if !aurmeta.ValidPackageName(p.PackageBase) {
		return plan.Package{}, false, errors.New("invalid AUR package base")
	}
	return plan.Package{Name: p.Name, PackageBase: p.PackageBase}, true, nil
}

// AURSource pins .SRCINFO to the exact AUR Git commit that will be reviewed.
func (r Resolver) AURSource(ctx context.Context, name string) (plan.AURSource, bool, error) {
	if !aurmeta.ValidPackageName(name) {
		return plan.AURSource{}, false, errors.New("invalid AUR package base")
	}
	data, status, err := r.getBytes(ctx, "https://aur.archlinux.org/"+name+".git/info/refs?service=git-upload-pack")
	if err != nil {
		return plan.AURSource{}, false, err
	}
	if status == http.StatusNotFound {
		return plan.AURSource{}, false, nil
	}
	commit, err := advertisedHEAD(data)
	if err != nil {
		return plan.AURSource{}, false, err
	}
	endpoint := "https://aur.archlinux.org/cgit/aur.git/plain/.SRCINFO?h=" + url.QueryEscape(name) + "&id=" + url.QueryEscape(commit)
	data, status, err = r.getBytes(ctx, endpoint)
	if err != nil {
		return plan.AURSource{}, false, err
	}
	if status == http.StatusNotFound {
		return plan.AURSource{}, false, nil
	}
	metadata, err := aurmeta.Parse(data)
	if err != nil {
		return plan.AURSource{}, false, fmt.Errorf("parse pinned .SRCINFO: %w", err)
	}
	if metadata.PackageBase != name {
		return plan.AURSource{}, false, errors.New("pinned .SRCINFO package base does not match request")
	}
	return plan.AURSource{Commit: commit, Metadata: metadata}, true, nil
}

// OfficialDependency includes the dependency closure, including installed
// satisfiers omitted by pacman's print transaction. Every edge is resolved by
// pacman, and every satisfied binding uses the shared installed predicate.
func (r Resolver) OfficialDependency(ctx context.Context, requirement string) (plan.OfficialDependency, error) {
	binding, err := r.officialDependency(ctx, requirement)
	if err != nil {
		return binding, err
	}
	queue := append([]string(nil), binding.Packages...)
	packages := map[string]string{}
	for _, target := range queue {
		_, name, _ := archrepo.Split(target)
		packages[name] = target
	}
	resolved := map[string]bool{requirement: true}
	for i := 0; i < len(queue); i++ {
		target := queue[i]
		result, err := archrepo.Query(ctx, r.Runner, []string{"-Si", "--", target})
		if err != nil {
			return binding, &QueryError{Err: err}
		}
		info, err := archrepo.ParseInfo(result.Stdout)
		repo, name, _ := archrepo.Split(target)
		if err != nil || info["Repository"] != repo || info["Name"] != name || info["Depends On"] == "" {
			return binding, &QueryError{Err: fmt.Errorf("missing or changed dependency metadata for %s", target)}
		}
		if info["Depends On"] == "None" {
			continue
		}
		for _, dependency := range strings.Fields(info["Depends On"]) {
			if _, err := aurmeta.ParseDependency(dependency); err != nil {
				return binding, &QueryError{Err: err}
			}
			if resolved[dependency] {
				continue
			}
			resolved[dependency] = true
			child, err := r.officialDependency(ctx, dependency)
			if err != nil {
				return binding, fmt.Errorf("dependency of %s: %w", target, err)
			}
			binding.Satisfied = binding.Satisfied && child.Satisfied
			for _, member := range child.Packages {
				_, concrete, _ := archrepo.Split(member)
				if previous := packages[concrete]; previous != "" {
					if previous != member {
						return binding, &QueryError{Err: fmt.Errorf("conflicting repositories for dependency %s", concrete)}
					}
					continue
				}
				packages[concrete] = member
				queue = append(queue, member)
			}
		}
	}
	sort.Strings(queue)
	binding.Packages = queue
	return binding, nil
}

// officialDependency resolves one edge. -T alone has no source semantics.
func (r Resolver) officialDependency(ctx context.Context, requirement string) (plan.OfficialDependency, error) {
	binding := plan.OfficialDependency{Requirement: requirement}
	result, err := r.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "pacman", Args: []string{"-T", "--", requirement}})
	if err == nil {
		if strings.TrimSpace(result.Stdout) != "" {
			return binding, errors.New("pacman dependency test returned contradictory output")
		}
		binding.Satisfied = true
	}
	if err != nil && (!run.Exited(err, 127) || strings.TrimSpace(result.Stdout) != requirement || strings.TrimSpace(result.Stderr) != "") {
		return binding, fmt.Errorf("inspect installed dependency: %w", err)
	}
	format := "%r/%n\t%P"
	result, err = archrepo.Query(ctx, r.Runner, []string{"-Sp", "--noconfirm", "--print-format", format, "--", requirement})
	if err != nil {
		return binding, &QueryError{Err: err}
	}
	want := aurmeta.DependencyName(requirement)
	if want == "" {
		return binding, fmt.Errorf("invalid dependency requirement %q", requirement)
	}
	candidates := make(map[string]bool)
	packages := make(map[string]bool)
	records, err := parseProviderTransaction(result.Stdout)
	if err != nil {
		return binding, &QueryError{Err: fmt.Errorf("pacman returned invalid transaction metadata for %q: %w", requirement, err)}
	}
	for _, record := range records {
		if packages[record.Name] {
			return binding, &QueryError{Err: fmt.Errorf("pacman returned invalid or duplicate transaction metadata for %q", requirement)}
		}
		packages[record.Name] = true
		_, concreteName, _ := archrepo.Split(record.Name)
		if concreteName == want || providesName(record.Provides, want) {
			candidates[record.Name] = true
		}
	}
	// An exact unversioned package target takes precedence over virtual
	// providers pulled in as its dependencies (for example ca-certificates).
	// Versioned ambiguities still fail closed rather than guessing a satisfier.
	if requirement == want {
		for target := range candidates {
			_, name, _ := archrepo.Split(target)
			if name == want {
				candidates = map[string]bool{target: true}
				break
			}
		}
	}
	if len(candidates) != 1 {
		return binding, &QueryError{Err: fmt.Errorf("pacman selected %d concrete providers for %q", len(candidates), requirement)}
	}
	for name := range candidates {
		binding.Provider = name
	}
	for name := range packages {
		binding.Packages = append(binding.Packages, name)
	}
	sort.Strings(binding.Packages)
	if binding.Satisfied {
		for _, target := range binding.Packages {
			match, err := archrepo.InstalledMatch(ctx, r.Runner, target)
			if err != nil {
				return binding, &QueryError{Err: err}
			}
			if !match {
				binding.Satisfied = false
			}
		}
	}
	return binding, nil
}

type providerTransactionRecord struct {
	Name     string
	Provides []string
}

func parseProviderTransaction(output string) ([]providerTransactionRecord, error) {
	lines := strings.Split(output, "\n")
	records := make([]providerTransactionRecord, 0, len(lines))
	seen := make(map[string]bool, len(lines))
	for index, line := range lines {
		if index == len(lines)-1 && line == "" {
			continue
		}
		if strings.HasSuffix(line, "\r") {
			line = strings.TrimSuffix(line, "\r")
		}
		if line == "" || strings.ContainsRune(line, '\r') || strings.Count(line, "\t") != 1 {
			return nil, errors.New("record must contain exactly one tab-delimited name and provides field")
		}
		name, provides, _ := strings.Cut(line, "\t")
		_, concreteName, identityErr := archrepo.Split(name)
		if identityErr != nil || seen[concreteName] {
			return nil, fmt.Errorf("invalid package name %q", name)
		}
		seen[concreteName] = true
		record := providerTransactionRecord{Name: name}
		if provides != "" {
			for _, provided := range strings.Split(provides, " ") {
				if provided == "" {
					return nil, errors.New("provides field contains unexpected whitespace")
				}
				if _, err := aurmeta.ParseProvide(provided); err != nil {
					return nil, fmt.Errorf("invalid provides token %q", provided)
				}
				record.Provides = append(record.Provides, provided)
			}
		}
		records = append(records, record)
	}
	return records, nil
}

// OfficialTransaction materializes pacman's current transaction for exact
// concrete package targets without performing it or allowing interaction.
func (r Resolver) OfficialTransaction(ctx context.Context, packages []string) ([]string, error) {
	transaction, err := archrepo.Transaction(ctx, r.Runner, packages)
	if err != nil {
		return nil, &QueryError{Err: err}
	}
	return transaction, nil
}

func providesName(values []string, want string) bool {
	for _, provided := range values {
		if aurmeta.DependencyName(provided) == want {
			return true
		}
	}
	return false
}

// CompareVersions delegates to Arch's supported package version comparator.
func (r Resolver) CompareVersions(ctx context.Context, left, right string) (int, error) {
	result, err := r.Runner.Run(ctx, run.Spec{Name: "vercmp", Args: []string{left, right}})
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(result.Stdout)
	if len(fields) != 1 {
		return 0, errors.New("vercmp returned ambiguous output")
	}
	comparison, err := strconv.Atoi(fields[0])
	if err != nil || comparison < -1 || comparison > 1 {
		return 0, errors.New("vercmp returned invalid output")
	}
	return comparison, nil
}

func (r Resolver) Flatpak(ctx context.Context, id string) (bool, error) {
	data, status, err := r.getBytes(ctx, "https://flathub.org/api/v2/appstream/"+url.PathEscape(id))
	if err != nil {
		return false, err
	}
	var response struct {
		ID     string `json:"id"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return false, fmt.Errorf("malformed Flathub response: %w", err)
	}
	// Flathub also returns this 404 for missing AppStream metadata and EOL
	// entries. It does not prove that the exact Flatpak ref is absent.
	if status == http.StatusNotFound && response.Detail == "App not found" {
		return false, errors.New("Flathub has no current application metadata; exact availability could not be confirmed")
	}
	if status != http.StatusOK || response.ID != id {
		return false, errors.New("Flathub query returned an invalid response")
	}
	return true, nil
}

func (r Resolver) getJSON(ctx context.Context, endpoint string, target any) (int, error) {
	data, status, err := r.getBytes(ctx, endpoint)
	if err != nil || status == http.StatusNotFound {
		return status, err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return status, fmt.Errorf("malformed remote JSON: %w", err)
	}
	return status, nil
}

func (r Resolver) getBytes(ctx context.Context, endpoint string) ([]byte, int, error) {
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return errors.New("unexpected metadata redirect; refusing to change source")
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "ops/1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return nil, resp.StatusCode, fmt.Errorf("service returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(data) > 2*1024*1024 {
		return nil, resp.StatusCode, errors.New("service response exceeds size limit")
	}
	return data, resp.StatusCode, nil
}
