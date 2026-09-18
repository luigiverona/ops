package flatpak

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

func TestSupportedFlathubIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, remove, add          string
		ready, disabled, malformed bool
	}{
		{name: "canonical", ready: true},
		{name: "disabled", add: "xa.disable=true", ready: true, disabled: true},
		{name: "wrong URL", remove: "url=", add: "url=https://example.org/"},
		{name: "commit verification off", remove: "gpg-verify=", add: "gpg-verify=false"},
		{name: "commit default", remove: "gpg-verify=", ready: true},
		{name: "summary verification off", remove: "gpg-verify-summary=", add: "gpg-verify-summary=false"},
		{name: "summary default off", remove: "gpg-verify-summary="},
		{name: "collection empty", add: "collection-id=", ready: true},
		{name: "collection with summary", add: "collection-id=org.flathub.Stable"},
		{name: "collection without summary", remove: "gpg-verify-summary=", add: "collection-id=org.flathub.Stable\ngpg-verify-summary=false"},
		{name: "verified", add: "xa.subset=verified"},
		{name: "floss", add: "xa.subset=floss"},
		{name: "verified_floss", add: "xa.subset=verified_floss"},
		{name: "literal dash", add: "xa.subset=-"},
		{name: "empty subset", add: "xa.subset=", ready: true},
		{name: "explicitly empty subset", add: "xa.subset=\nxa.subset-is-set=true", ready: true},
		{name: "escaped space subset", add: `xa.subset=\s`},
		{name: "empty filter", add: "xa.filter=", ready: true},
		{name: "filter", add: "xa.filter=/tmp/does-not-exist"},
		{name: "filter dash is path", add: "xa.filter=-"},
		{name: "OCI", add: "xa.oci=true"},
		{name: "explicit OSTree", add: "xa.oci=false", ready: true},
		{name: "content override", add: "contenturl=https://example.org/"},
		{name: "empty content override unsupported", add: "contenturl="},
		{name: "canonical content override unsupported", add: "contenturl=" + FlathubRepositoryURL},
		{name: "mirrorlist", remove: "url=", add: "url=mirrorlist=" + FlathubRepositoryURL},
		{name: "metalink", add: "metalink=https://example.org/"},
		{name: "TLS permissive", add: "tls-permissive=true"},
		{name: "TLS normal", add: "tls-permissive=false", ready: true},
		{name: "custom CA", add: "tls-ca-path=/tmp/ca"},
		{name: "TLS client", add: "tls-client-cert-path=/tmp/cert"},
		{name: "TLS client key", add: "tls-client-key-path=/tmp/key"},
		{name: "proxy", add: "proxy=https://example.org/"},
		{name: "backend", add: "custom-backend=other"},
		{name: "unconfigured", add: "unconfigured-state=subscription"},
		{name: "branches", add: "branches=app/org.example.App/x86_64/stable;"},
		{name: "extra trust", add: "gpgkeypath=/tmp/key"},
		{name: "no enumerate", add: "xa.noenumerate=true"},
		{name: "no deps", add: "xa.nodeps=true"},
		{name: "authenticator", add: "xa.authenticator-name=org.example.Auth"},
		{name: "authenticator option", add: "xa.authenticator-options.url=https://example.org/"},
		{name: "authenticator installation", add: "xa.authenticator-install=true"},
		{name: "token type", add: "xa.default-token-type=1"},
		{name: "default branch", add: "xa.default-branch=other"},
		{name: "main ref", add: "xa.main-ref=app/org.example.App/x86_64/stable"},
		{name: "priority", add: "xa.prio=0"},
		{name: "signature lookaside", add: "xa.signature-lookaside=https://example.org/"},
		{name: "redirect", add: "xa.redirect-url=https://example.org/"},
		{name: "deploy collection", add: "xa.deploy-collection-id=org.example.Repo"},
		{name: "new keys", add: "xa.gpg-keys=other"},
		{name: "unknown", add: "xa.future-restriction=true"},
		{name: "numeric true", remove: "gpg-verify-summary=", add: "gpg-verify-summary=1", ready: true},
		{name: "numeric false", remove: "gpg-verify-summary=", add: "gpg-verify-summary=0"},
		{name: "boolean whitespace", remove: "gpg-verify-summary=", add: "gpg-verify-summary= true \t", ready: true},
		{name: "malformed boolean", remove: "gpg-verify-summary=", add: "gpg-verify-summary=True", malformed: true},
		{name: "escaped boolean", remove: "gpg-verify-summary=", add: `gpg-verify-summary=true\s`, malformed: true},
		{name: "inline comment boolean", remove: "gpg-verify-summary=", add: "gpg-verify-summary=true #comment", malformed: true},
		{name: "malformed disabled", add: "xa.disable=perhaps", malformed: true},
		{name: "presentation", remove: "xa.title=", add: `xa.title=Harmless\sTitle\nWith\\Escaping` + "\nxa.title-is-set=true", ready: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := fixtureConfig(tc.remove, tc.add)
			groups, err := parseKeyfile(data)
			if err != nil {
				t.Fatal(err)
			}
			ready, disabled, err := supportedFlathub(groups, testpkg.FlatpakKeyring())
			if (err != nil) != tc.malformed || ready != tc.ready || disabled != tc.disabled {
				t.Fatalf("trusted=%v disabled=%v err=%v", ready, disabled, err)
			}
		})
	}
}

func fixtureConfig(remove, add string) []byte {
	var lines []string
	for _, line := range strings.Split(string(testpkg.FlatpakConfig()), "\n") {
		if remove != "" && strings.HasPrefix(line, remove) {
			continue
		}
		lines = append(lines, line)
	}
	return []byte(strings.Join(lines, "\n") + add + "\n")
}

func nativeIdentityFixture(t *testing.T, data, key []byte) Manager {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FLATPAK_USER_DIR", filepath.Join(dir, "user"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	for _, sub := range []string{"objects", "tmp", "refs/heads", "refs/remotes", "extensions", "state"} {
		if err := os.MkdirAll(filepath.Join(dir, "user/repo", sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "user/repo/config"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if key != nil {
		if err := os.WriteFile(filepath.Join(dir, "user/repo/flathub.trustedkeys.gpg"), key, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return Manager{Runner: run.Exec{}}
}

func TestNativeFlathubIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, remove, add string
		ready, disabled   bool
	}{
		{name: "canonical", ready: true},
		{name: "disabled", add: "xa.disable=true", disabled: true},
		{name: "summary disabled", remove: "gpg-verify-summary=", add: "gpg-verify-summary=false"},
		{name: "summary absent", remove: "gpg-verify-summary="},
		{name: "summary numeric", remove: "gpg-verify-summary=", add: "gpg-verify-summary=1", ready: true},
		{name: "commit absent uses default", remove: "gpg-verify=", ready: true},
		{name: "empty collection", add: "collection-id=", ready: true},
		{name: "collection summary true", add: "collection-id=org.flathub.Stable"},
		{name: "collection summary false", remove: "gpg-verify-summary=", add: "collection-id=org.flathub.Stable\ngpg-verify-summary=false"},
		{name: "verified", add: "xa.subset=verified"},
		{name: "floss", add: "xa.subset=floss"},
		{name: "verified_floss", add: "xa.subset=verified_floss"},
		{name: "dash", add: "xa.subset=-"},
		{name: "empty subset", add: "xa.subset=\nxa.subset-is-set=true", ready: true},
		{name: "empty filter", add: "xa.filter=", ready: true},
		{name: "missing filter", add: "xa.filter=/does-not-exist"},
		{name: "OCI", add: "xa.oci=true"},
		{name: "content override", add: "contenturl=https://example.org/"},
		{name: "TLS permissive", add: "tls-permissive=true"},
		{name: "presentation", remove: "xa.title=", add: `xa.title=Another\sTitle\nWith\\Escaping` + "\nxa.title-is-set=true", ready: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := nativeIdentityFixture(t, fixtureConfig(tc.remove, tc.add), testpkg.FlatpakKeyring())
			before := persistentFiles(t, os.Getenv("FLATPAK_USER_DIR"))
			remotes, err := m.Remotes(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			remote := remotes["flathub"]
			if remote.Ready() != tc.ready || (remote.Canonical() && !remote.Enabled) != tc.disabled {
				t.Fatalf("%+v", remote)
			}
			if err := m.Verify(context.Background(), "org.example.App"); err == nil {
				t.Fatal("absent app accepted")
			}
			_, _ = m.Applications(context.Background())
			if after := persistentFiles(t, os.Getenv("FLATPAK_USER_DIR")); before != after {
				t.Fatal("native inventory modified persistent files")
			}
		})
	}
}

func persistentFiles(t *testing.T, root string) string {
	t.Helper()
	var out strings.Builder
	err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		st, err := e.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&out, "%s %s %v\n", path, st.Mode(), st.ModTime())
		if st.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&out, "%x\n", b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestNativeTrustMaterialFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  []byte
	}{
		{"absent", nil}, {"empty", []byte{}}, {"malformed", []byte("not a keyring")},
		{"replaced", bytes.Repeat([]byte{1}, 2888)},
		{"appended", append(append([]byte{}, testpkg.FlatpakKeyring()...), 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := nativeIdentityFixture(t, testpkg.FlatpakConfig(), tc.key)
			if err := os.WriteFile(filepath.Join(os.Getenv("FLATPAK_USER_DIR"), "repo/other.trustedkeys.gpg"), testpkg.FlatpakKeyring(), 0600); err != nil {
				t.Fatal(err)
			}
			remotes, err := m.Remotes(context.Background())
			if err == nil && remotes["flathub"].Canonical() {
				t.Fatal("untrusted keyring accepted, possibly borrowed from other remote")
			}
		})
	}
}

func TestKeyfileAmbiguityFailsClosed(t *testing.T) {
	for _, data := range [][]byte{
		fixtureConfig("", "gpg-verify=true"), fixtureConfig("", "[remote \"flathub\"]\nurl="+FlathubRepositoryURL),
		fixtureConfig("", "[core]\nparent=/tmp/other"), fixtureConfig("", "not-an-entry"),
		fixtureConfig("", "xa.subset[en]=verified"), fixtureConfig("", "[bad] junk"),
		fixtureConfig("", ";not-a-comment"), append(testpkg.FlatpakConfig(), 0),
		{0xff}, bytes.Repeat([]byte{'#'}, identityFileLimit+1),
	} {
		if _, err := parseKeyfile(data); err == nil {
			t.Fatalf("accepted ambiguous keyfile %.120q", data)
		}
	}
	for _, value := range []string{`\q`, `\;`, `\`} {
		if _, err := keyfileString(value); err == nil {
			t.Fatalf("accepted escape %q", value)
		}
	}
	groups, err := parseKeyfile([]byte(" # comment\n [core] \n repo_version = 1\nmode=bare-user-only\n[remote \"flathub\"]\nurl=" + FlathubRepositoryURL + "\ngpg-verify-summary=true\nxa.title=foo # literal\n"))
	if err != nil {
		t.Fatal(err)
	}
	ready, _, err := supportedFlathub(groups, testpkg.FlatpakKeyring())
	if err != nil || !ready {
		t.Fatalf("keyfile semantics %v %v", ready, err)
	}
}

func TestIdentityFilesAreBoundedAndNeverFollowSymlinks(t *testing.T) {
	for _, target := range []string{"config", "flathub.trustedkeys.gpg"} {
		for _, kind := range []string{"symlink", "directory", "fifo", "oversize"} {
			t.Run(target+"/"+kind, func(t *testing.T) {
				m := nativeIdentityFixture(t, testpkg.FlatpakConfig(), testpkg.FlatpakKeyring())
				path := filepath.Join(os.Getenv("FLATPAK_USER_DIR"), "repo", target)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				var err error
				switch kind {
				case "symlink":
					err = os.Symlink(filepath.Join(t.TempDir(), "absent"), path)
				case "directory":
					err = os.Mkdir(path, 0700)
				case "fifo":
					err = syscall.Mkfifo(path, 0600)
				case "oversize":
					err = os.WriteFile(path, bytes.Repeat([]byte{'#'}, identityFileLimit+1), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				// Direct inspection must reject before any FIFO read/native query.
				err = inspectRemoteIdentity(map[string]Remote{"flathub": {Name: "flathub", URL: FlathubRepositoryURL, Enabled: true}})
				if err == nil {
					t.Fatal("unsafe file accepted")
				}
				_ = m
			})
		}
	}
	root := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if dir, err := openIdentityDirectory(link); err == nil {
		dir.Close()
		t.Fatal("symlink directory accepted")
	}
}

func TestIdentityReadsPreserveAccessTimes(t *testing.T) {
	nativeIdentityFixture(t, testpkg.FlatpakConfig(), testpkg.FlatpakKeyring())
	path := filepath.Join(os.Getenv("FLATPAK_USER_DIR"), "repo/config")
	var before, after syscall.Stat_t
	if err := syscall.Stat(path, &before); err != nil {
		t.Fatal(err)
	}
	if err := inspectRemoteIdentity(map[string]Remote{"flathub": {Name: "flathub", URL: FlathubRepositoryURL, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Stat(path, &after); err != nil {
		t.Fatal(err)
	}
	if before.Atim != after.Atim || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		t.Fatal("direct inspection changed timestamps")
	}
}

type identityRunner func(context.Context, run.Spec) (run.Result, error)

func (r identityRunner) Run(ctx context.Context, spec run.Spec) (run.Result, error) {
	return r(ctx, spec)
}

func TestHiddenIdentityDriftAroundMutation(t *testing.T) {
	for _, drift := range []string{"summary", "subset", "filter", "content", "keyring"} {
		for _, operation := range []string{"add", "enable", "install"} {
			for _, when := range []string{"before", "after", "during app inventory"} {
				if when == "during app inventory" && operation != "install" {
					continue
				}
				t.Run(drift+"/"+operation+"/"+when, func(t *testing.T) {
					mutations := 0
					drifted := when == "before"
					runner := identityRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
						switch s.Args[0] {
						case "remotes":
							value := testpkg.Flathub
							if operation == "enable" && mutations == 0 {
								value = strings.Replace(value, `"options":""`, `"options":"disabled"`, 1)
							}
							if operation == "add" && mutations == 0 && !drifted {
								value = "[]"
							}
							result := testpkg.FlatpakRemotes(value)
							if drifted {
								data, err := os.ReadFile(filepath.Join(os.Getenv("FLATPAK_USER_DIR"), "repo/config"))
								if err != nil {
									t.Fatal(err)
								}
								switch drift {
								case "summary":
									data = bytes.Replace(data, []byte("gpg-verify-summary=true"), []byte("gpg-verify-summary=false"), 1)
								case "subset":
									data = append(data, []byte("xa.subset=verified\n")...)
								case "filter":
									data = append(data, []byte("xa.filter=/missing\n")...)
								case "content":
									data = append(data, []byte("contenturl=https://other/\n")...)
								case "keyring":
									if err := os.WriteFile(filepath.Join(os.Getenv("FLATPAK_USER_DIR"), "repo/flathub.trustedkeys.gpg"), []byte("replaced"), 0600); err != nil {
										t.Fatal(err)
									}
								}
								testpkg.WriteFlatpakConfig(data)
							}
							return result, nil
						case "list":
							if when == "during app inventory" {
								drifted = true
							}
							if mutations > 0 {
								return run.Result{Stdout: testpkg.FlatpakApps("org.example.App")}, nil
							}
							return run.Result{}, nil
						case "remote-add", "remote-modify", "install":
							mutations++
							drifted = true
							return run.Result{}, nil
						}
						panic("unexpected command")
					})
					m := Manager{Runner: runner}
					var err error
					switch operation {
					case "add":
						err = m.AddFlathub(context.Background())
					case "enable":
						err = m.EnableFlathub(context.Background())
					case "install":
						err = m.Install(context.Background(), "org.example.App")
					}
					wantMutations := 0
					if when == "after" {
						wantMutations = 1
					}
					if err == nil || mutations != wantMutations {
						t.Fatalf("err=%v mutations=%d", err, mutations)
					}
				})
			}
		}
	}
}

func TestNativeFilterFilesNeverBecomeFullFlathub(t *testing.T) {
	for _, contents := range []string{"allow *\n", "deny *\nallow org.example.App\n", "malformed\n", ""} {
		t.Run(contents, func(t *testing.T) {
			filter := filepath.Join(t.TempDir(), "filter")
			if err := os.WriteFile(filter, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			m := nativeIdentityFixture(t, fixtureConfig("", "xa.filter="+filter), testpkg.FlatpakKeyring())
			for _, missing := range []bool{false, true} {
				if missing {
					if err := os.Rename(filter, filepath.Join(os.Getenv("FLATPAK_USER_DIR"), "repo/flathub.filter")); err != nil {
						t.Fatal(err)
					}
				}
				remotes, err := m.Remotes(context.Background())
				if err == nil && remotes["flathub"].Canonical() {
					t.Fatal("configured filter accepted")
				}
			}
		})
	}
}

func TestNativeCookiePolicyAndIgnoredFilterBackup(t *testing.T) {
	for _, file := range []string{"flathub.cookies.txt", "flathub.filter"} {
		t.Run(file, func(t *testing.T) {
			m := nativeIdentityFixture(t, testpkg.FlatpakConfig(), testpkg.FlatpakKeyring())
			if err := os.WriteFile(filepath.Join(os.Getenv("FLATPAK_USER_DIR"), "repo", file), []byte("arbitrary content"), 0600); err != nil {
				t.Fatal(err)
			}
			remotes, err := m.Remotes(context.Background())
			if err != nil || remotes["flathub"].Ready() != (file == "flathub.filter") {
				t.Fatalf("%+v %v", remotes, err)
			}
		})
	}
}

func TestInventoryAlwaysHasDeadline(t *testing.T) {
	m := Manager{Runner: identityRunner(func(ctx context.Context, s run.Spec) (run.Result, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded native query")
		}
		return testpkg.FlatpakRemotes("[]"), nil
	})}
	if _, err := m.Remotes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Applications(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNativeMalformedConfigQueryIsBounded(t *testing.T) {
	m := nativeIdentityFixture(t, testpkg.FlatpakConfig(), testpkg.FlatpakKeyring())
	path := filepath.Join(os.Getenv("FLATPAK_USER_DIR"), "repo/config")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	for _, query := range []func(context.Context) error{
		func(ctx context.Context) error { _, err := m.Remotes(ctx); return err },
		func(ctx context.Context) error { _, err := m.Applications(ctx); return err },
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := query(ctx)
		cancel()
		if err == nil {
			t.Fatal("FIFO configuration accepted")
		}
	}
}
