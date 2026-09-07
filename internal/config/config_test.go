package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestV2StrictDeclarations(t *testing.T) {
	for _, data := range []string{
		"version=2\npacman=[\"git\",\"git\"]",
		"version=2\naur=[\"pkg\",\"pkg\"]",
		"version=2\nflatpak=[\"org.example.App\",\"org.example.App\"]",
		"version=2\npacman=[\"--help\"]",
		"version=2\npacman=[\"pkg>=2\"]",
		"version=2\naur=[\"../escape\"]",
		"version=2\naur=[\"..\"]",
		"version=2\npacman=[\" git\"]",
		"version=2\nflatpak=[\"org.example.App/stable\"]",
		"version=2\nflatpak=[\"App\"]",
		"version=2\nunknown=[]",
		"version=2\n[apps]\nbrowser=[]",
	} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
	first, err := Parse([]byte("version=2\npacman=[\"z\",\"a\"]\naur=[\"pkg\"]"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Parse([]byte("version=2\naur=[\"pkg\"]\npacman=[\"a\",\"z\"]"))
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("order: %#v %#v %v", first, second, err)
	}
	if _, err := Parse([]byte("version=2")); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte("version=1\n[apps]\nbrowser=[]")); err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("migration: %v", err)
	}
}

func TestInstallerDefaultMatches(t *testing.T) {
	data, err := os.ReadFile("../../script/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(data), "<<'OPS_CONFIG'\n")
	if !ok {
		t.Fatal("installer config marker missing")
	}
	got, _, ok := strings.Cut(rest, "\nOPS_CONFIG")
	if !ok || got+"\n" != Default {
		t.Fatal("installer default differs from config.Default")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg, err := Parse([]byte(Default))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != Version || len(cfg.Applications) != 0 {
		t.Fatalf("unexpected default: %#v", cfg)
	}
}

func TestValidSourcesAndCasing(t *testing.T) {
	data := `version = 2
pacman = ["firefox"]
aur = ["mullvad-vpn-bin"]
flatpak = ["Com.Example.Vault"]
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
		"unsupported version": `version=3
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
