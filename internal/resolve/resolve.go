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
		return plan.Package{}, false, nil
	}
	fields := parsePacmanInfo(result.Stdout)
	if fields["Name"] != name {
		return plan.Package{}, false, nil
	}
	return plan.Package{Name: name, Repository: fields["Repository"], Optional: splitPacmanList(fields["Optional Deps"]), Conflicts: splitPacmanList(fields["Conflicts With"])}, true, nil
}

func (r Resolver) AUR(ctx context.Context, name string) (plan.Package, bool, error) {
	var response struct {
		ResultCount int `json:"resultcount"`
		Results     []struct {
			Name, PackageBase string
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
	return plan.Package{Name: p.Name, PackageBase: p.PackageBase, Optional: p.OptDepends, Conflicts: p.Conflicts}, true, nil
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
