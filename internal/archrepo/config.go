package archrepo

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/luigiverona/ops/internal/archtrust"
)

// configuredSections validates the expanded configuration for general upgrades.
// Its filtered output is not evidence of official source identity.
func configuredSections(output string) (string, bool, error) {
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

// ValidateConfigured checks syntax only; a general -Syu intentionally retains
// the complete user repository set, including custom repositories.
func ValidateConfigured(output string) error {
	_, _, err := configuredSections(output)
	return err
}

// OfficialConfig replaces all source and signature policy, including repository
// overrides. No user Server or mirror Include defines official package identity.
// This config must be paired with independently acquired sync databases.
func OfficialConfig(output string) (string, bool, error) {
	if _, _, err := configuredSections(output); err != nil {
		return "", false, err
	}
	var b strings.Builder
	b.WriteString("[options]\nArchitecture = x86_64\nSigLevel = PackageRequired PackageTrustedOnly DatabaseOptional DatabaseTrustedOnly\n")
	section := ""
	multilib := false
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if strings.HasPrefix(line, "[") {
			section = line[1 : len(line)-1]
			multilib = multilib || section == "multilib"
			continue
		}
		if section != "options" {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "Architecture":
			if value != "x86_64" && value != "auto" {
				return "", false, fmt.Errorf("official operations require x86_64")
			}
		case "RootDir", "DBPath", "CacheDir":
			expected := map[string]string{"RootDir": "/", "DBPath": "/var/lib/pacman", "CacheDir": "/var/cache/pacman/pkg"}[key]
			if filepath.Clean(value) != expected {
				return "", false, fmt.Errorf("official operations require standard %s", key)
			}
			b.WriteString(key + " = " + expected + "\n")
		case "SigLevel":
			for _, v := range strings.Fields(value) {
				if v == "Never" || v == "PackageNever" || v == "PackageOptional" || v == "Optional" {
					return "", false, fmt.Errorf("official operations require package signatures")
				}
			}
		case "XferCommand", "NoUpgrade", "NoExtract", "DisableSandbox", "DisableSandboxFilesystem", "DisableSandboxSyscalls":
			return "", false, fmt.Errorf("unsupported official operation policy %s", key)
		case "GPGDir", "HookDir", "LogFile", "DownloadUser", "IgnorePkg", "IgnoreGroup", "HoldPkg", "LocalFileSigLevel", "RemoteFileSigLevel", "ParallelDownloads", "CleanMethod", "UseSyslog", "Color", "NoProgressBar", "CheckSpace", "VerbosePkgLists", "ILoveCandy":
			b.WriteString(line + "\n")
		default:
			return "", false, fmt.Errorf("unsupported official operation option %s", key)
		}
	}
	for _, repo := range Repositories() {
		if repo == "multilib" && !multilib {
			continue
		}
		b.WriteString("[" + repo + "]\nServer = " + archtrust.Endpoint + "/" + repo + "/os/x86_64\n")
	}
	return b.String(), true, nil
}
