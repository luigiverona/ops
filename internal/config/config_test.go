package config

import (
	"errors"
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
		"version=2\npacman=[\"pkg\"]\naur=[\"pkg\"]",
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

func TestDocumentedConfigurationExamples(t *testing.T) {
	for _, path := range []string{"../../README.md", "../../docs/configuration.md"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		blocks := strings.Split(string(data), "```toml\n")[1:]
		if len(blocks) == 0 {
			t.Fatalf("no configuration example in %s", path)
		}
		for _, block := range blocks {
			body, _, ok := strings.Cut(block, "```")
			if !ok {
				t.Fatalf("unclosed TOML example in %s", path)
			}
			_, err := Parse([]byte(body))
			if strings.HasPrefix(body, "version = 1\n") && strings.Contains(path, "configuration.md") {
				if err == nil || !strings.Contains(err.Error(), "migrate") {
					t.Fatal("legacy migration behavior drifted")
				}
			} else if err != nil {
				t.Fatalf("invalid current example in %s: %v", path, err)
			}
		}
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
	for _, data := range []string{"version = [", "pacman=[]", "version=3", "version=2\npacman=[1]", "version=2\npacman=[\"\"]", "version=2\n[unknown]"} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatalf("accepted %q", data)
		}
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
	before, err := os.Stat(path)
	if err != nil {
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
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("existing configuration metadata changed: %v", err)
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

func TestFormatDiagnostics(t *testing.T) {
	tests := []struct {
		name, data, want string
	}{
		{"missing field", "pacman=[]", "missing version field"},
		{"legacy", "version=1\n[apps]\nbrowser=[]", "manually migrate"},
		{"zero", "version=0", "unsupported apps.toml format 0 in the version field"},
		{"negative", "version=-1", "unsupported apps.toml format -1 in the version field"},
		{"future", "version=3", "unsupported future apps.toml format 3"},
		{"far future", "version=9223372036854775807", "unsupported future apps.toml format 9223372036854775807"},
		{"string", "version=\"2\"", "expected an integer format number"},
		{"float", "version=2.0", "expected an integer format number"},
		{"boolean", "version=true", "expected an integer format number"},
		{"array", "version=[2]", "expected an integer format number"},
		{"table", "[version]", "expected an integer format number"},
		{"unknown field", "version=2\nunknown=[]", "invalid apps.toml format 2"},
		{"wrong list type", "version=2\npacman=[1]", "invalid apps.toml format 2"},
		{"malformed TOML", "version=[", "invalid apps.toml syntax"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.data))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse(%q) = %v, want %q", test.data, err, test.want)
			}
			if strings.Contains(err.Error(), "migrate") != (test.name == "legacy") {
				t.Fatalf("incorrect migration guidance: %v", err)
			}
			if strings.Contains(test.name, "future") {
				for _, want := range []string{"installed ops binary supports format 2", "check for a compatible ops release", "do not simply change the version field"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("missing future-format guidance %q: %v", want, err)
					}
				}
			}
			if test.name == "legacy" && !strings.Contains(err.Error(), DocumentationURL+"#migration-from-format-1") {
				t.Fatalf("missing binary-installation documentation link: %v", err)
			}
		})
	}
}

func TestLoadDiagnosticsIncludePathAndPreserveFile(t *testing.T) {
	path := Path(t.TempDir())
	for _, data := range []string{"", "version=1", "version=0", "version=3", "version=\"2\"", "version=2\nunknown=[]", "version=["} {
		if data != "" {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("missing path: %v", err)
		}
		if data == "" {
			if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "is missing; create a file using format 2") {
				t.Fatalf("missing-file diagnostic: %v", err)
			}
		} else if got, err := os.ReadFile(path); err != nil || string(got) != data {
			t.Fatalf("Load changed configuration: %q, %v", got, err)
		}
	}
}

func TestDefaultCommentsDoNotChangeDeclarations(t *testing.T) {
	minimal, err := Parse([]byte("version=2"))
	if err != nil {
		t.Fatal(err)
	}
	generated, err := Parse([]byte(Default))
	if err != nil || !reflect.DeepEqual(generated, minimal) {
		t.Fatalf("default differs from empty format 2: %#v, %v", generated, err)
	}
}
