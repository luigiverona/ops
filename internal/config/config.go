// Package config loads and validates the user application declaration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const Version = 1

var Categories = []string{"browser", "vpn", "vault", "mail", "social", "music", "game"}

const Default = `# ops configuration
#
# Define each application using the "source:package" format.
# Supported sources are "pacman", "aur", and "flatpak".
# For pacman and AUR, use the exact package name; for Flatpak, use the exact application ID.
# Add applications under the appropriate category below, and leave unused categories empty.
# ops automatically installs and configures any required dependencies or system prerequisites.
# ops installs declared applications but never removes applications that are no longer listed.
# Applications that cannot be installed are skipped and reported as unresolved when the run finishes.
#
# Example:
#
# [apps]
# browser = ["aur:librewolf-bin", "aur:mullvad-browser-bin"]
# vpn = ["pacman:mullvad-vpn"]
# vault = ["pacman:bitwarden"]
# mail = ["flatpak:com.tutanota.Tutanota"]
# social = ["pacman:discord"]
# music = ["pacman:spotify-launcher"]
# game = ["pacman:steam"]

version = 1

[apps]
browser = []
vpn = []
vault = []
mail = []
social = []
music = []
game = []
`

type rawConfig struct {
	Version *int    `toml:"version"`
	Apps    rawApps `toml:"apps"`
}

type rawApps struct {
	Browser []string `toml:"browser"`
	VPN     []string `toml:"vpn"`
	Vault   []string `toml:"vault"`
	Mail    []string `toml:"mail"`
	Social  []string `toml:"social"`
	Music   []string `toml:"music"`
	Game    []string `toml:"game"`
}

// Application is a validated application declaration.
type Application struct {
	Category   string
	Source     string
	Identifier string
}

// Config is the validated v1 configuration.
type Config struct {
	Version      int
	Applications []Application
}

// Path returns the canonical configuration path for home.
func Path(home string) string { return filepath.Join(home, ".config", "ops", "apps.toml") }

// Load reads and strictly validates a configuration file.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	return Parse(b)
}

// Parse strictly decodes and validates v1 configuration data.
func Parse(data []byte) (Config, error) {
	var raw rawConfig
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	if raw.Version == nil {
		return Config{}, errors.New("invalid configuration: missing version")
	}
	if *raw.Version != Version {
		return Config{}, fmt.Errorf("invalid configuration: unsupported version %d", *raw.Version)
	}

	values := map[string][]string{
		"browser": raw.Apps.Browser, "vpn": raw.Apps.VPN, "vault": raw.Apps.Vault,
		"mail": raw.Apps.Mail, "social": raw.Apps.Social, "music": raw.Apps.Music,
		"game": raw.Apps.Game,
	}
	seen := make(map[string]string)
	cfg := Config{Version: *raw.Version}
	for _, category := range Categories {
		for i, declaration := range values[category] {
			app, err := parseApplication(category, declaration)
			if err != nil {
				return Config{}, fmt.Errorf("invalid configuration: apps.%s[%d]: %w", category, i, err)
			}
			key := app.Source + "\x00" + app.Identifier
			if previous, ok := seen[key]; ok {
				return Config{}, fmt.Errorf("invalid configuration: duplicate declaration %s:%s in %s and %s", app.Source, app.Identifier, previous, category)
			}
			seen[key] = category
			cfg.Applications = append(cfg.Applications, app)
		}
	}
	return cfg, nil
}

func parseApplication(category, declaration string) (Application, error) {
	declaration = strings.TrimSpace(declaration)
	colon := strings.IndexByte(declaration, ':')
	if colon < 0 {
		return Application{}, errors.New("missing ':'")
	}
	source := strings.ToLower(strings.TrimSpace(declaration[:colon]))
	identifier := strings.TrimSpace(declaration[colon+1:])
	if source == "" {
		return Application{}, errors.New("empty source")
	}
	if identifier == "" {
		return Application{}, errors.New("empty identifier")
	}
	switch source {
	case "pacman", "aur", "flatpak":
	default:
		return Application{}, fmt.Errorf("unsupported source %q", source)
	}
	if strings.ContainsAny(identifier, "\x00\r\n") {
		return Application{}, errors.New("identifier contains control characters")
	}
	return Application{Category: category, Source: source, Identifier: identifier}, nil
}

// EnsureDefault creates the default configuration with private-by-default
// directory permissions. An existing file is always preserved.
func EnsureDefault(path string) (bool, error) {
	if _, err := os.Lstat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect configuration: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create configuration directory: %w", err)
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
