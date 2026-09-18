// Package flatpak manages only user-scoped Flatpak state.
package flatpak

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/run"
)

const FlathubURL = "https://dl.flathub.org/repo/flathub.flatpakrepo"
const FlathubRepositoryURL = "https://dl.flathub.org/repo/"

type Remote struct {
	Name    string
	URL     string
	Enabled bool
	Options []string
	// SourceTrusted is established from persistent configuration and the pinned
	// per-remote keyring. CLI columns alone never establish this evidence.
	SourceTrusted bool
}

func (r Remote) Canonical() bool {
	if r.Name != "flathub" || r.URL != FlathubRepositoryURL || !r.SourceTrusted {
		return false
	}
	for _, option := range r.Options {
		if option != "disabled" {
			return false
		}
	}
	return true
}
func (r Remote) Ready() bool { return r.Canonical() && r.Enabled }

type Manager struct{ Runner run.Runner }

var remoteName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// Use Flatpak's C-locale JSON column output: unlike its unescaped tab table,
// JSON preserves record boundaries even for hostile remote URLs.
func ParseRemotes(output string) (map[string]Remote, error) {
	remotes := map[string]Remote{}
	rows, err := parseRows(output, []string{"options", "name", "url"})
	if err != nil {
		return nil, err
	}
	for _, fields := range rows {
		if len(fields) != 3 || !remoteName.MatchString(fields[1]) {
			return nil, fmt.Errorf("malformed Flatpak remote record")
		}
		parsed, err := url.Parse(fields[2])
		if err != nil || parsed.Scheme == "" || strings.ContainsAny(fields[2], " \r\n\x1b") {
			return nil, fmt.Errorf("malformed Flatpak remote URL")
		}
		if _, exists := remotes[fields[1]]; exists {
			return nil, fmt.Errorf("duplicate Flatpak remote")
		}
		remote := Remote{Name: fields[1], URL: fields[2], Enabled: true}
		if fields[0] != "" {
			seen := map[string]bool{}
			for _, option := range strings.Split(fields[0], ",") {
				switch option {
				case "disabled", "oci", "no-enumerate", "no-gpg-verify", "filtered":
				default:
					return nil, fmt.Errorf("unknown Flatpak remote option")
				}
				if seen[option] {
					return nil, fmt.Errorf("duplicate Flatpak remote option")
				}
				seen[option] = true
				remote.Options = append(remote.Options, option)
				if option == "disabled" {
					remote.Enabled = false
				}
			}
		}
		remotes[remote.Name] = remote
	}
	return remotes, nil
}
func ParseApplications(output string) (map[string]string, error) {
	apps := map[string]string{}
	// Unlike remotes (which prints []), flatpak list emits no bytes for an
	// empty installation, including in JSON mode. A failed command never gets
	// here: Applications checks its exit status before parsing.
	if output == "" {
		return apps, nil
	}
	rows, err := parseRows(output, []string{"origin", "application_id"})
	if err != nil {
		return nil, err
	}
	for _, fields := range rows {
		if len(fields) != 2 || !remoteName.MatchString(fields[0]) || !config.ValidIdentifier(config.Flatpak, fields[1]) {
			return nil, fmt.Errorf("malformed Flatpak application origin record")
		}
		if _, exists := apps[fields[1]]; exists {
			return nil, fmt.Errorf("ambiguous Flatpak application ID (multiple refs or origins)")
		}
		apps[fields[1]] = fields[0]
	}
	return apps, nil
}

// Reject missing, duplicate, unknown and non-string fields, duplicate keys,
// null inventories and trailing data. Decoding into a map alone loses duplicates.
func parseRows(output string, keys []string) ([][]string, error) {
	decoder := json.NewDecoder(strings.NewReader(output))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return nil, fmt.Errorf("invalid Flatpak JSON inventory")
	}
	var rows [][]string
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil || token != json.Delim('{') {
			return nil, fmt.Errorf("invalid Flatpak JSON record")
		}
		fields := map[string]string{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return nil, fmt.Errorf("invalid Flatpak JSON field")
			}
			allowed := false
			for _, want := range keys {
				allowed = allowed || key == want
			}
			if _, exists := fields[key]; exists || !allowed {
				return nil, fmt.Errorf("duplicate or unknown Flatpak JSON field")
			}
			valueToken, err := decoder.Token()
			value, ok := valueToken.(string)
			if err != nil || !ok {
				return nil, fmt.Errorf("invalid Flatpak JSON value")
			}
			fields[key] = value
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim('}') || len(fields) != len(keys) {
			return nil, fmt.Errorf("incomplete Flatpak JSON record")
		}
		row := make([]string, len(keys))
		for i, key := range keys {
			row[i] = fields[key]
		}
		rows = append(rows, row)
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim(']') {
		return nil, fmt.Errorf("incomplete Flatpak JSON inventory")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing Flatpak JSON data")
	}
	return rows, nil
}
func (m Manager) Remotes(ctx context.Context) (map[string]Remote, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := m.Runner.Run(ctx, run.Spec{Name: "flatpak", Args: []string{"remotes", "--user", "--show-disabled", "--columns=options,name,url", "--json"}, ReadOnlyFilesystem: true, FailureOutput: run.FailureStderr})
	if err != nil {
		return nil, err
	}
	remotes, err := ParseRemotes(result.Stdout)
	if err != nil {
		return nil, err
	}
	if err := inspectRemoteIdentity(remotes); err != nil {
		return nil, fmt.Errorf("inspect Flathub source/trust configuration: %w", err)
	}
	return remotes, nil
}
func (m Manager) Applications(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := m.Runner.Run(ctx, run.Spec{Name: "flatpak", Args: []string{"list", "--user", "--app", "--columns=origin,application", "--json"}, ReadOnlyFilesystem: true, FailureOutput: run.FailureStderr})
	if err != nil {
		return nil, err
	}
	return ParseApplications(result.Stdout)
}
func (m Manager) VerifyFlathub(ctx context.Context) error {
	remotes, err := m.Remotes(ctx)
	if err != nil {
		return err
	}
	if !remotes["flathub"].Ready() {
		return fmt.Errorf("user flathub remote lacks the supported enabled Flathub source/trust identity; reconcile manually and rerun ops")
	}
	return nil
}
func (m Manager) AddFlathub(ctx context.Context) error {
	remotes, err := m.Remotes(ctx)
	if err != nil {
		return err
	}
	if _, exists := remotes["flathub"]; exists {
		return fmt.Errorf("flathub remote changed after planning; rerun ops")
	}
	_, err = m.Runner.Run(ctx, run.Spec{Name: "flatpak", Args: []string{"remote-add", "--user", "flathub", FlathubURL}, FailureOutput: run.FailureStderr})
	if err != nil {
		return err
	}
	return m.VerifyFlathub(ctx)
}
func (m Manager) EnableFlathub(ctx context.Context) error {
	remotes, err := m.Remotes(ctx)
	if err != nil {
		return err
	}
	if !remotes["flathub"].Canonical() || remotes["flathub"].Enabled {
		return fmt.Errorf("flathub source changed after planning; rerun ops")
	}
	_, err = m.Runner.Run(ctx, run.Spec{Name: "flatpak", Args: []string{"remote-modify", "--user", "--enable", "flathub"}, FailureOutput: run.FailureStderr})
	if err != nil {
		return err
	}
	return m.VerifyFlathub(ctx)
}
func (m Manager) Install(ctx context.Context, id string) error {
	if !config.ValidIdentifier(config.Flatpak, id) {
		return fmt.Errorf("invalid Flatpak application ID")
	}
	if err := m.VerifyFlathub(ctx); err != nil {
		return err
	}
	apps, err := m.Applications(ctx)
	if err != nil {
		return err
	}
	if origin := apps[id]; origin != "" {
		if origin != "flathub" {
			return fmt.Errorf("installed app has a different origin; reconcile manually and rerun ops")
		}
		return m.Verify(ctx, id)
	}
	// Application inventory is a separate process; repeat the complete trust
	// check immediately before mutation in case it changed during that query.
	if err := m.VerifyFlathub(ctx); err != nil {
		return err
	}
	_, err = m.Runner.Run(ctx, run.Spec{Name: "flatpak", Args: []string{"install", "--user", "--noninteractive", "--", "flathub", id}, FailureOutput: run.FailureCombined})
	if err != nil {
		return err
	}
	return m.Verify(ctx, id)
}
func (m Manager) Verify(ctx context.Context, id string) error {
	if err := m.VerifyFlathub(ctx); err != nil {
		return err
	}
	apps, err := m.Applications(ctx)
	if err != nil {
		return err
	}
	if apps[id] != "flathub" {
		return fmt.Errorf("Flatpak application %s is not installed from validated user Flathub", id)
	}
	return nil
}
