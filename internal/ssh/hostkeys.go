package ssh

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	managedMarker       = "# Managed by ops. Manual changes may be replaced."
	managedIncludeStart = "# BEGIN ops managed GitHub configuration"
	managedIncludeEnd   = "# END ops managed GitHub configuration"
	githubMetadataURL   = "https://api.github.com/meta"
	metadataLimit       = 1 << 20
)

type githubMetadata struct {
	SSHKeys []string `json:"ssh_keys"`
}

func (m Manager) fetchGitHubHostKeys(ctx context.Context) ([]string, error) {
	endpoint := m.MetadataURL
	if endpoint == "" {
		endpoint = githubMetadataURL
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "api.github.com" {
			return nil, errors.New("invalid built-in GitHub metadata endpoint")
		}
	}
	client := m.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "ops/1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub metadata returned HTTP %s", resp.Status)
	}
	reader := io.LimitReader(resp.Body, metadataLimit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if len(data) > metadataLimit {
		return nil, errors.New("GitHub metadata exceeds size limit")
	}
	var metadata githubMetadata
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&metadata); err != nil {
		return nil, fmt.Errorf("parse GitHub metadata: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return nil, err
	}
	return validateHostKeys(metadata.SSHKeys)
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("parse trailing GitHub metadata: %w", err)
	}
	return errors.New("GitHub metadata contains multiple JSON values")
}

func validateHostKeys(values []string) ([]string, error) {
	seen := make(map[string]bool)
	keys := make([]string, 0, len(values))
	ed25519 := false
	for _, value := range values {
		fields := strings.Fields(value)
		if len(fields) != 2 || !allowedHostKeyType(fields[0]) {
			return nil, errors.New("GitHub metadata contains a malformed or unsupported SSH host key")
		}
		line := fields[0] + " " + fields[1]
		if _, err := PublicFingerprint(line); err != nil {
			return nil, fmt.Errorf("GitHub metadata contains invalid SSH host-key material: %w", err)
		}
		if seen[line] {
			return nil, errors.New("GitHub metadata contains a duplicate SSH host key")
		}
		seen[line] = true
		ed25519 = ed25519 || fields[0] == "ssh-ed25519"
		keys = append(keys, line)
	}
	if len(keys) == 0 || !ed25519 {
		return nil, errors.New("GitHub metadata does not contain the required SSH host keys")
	}
	sort.Strings(keys)
	return keys, nil
}

func allowedHostKeyType(value string) bool {
	return value == "ssh-ed25519" || value == "ssh-rsa" || value == "ecdsa-sha2-nistp256"
}

func renderKnownHosts(keys []string) []byte {
	var b strings.Builder
	b.WriteString(managedMarker)
	b.WriteByte('\n')
	for _, key := range keys {
		b.WriteString("github.com ")
		b.WriteString(key)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func validManagedKnownHosts(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(io.LimitReader(f, metadataLimit+1))
	if !scanner.Scan() || scanner.Text() != managedMarker {
		return false
	}
	count := 0
	ed25519 := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "github.com" || !allowedHostKeyType(fields[1]) {
			return false
		}
		if _, err := PublicFingerprint(fields[1] + " " + fields[2]); err != nil {
			return false
		}
		ed25519 = ed25519 || fields[1] == "ssh-ed25519"
		count++
	}
	return scanner.Err() == nil && count > 0 && ed25519
}
