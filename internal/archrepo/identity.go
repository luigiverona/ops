// Package archrepo defines the official Arch source contract used by ops.
package archrepo

import (
	"fmt"
	"strings"

	"github.com/luigiverona/ops/internal/aurmeta"
)

// Repositories returns the supported stable official repositories in priority order.
func Repositories() []string { return []string{"core", "extra", "multilib"} }
func Official(repo string) bool {
	for _, allowed := range Repositories() {
		if repo == allowed {
			return true
		}
	}
	return false
}

// Target is represented as repo/name at every transaction boundary. Bare names
// are deliberately invalid, including when there is only one configured repo.
func Split(target string) (repo, name string, err error) {
	repo, name, ok := strings.Cut(target, "/")
	if !ok || !Official(repo) || !aurmeta.ValidPackageName(name) {
		return "", "", fmt.Errorf("invalid official package target %q", target)
	}
	return repo, name, nil
}

// ParseInfo reads pacman's C-locale information records. Duplicate fields (in
// particular a second package record) are invalid, not last-value-wins.
func ParseInfo(output string) (map[string]string, error) {
	fields := map[string]string{}
	seen := map[string]bool{}
	key := ""
	for _, line := range strings.Split(output, "\n") {
		if strings.ContainsAny(line, "\r\x00\x1b") {
			return nil, fmt.Errorf("invalid pacman information control character")
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, " ") && key != "" {
			fields[key] += "\n" + strings.TrimSpace(line)
			continue
		}
		left, right, ok := strings.Cut(line, ":")
		left, right = strings.TrimSpace(left), strings.TrimSpace(right)
		if !ok || left == "" || seen[left] {
			return nil, fmt.Errorf("malformed or duplicate pacman information field")
		}
		seen[left] = true
		fields[left], key = right, left
	}
	if !aurmeta.ValidPackageName(fields["Name"]) {
		return nil, fmt.Errorf("missing pacman package identity")
	}
	return fields, nil
}

// Prerequisite identifies the fixed foundational packages on supported Arch.
func Prerequisite(name string) string {
	switch name {
	case "git":
		return "extra/git"
	case "openssh":
		return "core/openssh"
	case "github-cli":
		return "extra/github-cli"
	case "flatpak":
		return "extra/flatpak"
	}
	return ""
}
