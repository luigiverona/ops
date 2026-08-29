package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg, err := Parse([]byte(Default))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 || len(cfg.Applications) != 0 {
		t.Fatalf("unexpected default: %#v", cfg)
	}
}

func TestValidSourcesAndCasing(t *testing.T) {
	data := `version = 1
[apps]
browser = [" pacman:firefox "]
vpn = ["aur:mullvad-vpn-bin"]
vault = ["flatpak:Com.Example.Vault"]
mail=[]
social=[]
music=[]
game=[]
`
	cfg, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Applications[2].Identifier; got != "Com.Example.Vault" {
		t.Fatalf("identifier casing changed: %q", got)
	}
}

func TestInvalidConfigs(t *testing.T) {
	tests := map[string]string{
		"malformed TOML":  `version = [`,
		"missing version": `[apps]`,
		"unsupported version": `version=2
[apps]`,
		"unknown category": `version=1
[apps]
office=[]`,
		"unknown source":   configWith("browser", "snap:firefox"),
		"missing colon":    configWith("browser", "pacman"),
		"empty source":     configWith("browser", ":firefox"),
		"empty identifier": configWith("browser", "pacman: "),
		"duplicate declaration": `version=1
[apps]
browser=["pacman:firefox"]
vpn=[" PACMAN : firefox "]`,
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestEnsureDefaultPreservesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".config", "ops", "apps.toml")
	created, err := EnsureDefault(path)
	if err != nil || !created {
		t.Fatalf("first create = %v, %v", created, err)
	}
	if err := os.WriteFile(path, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err = EnsureDefault(path)
	if err != nil || created {
		t.Fatalf("second create = %v, %v", created, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "mine" {
		t.Fatal("existing configuration was overwritten")
	}
}

func TestConfigurationPathsRejectSymlinksAndNonRegularFiles(t *testing.T) {
	t.Run("ops directory symlink", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		_ = os.Mkdir(filepath.Join(home, ".config"), 0o700)
		_ = os.Symlink(outside, filepath.Join(home, ".config", "ops"))
		path := Path(home)
		if _, err := EnsureDefault(path); err == nil {
			t.Fatal("expected directory symlink rejection")
		}
		if _, err := os.Lstat(filepath.Join(outside, "apps.toml")); !os.IsNotExist(err) {
			t.Fatal("configuration was created outside the expected directory")
		}
	})
	t.Run("apps file symlink", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "ops")
		_ = os.MkdirAll(dir, 0o700)
		outside := filepath.Join(home, "outside")
		_ = os.WriteFile(outside, []byte("keep"), 0o600)
		_ = os.Symlink(outside, Path(home))
		if _, err := EnsureDefault(Path(home)); err == nil {
			t.Fatal("expected file symlink rejection")
		}
		if _, err := Load(Path(home)); err == nil {
			t.Fatal("expected symlinked configuration load rejection")
		}
		data, _ := os.ReadFile(outside)
		if string(data) != "keep" {
			t.Fatal("symlink target was modified")
		}
	})
	t.Run("apps path directory", func(t *testing.T) {
		home := t.TempDir()
		_ = os.MkdirAll(Path(home), 0o700)
		if _, err := EnsureDefault(Path(home)); err == nil {
			t.Fatal("expected non-regular file rejection")
		}
	})
}

func configWith(category, declaration string) string {
	var b strings.Builder
	b.WriteString("version=1\n[apps]\n")
	b.WriteString(category + "=[\"" + declaration + "\"]\n")
	return b.String()
}
