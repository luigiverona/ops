// Package resolve performs exact, read-only package resolution.
package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
)

// Resolver resolves official, AUR, and Flathub identifiers without fallback.
type Resolver struct {
	Runner run.Runner
	Client *http.Client
}

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
