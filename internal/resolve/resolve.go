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

	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
)

// Resolver resolves official, AUR, and Flathub identifiers without fallback.
type Resolver struct {
	Runner run.Runner
	Client *http.Client
}

var gitObject = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)
var packageName = regexp.MustCompile(`^[A-Za-z0-9@._+][A-Za-z0-9@._+-]*$`)

func (r Resolver) Pacman(ctx context.Context, name string) (plan.Package, bool, error) {
	result, err := r.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Si", "--", name}})
	if err != nil {
		message := strings.ToLower(result.Stderr)
		if strings.Contains(message, "target not found") || strings.Contains(message, "was not found") {
			return r.archPackage(ctx, name)
		}
		return plan.Package{}, false, err
	}
	fields := parsePacmanInfo(result.Stdout)
	if fields["Name"] != name {
		return r.archPackage(ctx, name)
	}
	return plan.Package{Name: name, Repository: fields["Repository"], Optional: splitPacmanList(fields["Optional Deps"]), Required: splitPacmanList(fields["Depends On"]), Conflicts: splitPacmanList(fields["Conflicts With"])}, true, nil
}

func (r Resolver) archPackage(ctx context.Context, name string) (plan.Package, bool, error) {
	var response struct {
		Valid   bool `json:"valid"`
		Results []struct {
			Name         string   `json:"pkgname"`
			Repository   string   `json:"repo"`
			Architecture string   `json:"arch"`
			Depends      []string `json:"depends"`
			Optional     []string `json:"optdepends"`
			Conflicts    []string `json:"conflicts"`
		} `json:"results"`
	}
	endpoint := "https://archlinux.org/packages/search/json/?name=" + url.QueryEscape(name) + "&arch=x86_64"
	status, err := r.getJSON(ctx, endpoint, &response)
	if err != nil {
		return plan.Package{}, false, err
	}
	if status != http.StatusOK || !response.Valid {
		return plan.Package{}, false, nil
	}
	for _, result := range response.Results {
		if result.Name != name || (result.Architecture != "x86_64" && result.Architecture != "any") {
			continue
		}
		switch result.Repository {
		case "core", "extra", "multilib":
		default:
			continue
		}
		return plan.Package{Name: name, Repository: result.Repository, Required: result.Depends, Optional: result.Optional, Conflicts: result.Conflicts}, true, nil
	}
	return plan.Package{}, false, nil
}

func (r Resolver) AUR(ctx context.Context, name string) (plan.Package, bool, error) {
	var response struct {
		ResultCount int `json:"resultcount"`
		Results     []struct {
			Name, PackageBase string
			Depends           []string
			OptDepends        []string
			Conflicts         []string
		} `json:"results"`
	}
	endpoint := "https://aur.archlinux.org/rpc/v5/info?arg%5B%5D=" + url.QueryEscape(name)
	status, err := r.getJSON(ctx, endpoint, &response)
	if err != nil {
		return plan.Package{}, false, err
	}
	if status == http.StatusNotFound || response.ResultCount != 1 || response.Results[0].Name != name {
		return plan.Package{}, false, nil
	}
	p := response.Results[0]
	return plan.Package{Name: p.Name, PackageBase: p.PackageBase, Required: p.Depends, Optional: p.OptDepends, Conflicts: p.Conflicts}, true, nil
}

// AURSource pins .SRCINFO to the exact AUR Git commit that will be reviewed.
func (r Resolver) AURSource(ctx context.Context, name string) (plan.AURSource, bool, error) {
	if name == "" || strings.ContainsAny(name, "/\\\x00\r\n") {
		return plan.AURSource{}, false, errors.New("invalid AUR package base")
	}
	repository := "https://aur.archlinux.org/" + name + ".git"
	result, err := r.Runner.Run(ctx, run.Spec{Name: "git", Args: []string{"ls-remote", repository, "HEAD"}})
	if err != nil {
		return plan.AURSource{}, false, err
	}
	commits := make(map[string]bool)
	for _, line := range strings.Split(result.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "HEAD" && gitObject.MatchString(fields[0]) {
			commits[strings.ToLower(fields[0])] = true
		}
	}
	if len(commits) != 1 {
		return plan.AURSource{}, false, errors.New("AUR HEAD did not resolve to exactly one commit")
	}
	var commit string
	for value := range commits {
		commit = value
	}
	endpoint := "https://aur.archlinux.org/cgit/aur.git/plain/.SRCINFO?h=" + url.QueryEscape(name) + "&id=" + url.QueryEscape(commit)
	data, status, err := r.getBytes(ctx, endpoint)
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

// OfficialDependency asks pacman whether a dependency is already satisfied and,
// if not, materializes the concrete package selected by pacman's own resolver.
func (r Resolver) OfficialDependency(ctx context.Context, requirement string) (plan.OfficialDependency, error) {
	binding := plan.OfficialDependency{Requirement: requirement}
	result, err := r.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-T", "--", requirement}})
	if err == nil {
		if strings.TrimSpace(result.Stdout) != "" {
			return binding, errors.New("pacman dependency test returned contradictory output")
		}
		binding.Satisfied = true
		return binding, nil
	}
	if strings.TrimSpace(result.Stdout) != requirement {
		return binding, fmt.Errorf("inspect installed dependency: %w", err)
	}
	format := "%n\t%P"
	result, err = r.Runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Sp", "--needed", "--noconfirm", "--print-format", format, "--", requirement}})
	if err != nil {
		return binding, err
	}
	want := aurmeta.DependencyName(requirement)
	if want == "" {
		return binding, fmt.Errorf("invalid dependency requirement %q", requirement)
	}
	candidates := make(map[string]bool)
	packages := make(map[string]bool)
	records, err := parseProviderTransaction(result.Stdout)
	if err != nil {
		return binding, fmt.Errorf("pacman returned invalid transaction metadata for %q: %w", requirement, err)
	}
	for _, record := range records {
		if packages[record.Name] {
			return binding, fmt.Errorf("pacman returned invalid or duplicate transaction metadata for %q", requirement)
		}
		packages[record.Name] = true
		if record.Name == want || providesName(record.Provides, want) {
			candidates[record.Name] = true
		}
	}
	if len(candidates) != 1 {
		return binding, fmt.Errorf("pacman selected %d concrete providers for %q", len(candidates), requirement)
	}
	for name := range candidates {
		binding.Provider = name
	}
	for name := range packages {
		binding.Packages = append(binding.Packages, name)
	}
	sort.Strings(binding.Packages)
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
		if !packageName.MatchString(name) || seen[name] {
			return nil, fmt.Errorf("invalid package name %q", name)
		}
		seen[name] = true
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
	if len(packages) == 0 {
		return nil, nil
	}
	args := []string{"-Sp", "--needed", "--noconfirm", "--print-format", "%n", "--"}
	args = append(args, packages...)
	result, err := r.Runner.Run(ctx, run.Spec{Name: "pacman", Args: args})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var transaction []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if !packageName.MatchString(name) || seen[name] {
			return nil, errors.New("pacman returned invalid or duplicate concrete transaction metadata")
		}
		seen[name] = true
		transaction = append(transaction, name)
	}
	sort.Strings(transaction)
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
	status, err := r.getJSON(ctx, "https://flathub.org/api/v2/appstream/"+url.PathEscape(id), &struct{}{})
	if err != nil {
		return false, err
	}
	return status == http.StatusOK, nil
}

func (r Resolver) getJSON(ctx context.Context, endpoint string, target any) (int, error) {
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "ops/1")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("service returned HTTP %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

func (r Resolver) getBytes(ctx context.Context, endpoint string) ([]byte, int, error) {
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
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
	if resp.StatusCode == http.StatusNotFound {
		return nil, resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("service returned HTTP %s", resp.Status)
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

func parsePacmanInfo(output string) map[string]string {
	fields := make(map[string]string)
	var key string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, " ") && key != "" {
			fields[key] += "\n" + strings.TrimSpace(line)
			continue
		}
		left, right, ok := strings.Cut(line, ":")
		if ok {
			key = strings.TrimSpace(left)
			fields[key] = strings.TrimSpace(right)
		}
	}
	return fields
}

func splitPacmanList(value string) []string {
	if value == "" || value == "None" {
		return nil
	}
	return strings.Split(value, "\n")
}
