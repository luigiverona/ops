// Package config loads and validates the user application declaration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/luigiverona/ops/internal/aurmeta"

	"github.com/pelletier/go-toml/v2"
)

// Version is the supported apps.toml format, independent of the ops program version.
const Version = 2

// DocumentationURL is usable from binary-only installations.
const DocumentationURL = "https://github.com/luigiverona/ops/blob/main/docs/configuration.md"

const Default = `# Applications managed by ops.
# Use exact, case-sensitive identifiers from the selected source.
#
# Official Arch packages (pacman):
#   https://archlinux.org/packages/
#   pacman -Ss SEARCH_TERM
#
# AUR packages (aur):
#   https://aur.archlinux.org/
#
# Flatpak application IDs (flatpak):
#   https://flathub.org/
#   flatpak search SEARCH_TERM

# apps.toml format version. Independent of the ops program version.
version = 2

pacman = []
aur = []
flatpak = []
`

type rawConfig struct {
	Version *int     `toml:"version"`
	Pacman  []string `toml:"pacman"`
	AUR     []string `toml:"aur"`
	Flatpak []string `toml:"flatpak"`
}

// Source is an exact package source. No fallback is permitted.
type Source string

const (
	Pacman  Source = "pacman"
	AUR     Source = "aur"
	Flatpak Source = "flatpak"
)

// Application is a validated, source-qualified declaration.
type Application struct {
	Source     Source
	Identifier string
}

type Config struct {
	Version      int
	Applications []Application
}

// ValidIdentifier rejects paths, options, versions, refs and fuzzy names.
func ValidIdentifier(source Source, id string) bool {
	switch source {
	case Pacman, AUR:
		return id != "." && id != ".." && aurmeta.ValidPackageName(id)
	case Flatpak:
		return len(id) <= 255 && flatpakID.MatchString(id)
	}
	return false
}

var flatpakID = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+\.[A-Za-z_][A-Za-z0-9_-]*$`)

// Path returns the canonical configuration path for home.
func Path(home string) string { return filepath.Join(home, ".config", "ops", "apps.toml") }

// Load reads and strictly validates a configuration file.
func Load(path string) (cfg Config, err error) {
	defer func() {
		if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("apps.toml %q is missing; create a file using format 2 (version = 2); see %s: %w", path, DocumentationURL, err)
		} else if err != nil {
			err = fmt.Errorf("apps.toml %q: %w", path, err)
		}
	}()
	if err := validateConfigPath(path, false); err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	return Parse(b)
}

// Parse validates the format before decoding source lists and never rewrites files.
func Parse(data []byte) (Config, error) {
	var header map[string]any
	if err := toml.Unmarshal(data, &header); err != nil {
		return Config{}, fmt.Errorf("invalid apps.toml syntax: %w", err)
	}
	value, present := header["version"]
	if !present {
		return Config{}, fmt.Errorf("missing version field; the installed ops binary supports apps.toml format 2 (version = 2); check the file's format before adding the field; see %s", DocumentationURL)
	}
	format, ok := value.(int64)
	if !ok {
		return Config{}, errors.New("invalid apps.toml version field: expected an integer format number; the installed ops binary supports format 2")
	}
	switch {
	case format == 1:
		return Config{}, fmt.Errorf("apps.toml format 1 is no longer supported; manually migrate categories and source-prefixed identifiers to format 2 source lists; see %s#migration-from-format-1; configuration was not changed", DocumentationURL)
	case format < Version:
		return Config{}, fmt.Errorf("unsupported apps.toml format %d in the version field; the installed ops binary supports format 2; check the file's format against %s; configuration was not changed", format, DocumentationURL)
	case format > Version:
		return Config{}, fmt.Errorf("unsupported future apps.toml format %d; the installed ops binary supports format 2; check for a compatible ops release; do not simply change the version field; configuration was not changed", format)
	}
	var raw rawConfig
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("invalid apps.toml format 2: %w", err)
	}
	cfg := Config{Version: Version}
	seen := make(map[Application]bool)
	packageSources := make(map[string]Source)
	for _, group := range []struct {
		source Source
		ids    []string
	}{
		{Pacman, raw.Pacman}, {AUR, raw.AUR}, {Flatpak, raw.Flatpak},
	} {
		ids := append([]string(nil), group.ids...)
		sort.Strings(ids)
		for _, id := range ids {
			app := Application{Source: group.source, Identifier: id}
			if !ValidIdentifier(app.Source, id) {
				return Config{}, fmt.Errorf("invalid apps.toml format 2: malformed %s identifier %q", app.Source, id)
			}
			if seen[app] {
				return Config{}, fmt.Errorf("invalid apps.toml format 2: duplicate declaration %s:%s", app.Source, id)
			}
			seen[app] = true
			if app.Source != Flatpak {
				if source, ok := packageSources[id]; ok && source != app.Source {
					return Config{}, fmt.Errorf("invalid apps.toml format 2: package %q cannot be both pacman and AUR", id)
				}
				packageSources[id] = app.Source
			}
			cfg.Applications = append(cfg.Applications, app)
		}
	}
	return cfg, nil
}

// EnsureDefault creates the default configuration with private-by-default
// directory permissions. An existing file is always preserved.
func EnsureDefault(path string) (bool, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return false, fmt.Errorf("create configuration directory: %w", err)
	}
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("configuration directory is not a safe regular directory")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return false, fmt.Errorf("create configuration directory: %w", err)
		}
	} else {
		return false, fmt.Errorf("inspect configuration directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return false, errors.New("configuration file is not a safe regular file")
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect configuration: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("create configuration: %w", err)
	}
	if _, err := f.WriteString(Default); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("write configuration: %w", err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("close configuration: %w", err)
	}
	return true, nil
}

func validateConfigPath(path string, allowMissing bool) error {
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if err != nil {
		if allowMissing && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect configuration directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("configuration directory is not a safe regular directory")
	}
	info, err = os.Lstat(path)
	if err != nil {
		if allowMissing && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect configuration: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("configuration file is not a safe regular file")
	}
	return nil
}
