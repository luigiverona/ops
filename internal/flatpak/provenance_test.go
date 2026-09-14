package flatpak

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

func TestRealFlatpakEmptyInventoryContract(t *testing.T) {
	if _, err := exec.LookPath("flatpak"); err != nil {
		t.Skip("flatpak unavailable")
	}
	dir := t.TempDir()
	env := []string{"FLATPAK_USER_DIR=" + filepath.Join(dir, "user"), "XDG_CACHE_HOME=" + filepath.Join(dir, "cache"), "XDG_DATA_HOME=" + filepath.Join(dir, "data"), "XDG_CONFIG_HOME=" + filepath.Join(dir, "config")}
	for _, tc := range []struct {
		args []string
		apps bool
	}{
		{[]string{"remotes", "--user", "--show-disabled", "--columns=options,name,url", "--json"}, false},
		{[]string{"list", "--user", "--app", "--columns=origin,application", "--json"}, true},
	} {
		result, err := (run.Exec{}).Run(context.Background(), run.Spec{Name: "flatpak", Args: tc.args, Env: env})
		if err != nil {
			t.Fatalf("%v: %v %s", tc.args, err, result.Stderr)
		}
		if tc.apps {
			apps, err := ParseApplications(result.Stdout)
			if err != nil || len(apps) != 0 {
				t.Fatalf("empty apps %q: %v", result.Stdout, err)
			}
		} else {
			remotes, err := ParseRemotes(result.Stdout)
			if err != nil || len(remotes) != 0 {
				t.Fatalf("empty remotes %q: %v", result.Stdout, err)
			}
		}
	}
}

func TestRemoteIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, url, options string
		ready              bool
	}{
		{"flathub", FlathubRepositoryURL, "", true},
		{"flathub", FlathubRepositoryURL, "disabled", false},
		{"flathub", "https://example.org/repo/", "", false},
		{"flathub", FlathubURL, "", false},
		{"other", FlathubRepositoryURL, "", false},
		{"flathub", FlathubRepositoryURL, "no-gpg-verify", false},
	} {
		output := `[{"name":"` + tc.name + `","url":"` + tc.url + `","options":"` + tc.options + `"}]`
		remotes, err := ParseRemotes(output)
		if err != nil || remotes[tc.name].Ready() != tc.ready {
			t.Fatalf("%s: %+v %v", output, remotes, err)
		}
	}
	for _, output := range []string{"", "\n", "null", "{}", "[{}]", `[{"name":"flathub","url":"https://dl.flathub.org/repo/","options":"","name":"evil"}]`, strings.TrimSuffix(testpkg.Flathub, "]") + "," + strings.TrimPrefix(testpkg.Flathub, "["), `[{"name":"flathub","url":"https://evil/\n\tflathub\thttps://dl.flathub.org/repo/","options":""}]`, testpkg.Flathub + "[]", `[{"name":"flathub","url":"https://dl.flathub.org/repo/","options":"unknown"}]`} {
		if _, err := ParseRemotes(output); err == nil {
			t.Errorf("accepted ambiguous remote output %q", output)
		}
	}
	remotes, err := ParseRemotes("[]")
	if err != nil || len(remotes) != 0 {
		t.Fatalf("missing remote: %v %v", remotes, err)
	}
}
func TestApplicationOrigins(t *testing.T) {
	for _, origin := range []string{"flathub", "other"} {
		apps, err := ParseApplications(`[{"application_id":"org.example.App","origin":"` + origin + `"}]`)
		if err != nil || apps["org.example.App"] != origin {
			t.Fatalf("%v %v", apps, err)
		}
	}
	for _, output := range []string{"\n", "null", "[{}]", `[{"application_id":"org.example.App","origin":""}]`, `[{"application_id":"org.example.App","origin":"flathub","origin":"other"}]`, `[{"application_id":"org.example.App","origin":"flathub"},{"application_id":"org.example.App","origin":"other"}]`, `[{"application_id":"org.example.App","origin":"flathub"},{"application_id":"org.example.App","origin":"flathub"}]`, `[{"application_id":"org.example.App","origin":"flathub\nother"}]`} {
		if _, err := ParseApplications(output); err == nil {
			t.Errorf("accepted %q", output)
		}
	}
}

type provenanceRunner struct {
	remote, apps string
	mutation     []string
	badPost      bool
}

func (r *provenanceRunner) Run(_ context.Context, s run.Spec) (run.Result, error) {
	if s.Name != "flatpak" || !strings.Contains(" "+strings.Join(s.Args, " ")+" ", " --user ") {
		panic("not user scoped")
	}
	switch s.Args[0] {
	case "remotes":
		return run.Result{Stdout: r.remote}, nil
	case "list":
		return run.Result{Stdout: r.apps}, nil
	case "remote-add", "remote-modify":
		r.mutation = append(r.mutation, strings.Join(s.Args, " "))
		if !r.badPost {
			r.remote = testpkg.Flathub
		}
	case "install":
		r.mutation = append(r.mutation, strings.Join(s.Args, " "))
		if !r.badPost {
			r.apps = testpkg.FlatpakApps("org.example.App")
		}
	default:
		panic("unexpected Flatpak command")
	}
	return run.Result{}, nil
}
func TestRemoteMutationsRevalidateAndVerify(t *testing.T) {
	for _, op := range []string{"add", "enable"} {
		for _, badPost := range []bool{false, true} {
			remote := "[]"
			if op == "enable" {
				remote = strings.Replace(testpkg.Flathub, `"options":""`, `"options":"disabled"`, 1)
			}
			runner := &provenanceRunner{remote: remote, apps: "[]", badPost: badPost}
			m := Manager{Runner: runner}
			var err error
			if op == "add" {
				err = m.AddFlathub(context.Background())
			} else {
				err = m.EnableFlathub(context.Background())
			}
			if (err != nil) != badPost || len(runner.mutation) != 1 {
				t.Fatalf("%s badPost=%v err=%v mutations=%v", op, badPost, err, runner.mutation)
			}
			if strings.Contains(runner.mutation[0], "--if-not-exists") {
				t.Fatal("namesake preserved")
			}
		}
	}
	runner := &provenanceRunner{remote: strings.Replace(testpkg.Flathub, FlathubRepositoryURL, "https://evil/", 1), apps: "[]"}
	m := Manager{Runner: runner}
	if m.AddFlathub(context.Background()) == nil || m.EnableFlathub(context.Background()) == nil || len(runner.mutation) != 0 {
		t.Fatal("wrong source mutated")
	}
}
func TestInstallAndVerifyRejectWrongOrigin(t *testing.T) {
	runner := &provenanceRunner{remote: testpkg.Flathub, apps: `[{"application_id":"org.example.App","origin":"other"}]`}
	manager := Manager{Runner: runner}
	if manager.Install(context.Background(), "org.example.App") == nil || manager.Verify(context.Background(), "org.example.App") == nil || len(runner.mutation) != 0 {
		t.Fatal("wrong origin accepted or migrated")
	}
	runner.apps = "[]"
	runner.badPost = true
	if manager.Install(context.Background(), "org.example.App") == nil || len(runner.mutation) != 1 {
		t.Fatal("missing postcondition accepted")
	}
	runner.apps = testpkg.FlatpakApps("org.example.App")
	if err := manager.Verify(context.Background(), "org.example.App"); err != nil {
		t.Fatal(err)
	}
	runner.remote = strings.Replace(testpkg.Flathub, FlathubRepositoryURL, "https://evil/", 1)
	if manager.Verify(context.Background(), "org.example.App") == nil {
		t.Fatal("name-only remote accepted")
	}
}
