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

func configWith(category, declaration string) string {
	var b strings.Builder
	b.WriteString("version=1\n[apps]\n")
	b.WriteString(category + "=[\"" + declaration + "\"]\n")
	return b.String()
}
