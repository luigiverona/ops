package archrepo

import (
	"fmt"
	"strings"
)

// OfficialConfig filters pacman-conf's fully expanded output, preserving global
// policy, mirrors and signature settings. Includes must already be expanded so
// a later include cannot reintroduce a custom repository.
func OfficialConfig(output string) (string, bool, error) {
	var filtered strings.Builder
	seen := map[string]bool{}
	section := ""
	custom := false
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if line == "" || strings.ContainsAny(line, "\r\x00\x1b") || strings.TrimSpace(line) != line {
			return "", false, fmt.Errorf("malformed expanded pacman configuration")
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") || strings.Count(line, "[") != 1 || strings.Count(line, "]") != 1 {
				return "", false, fmt.Errorf("malformed expanded pacman repository")
			}
			section = line[1 : len(line)-1]
			if section == "" || seen[section] || (!seen["options"] && section != "options") {
				return "", false, fmt.Errorf("duplicate or misplaced pacman configuration section")
			}
			seen[section] = true
			custom = custom || (section != "options" && !Official(section))
		} else {
			key, _, _ := strings.Cut(line, "=")
			if section == "" || strings.TrimSpace(key) == "Include" {
				return "", false, fmt.Errorf("pacman configuration was not fully expanded")
			}
		}
		if section == "options" || Official(section) {
			filtered.WriteString(line + "\n")
		}
	}
	if !seen["core"] || !seen["extra"] {
		return "", false, fmt.Errorf("required official core/extra repositories are not configured")
	}
	return filtered.String(), custom, nil
}
