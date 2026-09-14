package flatpak

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type isolatedFlatpak struct{ env []string }

func (r isolatedFlatpak) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	if s.Name != "flatpak" || (s.Args[0] != "remotes" && s.Args[0] != "list") {
		panic("review fixture permits inspection only")
	}
	s.Env = append(s.Env, r.env...)
	return (run.Exec{}).Run(ctx, s)
}

// Build the OSTree repository structure directly so this probe does not import
// GPG keys, create a real user remote, or contact the network.
func reviewFlatpak(t *testing.T, options string, legacy bool) isolatedFlatpak {
	t.Helper()
	if _, err := exec.LookPath("flatpak"); err != nil {
		t.Skip("flatpak unavailable")
	}
	dir := t.TempDir()
	for _, sub := range []string{"repo/objects", "repo/tmp", "repo/refs/heads", "repo/refs/remotes", "repo/extensions", "repo/state"} {
		if err := os.MkdirAll(filepath.Join(dir, "user", sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	data := "[core]\nrepo_version=1\nmode=bare-user-only\nmin-free-space-size=500MB\n[remote \"flathub\"]\nurl=" + FlathubRepositoryURL + "\ngpg-verify=true\n" + options
	if legacy {
		data = strings.Replace(data, "min-free-space-size=500MB\n", "", 1)
	}
	path := filepath.Join(dir, "user/repo/config")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		after, err := os.ReadFile(path)
		if err != nil || string(after) != data {
			t.Errorf("read-only inspection changed remote config: %v", err)
		}
	})
	return isolatedFlatpak{env: []string{"FLATPAK_USER_DIR=" + filepath.Join(dir, "user"), "XDG_CACHE_HOME=" + filepath.Join(dir, "cache"), "XDG_DATA_HOME=" + filepath.Join(dir, "data"), "XDG_CONFIG_HOME=" + filepath.Join(dir, "config")}}
}

func TestReviewFlatpakInspectionMustNotModifyConfig(t *testing.T) {
	runner := reviewFlatpak(t, "", true)
	var path string
	for _, item := range runner.env {
		if strings.HasPrefix(item, "FLATPAK_USER_DIR=") {
			path = filepath.Join(strings.TrimPrefix(item, "FLATPAK_USER_DIR="), "repo/config")
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (Manager{Runner: runner}).Remotes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(data) {
		t.Errorf("D-R5: inspection rewrote repo/config: %s", after)
	}
}

func TestReviewFlatpakSummaryVerificationMustRemainEnabled(t *testing.T) {
	runner := reviewFlatpak(t, "gpg-verify-summary=false\n", false)
	remotes, err := (Manager{Runner: runner}).Remotes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if remotes["flathub"].Ready() {
		t.Error("D-R2: disabled summary verification accepted as canonical Flathub")
	}
}

func TestReviewRealFlatpakSubsetMustNotBeCanonical(t *testing.T) {
	for _, subset := range []string{"verified", "-", ""} {
		t.Run(subset, func(t *testing.T) {
			runner := reviewFlatpak(t, "xa.subset="+subset+"\nxa.title=Harmless title\nxa.comment=Harmless comment\n", false)
			remotes, err := (Manager{Runner: runner}).Remotes(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if remotes["flathub"].Ready() != (subset == "") {
				t.Fatalf("subset %q accepted as canonical: %+v", subset, remotes["flathub"])
			}
		})
	}
}

func TestReviewFlatpakJSONValuesFailClosed(t *testing.T) {
	for _, parse := range []func(string) error{
		func(s string) error { _, err := ParseRemotes(s); return err },
		func(s string) error { _, err := ParseApplications(s); return err },
	} {
		for _, value := range []string{"null", "false", "true", "0", `""`, "{}", "[null]", "[false]", "[{}]", "[] []"} {
			if parse(value) == nil {
				t.Errorf("accepted %q", value)
			}
		}
	}
	for _, value := range []string{"null", "false", "true", "0", "[]", "{}"} {
		remote := `[{"name":"flathub","url":"` + FlathubRepositoryURL + `","options":` + value + `}]`
		if _, err := ParseRemotes(remote); err == nil {
			t.Errorf("accepted non-string option %s", value)
		}
		apps := `[{"application_id":"org.example.App","origin":` + value + `}]`
		if _, err := ParseApplications(apps); err == nil {
			t.Errorf("accepted non-string origin %s", value)
		}
	}
	if _, err := ParseApplications(strings.Replace(`[{"application_id":"org.example.App","origin":"flathub"}]`, `"origin"`, `"unexpected"`, 1)); err == nil {
		t.Error("accepted unknown field")
	}
}
