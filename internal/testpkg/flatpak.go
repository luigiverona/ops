package testpkg

import (
	"embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

// FlatpakData contains the two byte-identical native bootstrap observations.
// These are public trust material, not generated private keys.
//
//go:embed flatpakdata/*
var FlatpakData embed.FS

func FlatpakConfig() []byte { b, _ := FlatpakData.ReadFile("flatpakdata/config"); return b }
func FlatpakKeyring() []byte {
	b, _ := FlatpakData.ReadFile("flatpakdata/flathub.trustedkeys.gpg")
	return b
}

// IsolateFlatpakTests gives command fakes a private persistent inventory as well
// as JSON output. Native fixtures override this path with their own t.Setenv.
func IsolateFlatpakTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "ops-flatpak-tests-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Setenv("FLATPAK_USER_DIR", dir); err != nil {
		panic(err)
	}
	return m.Run()
}

// FlatpakRemotes accompanies mocked CLI records with matching on-disk state.
// Callers can then alter that state to exercise hidden configuration drift.
func FlatpakRemotes(output string) run.Result {
	dir := os.Getenv("FLATPAK_USER_DIR")
	if !strings.HasPrefix(filepath.Base(dir), "ops-flatpak-tests-") {
		panic("fake Flatpak state is not isolated")
	}
	var rows []struct{ Name, URL, Options string }
	data := "[core]\nrepo_version=1\nmode=bare-user-only\nmin-free-space-size=500MB\n"
	if json.Unmarshal([]byte(output), &rows) == nil {
		for _, row := range rows {
			if row.Name != "flathub" {
				continue
			}
			data = strings.Replace(string(FlatpakConfig()), "https://dl.flathub.org/repo/", row.URL, 1)
			if strings.Contains(row.Options, "disabled") {
				data += "xa.disable=true\n"
			}
		}
	}
	WriteFlatpakConfig([]byte(data))
	if err := os.WriteFile(filepath.Join(dir, "repo", "flathub.trustedkeys.gpg"), FlatpakKeyring(), 0600); err != nil {
		panic(err)
	}
	return run.Result{Stdout: output}
}

func WriteFlatpakConfig(data []byte) {
	dir := os.Getenv("FLATPAK_USER_DIR")
	if !strings.HasPrefix(filepath.Base(dir), "ops-flatpak-tests-") {
		panic("fake Flatpak state is not isolated")
	}
	if err := os.MkdirAll(filepath.Join(dir, "repo"), 0700); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "repo", "config"), data, 0600); err != nil {
		panic(err)
	}
}
