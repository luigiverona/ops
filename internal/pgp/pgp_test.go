package pgp

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/run"
)

const testFingerprint = "0123456789ABCDEF0123456789ABCDEF01234567"

type keyRunner struct {
	calls        []run.Spec
	listOutput   string
	listErr      error
	configOutput string
	configErr    error
	exportOutput string
	exportSet    bool
	importErr    error
	recvErr      error
	onRecv       func()
	onImport     func()
}

func (r *keyRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	r.calls = append(r.calls, spec)
	if spec.Name != "gpg" {
		return run.Result{}, errors.New("unexpected command")
	}
	if strings.Contains(strings.Join(spec.Args, " "), "--gpgconf-list") {
		output := r.configOutput
		if output == "" {
			output = "use_keyboxd:16:0:\n"
		}
		return run.Result{Stdout: output}, r.configErr
	}
	if strings.Contains(strings.Join(spec.Args, " "), "--list-keys") {
		return run.Result{Stdout: r.listOutput}, r.listErr
	}
	if strings.Contains(strings.Join(spec.Args, " "), "--recv-keys") {
		if r.onRecv != nil {
			r.onRecv()
		}
		return run.Result{}, r.recvErr
	}
	if strings.Contains(strings.Join(spec.Args, " "), "--export") {
		if r.exportSet {
			return run.Result{Stdout: r.exportOutput}, nil
		}
		return run.Result{Stdout: "verified-keyblock"}, nil
	}
	if strings.Contains(strings.Join(spec.Args, " "), "--import") {
		if r.importErr != nil {
			return run.Result{}, r.importErr
		}
		if input, err := io.ReadAll(spec.Stdin); err != nil || string(input) != "verified-keyblock" {
			return run.Result{}, errors.New("unexpected imported keyblock")
		}
		if home := gpgHome(spec); home != "" {
			if err := os.WriteFile(filepath.Join(home, "pubring.kbx"), []byte("updated"), 0o600); err != nil {
				return run.Result{}, err
			}
		}
		if r.onImport != nil {
			r.onImport()
		}
		return run.Result{}, nil
	}
	return run.Result{}, errors.New("unexpected gpg arguments")
}

type directoryEntry struct {
	mode os.FileMode
	data string
}

func directorySnapshot(t *testing.T, root string) map[string]directoryEntry {
	t.Helper()
	entries := make(map[string]directoryEntry)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		item := directoryEntry{mode: info.Mode()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			item.data = string(data)
		}
		entries[relative] = item
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func testGPG(t *testing.T, home string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg is not available")
	}
	base := []string{"--batch", "--no-tty", "--pinentry-mode", "loopback", "--passphrase", "", "--homedir", home}
	command := exec.Command("gpg", append(base, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("gpg %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func gpgHome(spec run.Spec) string {
	for index, arg := range spec.Args {
		if arg == "--homedir" && index+1 < len(spec.Args) {
			return spec.Args[index+1]
		}
	}
	return ""
}

func initializedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "pubring.kbx"), []byte("public-keyring"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func initializedKeyboxdHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "public-keys.d"), 0o750); err != nil {
		t.Fatal(err)
	}
	return home
}

func primaryFingerprint(fingerprint string) string {
	return "pub:-:2048:1:0000000000000000:0::::::\n" +
		"fpr:::::::::" + fingerprint + ":\n"
}

func TestHasDoesNotCreateOrInspectMissingGnuPGHome(t *testing.T) {
	runner := &keyRunner{}
	home := t.TempDir() + "/missing"
	present, err := (Manager{Runner: runner, Home: home}).Has(context.Background(), testFingerprint)
	if err != nil || present || len(runner.calls) != 0 {
		t.Fatalf("present=%v err=%v calls=%#v", present, err, runner.calls)
	}
}

func TestHasRequiresExactPrimaryFingerprint(t *testing.T) {
	for _, test := range []struct {
		name    string
		output  string
		want    bool
		wantErr bool
	}{
		{name: "exact primary", output: primaryFingerprint(testFingerprint), want: true},
		{name: "lowercase gpg output", output: primaryFingerprint(strings.ToLower(testFingerprint)), want: true},
		{name: "wrong primary", output: primaryFingerprint("FEDCBA9876543210FEDCBA9876543210FEDCBA98"), want: false},
		{name: "subkey only", output: "sub:-:2048:1:0000000000000000:0::::::\nfpr:::::::::" + testFingerprint + ":\n", want: false},
		{name: "multiple primaries", output: primaryFingerprint(testFingerprint) + primaryFingerprint("FEDCBA9876543210FEDCBA9876543210FEDCBA98"), wantErr: true},
		{name: "malformed", output: "pub:bad\n", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := initializedHome(t)
			runner := &keyRunner{listOutput: test.output}
			present, err := (Manager{Runner: runner, Home: home}).Has(context.Background(), testFingerprint)
			if present != test.want || (err != nil) != test.wantErr {
				t.Fatalf("present=%v err=%v", present, err)
			}
			if len(runner.calls) != 2 || !strings.Contains(strings.Join(runner.calls[0].Args, " "), "--gpgconf-list") || runner.calls[1].Interactive || runner.calls[1].Stdin == nil || gpgHome(runner.calls[1]) == home || !strings.Contains(strings.Join(runner.calls[1].Args, " "), "--no-default-keyring --keyring") {
				t.Fatalf("unsafe key inspection spec=%#v", runner.calls)
			}
			if input, err := io.ReadAll(runner.calls[1].Stdin); err != nil || len(input) != 0 {
				t.Fatalf("gpg inherited input=%q err=%v", input, err)
			}
		})
	}
}

func TestHasRecognizesOnlyKnownMissingKeyError(t *testing.T) {
	home := initializedHome(t)
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "known missing", err: &run.Error{Stderr: "gpg: error reading key: No public key", Err: errors.New("exit status 2")}, want: false},
		{name: "network", err: &run.Error{Stderr: "gpg: keybox unavailable", Err: errors.New("exit status 2")}, want: true},
		{name: "plain wrapped", err: errors.New("gpg: error reading key: No public key"), want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			present, err := (Manager{Runner: &keyRunner{listErr: test.err}, Home: home}).Has(context.Background(), testFingerprint)
			if present || (err != nil) != test.want {
				t.Fatalf("present=%v err=%v", present, err)
			}
		})
	}
}

func TestHasInspectsKeyboxdExportsInAnIsolatedHome(t *testing.T) {
	t.Run("existing exact key", func(t *testing.T) {
		home := initializedKeyboxdHome(t)
		runner := &keyRunner{configOutput: "use_keyboxd:16:1:\n", listOutput: primaryFingerprint(testFingerprint)}
		present, err := (Manager{Runner: runner, Home: home}).Has(context.Background(), testFingerprint)
		if err != nil || !present {
			t.Fatalf("present=%v err=%v", present, err)
		}
		if len(runner.calls) != 4 || !strings.Contains(strings.Join(runner.calls[0].Args, " "), "--gpgconf-list") || gpgHome(runner.calls[0]) != home || !strings.Contains(strings.Join(runner.calls[1].Args, " "), "--export-options export-minimal --export -- "+testFingerprint) || gpgHome(runner.calls[1]) != home {
			t.Fatalf("keyboxd export=%#v", runner.calls)
		}
		if !strings.Contains(strings.Join(runner.calls[2].Args, " "), "--import") || gpgHome(runner.calls[2]) == home || !strings.Contains(strings.Join(runner.calls[3].Args, " "), "--fingerprint --list-keys") {
			t.Fatalf("keyboxd isolated validation=%#v", runner.calls)
		}
		assertHasNeverUsesNetwork(t, runner.calls)
	})

	t.Run("missing key", func(t *testing.T) {
		runner := &keyRunner{configOutput: "use_keyboxd:16:1:\n", exportSet: true}
		present, err := (Manager{Runner: runner, Home: initializedKeyboxdHome(t)}).Has(context.Background(), testFingerprint)
		if err != nil || present || len(runner.calls) != 2 {
			t.Fatalf("present=%v err=%v calls=%#v", present, err, runner.calls)
		}
		assertHasNeverUsesNetwork(t, runner.calls)
	})

	for _, test := range []struct {
		name   string
		runner *keyRunner
	}{
		{name: "malformed exported material", runner: &keyRunner{configOutput: "use_keyboxd:16:1:\n", importErr: errors.New("invalid OpenPGP data")}},
		{name: "ambiguous exported material", runner: &keyRunner{configOutput: "use_keyboxd:16:1:\n", listOutput: primaryFingerprint(testFingerprint) + primaryFingerprint("FEDCBA9876543210FEDCBA9876543210FEDCBA98")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			present, err := (Manager{Runner: test.runner, Home: initializedKeyboxdHome(t)}).Has(context.Background(), testFingerprint)
			if present || err == nil {
				t.Fatalf("present=%v err=%v", present, err)
			}
		})
	}
}

func assertHasNeverUsesNetwork(t *testing.T, calls []run.Spec) {
	t.Helper()
	for _, call := range calls {
		args := strings.Join(call.Args, " ")
		if strings.Contains(args, "--keyserver") || strings.Contains(args, "--recv-keys") || strings.Contains(args, "--auto-key-retrieve") || !strings.Contains(args, "--no-auto-key-retrieve") {
			t.Fatalf("Has may have used network-capable GnuPG options: %#v", call)
		}
	}
}

func TestParseKeyboxdModeFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name    string
		output  string
		want    bool
		wantErr bool
	}{
		{name: "active", output: "debug-level:16:\"none:\nuse_keyboxd:16:1:\n", want: true},
		{name: "inactive", output: "use_keyboxd:16:0:\n", want: false},
		{name: "missing", output: "debug-level:16:\"none:\n", wantErr: true},
		{name: "malformed fields", output: "use_keyboxd:16:1\n", wantErr: true},
		{name: "unexpected flags", output: "use_keyboxd:0:1:\n", wantErr: true},
		{name: "invalid value", output: "use_keyboxd:16:2:\n", wantErr: true},
		{name: "ambiguous", output: "use_keyboxd:16:0:\nuse_keyboxd:16:1:\n", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseKeyboxdMode(test.output)
			if got != test.want || (err != nil) != test.wantErr {
				t.Fatalf("mode=%v err=%v", got, err)
			}
		})
	}
}

func TestImportRetrievesOnlyExactFingerprintAndRevalidates(t *testing.T) {
	home := initializedHome(t)
	before, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	runner := &keyRunner{listErr: &run.Error{Stderr: "gpg: error reading key: No public key", Err: errors.New("exit status 2")}}
	runner.onRecv = func() {
		runner.listErr = nil
		runner.listOutput = primaryFingerprint(testFingerprint)
	}
	if err := (Manager{Runner: runner, Home: home}).Import(context.Background(), testFingerprint); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(home)
	if err != nil || !os.SameFile(before, after) || after.Mode().Perm() != 0o700 {
		t.Fatalf("existing GnuPG home was not preserved: after=%v err=%v", after, err)
	}
	if len(runner.calls) != 8 {
		t.Fatalf("calls=%#v", runner.calls)
	}
	recv := runner.calls[2]
	if recv.Interactive || recv.Stdin == nil || !strings.Contains(strings.Join(recv.Args, " "), "--keyserver "+keyserver+" --recv-keys "+testFingerprint) || strings.Contains(strings.Join(recv.Args, " "), "--homedir "+home+" ") {
		t.Fatalf("unsafe key retrieval spec=%#v", recv)
	}
	if imported := runner.calls[5]; imported.Interactive || !strings.Contains(strings.Join(imported.Args, " "), "--homedir "+home+" --import") {
		t.Fatalf("verified key was not imported into the user keyring: %#v", imported)
	}
}

func TestImportRejectsWrongReturnedKeyAndInvalidFingerprint(t *testing.T) {
	home := initializedHome(t)
	runner := &keyRunner{listErr: &run.Error{Stderr: "gpg: error reading key: No public key", Err: errors.New("exit status 2")}}
	runner.onRecv = func() {
		runner.listErr = nil
		runner.listOutput = primaryFingerprint("FEDCBA9876543210FEDCBA9876543210FEDCBA98")
	}
	if err := (Manager{Runner: runner, Home: home}).Import(context.Background(), testFingerprint); err == nil || !strings.Contains(err.Error(), "did not provide") {
		t.Fatalf("wrong key error=%v", err)
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call.Args, " "), "--import") {
			t.Fatalf("wrong key reached the destination import: %#v", call)
		}
	}
	for _, fingerprint := range []string{"0123456789ABCDEF", strings.ToLower(testFingerprint)} {
		runner := &keyRunner{}
		if _, err := (Manager{Runner: runner, Home: home}).Has(context.Background(), fingerprint); err == nil || len(runner.calls) != 0 {
			t.Fatalf("fingerprint=%q err=%v calls=%#v", fingerprint, err, runner.calls)
		}
	}
}

func TestImportDoesNotCreateMissingGnuPGHomeForAnUnverifiedKey(t *testing.T) {
	home := filepath.Join(t.TempDir(), "gnupg")
	runner := &keyRunner{listErr: &run.Error{Stderr: "gpg: error reading key: No public key", Err: errors.New("exit status 2")}}
	runner.onRecv = func() {
		runner.listErr = nil
		runner.listOutput = primaryFingerprint("FEDCBA9876543210FEDCBA9876543210FEDCBA98")
	}
	err := (Manager{Runner: runner, Home: home}).Import(context.Background(), testFingerprint)
	if err == nil || !strings.Contains(err.Error(), "did not provide") {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unverified key created GnuPG home: %v", err)
	}
}

func TestGnuPGHomePathIsNotCreatedDuringPlanning(t *testing.T) {
	home := t.TempDir() + "/missing"
	_, err := (Manager{Runner: &keyRunner{}, Home: home}).Has(context.Background(), testFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("planning created GnuPG home: %v", err)
	}
}

func TestHasHonorsAnAbsentGNUPGHOMEWithoutCreatingIt(t *testing.T) {
	home := filepath.Join(t.TempDir(), "gnupg")
	t.Setenv("GNUPGHOME", home)
	present, err := (Manager{Runner: &keyRunner{}}).Has(context.Background(), testFingerprint)
	if err != nil || present {
		t.Fatalf("present=%v err=%v", present, err)
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("planning created GNUPGHOME: %v", err)
	}
}

func TestHasValidatesRelativeGNUPGHOMEWithoutCreatingIt(t *testing.T) {
	parent := t.TempDir()
	t.Chdir(parent)
	t.Setenv("GNUPGHOME", "gnupg")
	present, err := (Manager{Runner: &keyRunner{}}).Has(context.Background(), testFingerprint)
	if err != nil || present {
		t.Fatalf("present=%v err=%v", present, err)
	}
	if _, err := os.Lstat(filepath.Join(parent, "gnupg")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("planning created resolved relative GNUPGHOME: %v", err)
	}
}

func TestHasDoesNotModifyEmptyExistingGnuPGHome(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	before := directorySnapshot(t, home)
	present, err := (Manager{Runner: run.Exec{}, Home: home}).Has(context.Background(), testFingerprint)
	if err != nil || present {
		t.Fatalf("present=%v err=%v", present, err)
	}
	if after := directorySnapshot(t, home); !reflect.DeepEqual(after, before) {
		t.Fatalf("read-only inspection changed empty GnuPG home: before=%#v after=%#v", before, after)
	}
}

func TestHasInspectsInitializedPublicKeyringWithoutModifyingIt(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	testGPG(t, home, "--quick-generate-key", "ops test <ops@example.invalid>", "rsa2048", "sign", "1d")
	metadata := testGPG(t, home, "--with-colons", "--fingerprint", "--list-keys")
	var fingerprint string
	for _, line := range strings.Split(metadata, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 9 && fields[0] == "fpr" {
			fingerprint = fields[9]
			break
		}
	}
	if !aurmeta.ValidFingerprint(fingerprint) {
		t.Fatalf("generated fingerprint=%q", fingerprint)
	}
	before := directorySnapshot(t, home)
	present, err := (Manager{Runner: run.Exec{}, Home: home}).Has(context.Background(), fingerprint)
	if err != nil || !present {
		t.Fatalf("present=%v err=%v", present, err)
	}
	if after := directorySnapshot(t, home); !reflect.DeepEqual(after, before) {
		t.Fatalf("read-only inspection changed initialized GnuPG home: before=%#v after=%#v", before, after)
	}
}

func TestHasInspectsKeyboxdPublicKeysWithoutModifyingThem(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "common.conf"), []byte("use-keyboxd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGPG(t, home, "--quick-generate-key", "ops keyboxd test <ops@example.invalid>", "ed25519", "sign", "1d")
	metadata := testGPG(t, home, "--with-colons", "--fingerprint", "--list-keys")
	fingerprint := firstPrimaryFingerprint(metadata)
	if !aurmeta.ValidFingerprint(fingerprint) {
		t.Fatalf("generated fingerprint=%q", fingerprint)
	}
	if _, err := os.Stat(filepath.Join(home, "public-keys.d")); err != nil {
		t.Fatalf("keyboxd storage was not created: %v", err)
	}
	before := directorySnapshot(t, home)
	present, err := (Manager{Runner: run.Exec{}, Home: home}).Has(context.Background(), fingerprint)
	if err != nil || !present {
		t.Fatalf("present=%v err=%v", present, err)
	}
	if after := directorySnapshot(t, home); !reflect.DeepEqual(after, before) {
		t.Fatalf("read-only keyboxd inspection changed GnuPG home: before=%#v after=%#v", before, after)
	}
}

func TestHasDoesNotTreatInactiveKeyboxdStorageAsActive(t *testing.T) {
	home := initializedKeyboxdHome(t)
	before := directorySnapshot(t, home)
	present, err := (Manager{Runner: run.Exec{}, Home: home}).Has(context.Background(), testFingerprint)
	if err != nil || present {
		t.Fatalf("present=%v err=%v", present, err)
	}
	if after := directorySnapshot(t, home); !reflect.DeepEqual(after, before) {
		t.Fatalf("inactive keyboxd storage changed GnuPG home: before=%#v after=%#v", before, after)
	}
	if _, err := os.Stat(filepath.Join(home, "pubring.kbx")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inactive keyboxd storage initialized a classic keyring: %v", err)
	}
}

func TestHasTreatsActiveKeyboxdWithoutStorageAsAbsentWithoutMutation(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "common.conf"), []byte("use-keyboxd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := directorySnapshot(t, home)
	present, err := (Manager{Runner: run.Exec{}, Home: home}).Has(context.Background(), testFingerprint)
	if err != nil || present {
		t.Fatalf("present=%v err=%v", present, err)
	}
	if after := directorySnapshot(t, home); !reflect.DeepEqual(after, before) {
		t.Fatalf("missing keyboxd storage changed GnuPG home: before=%#v after=%#v", before, after)
	}
}

func TestHasDoesNotConsultClassicKeyringsWhenKeyboxdIsActiveWithoutStorage(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	testGPG(t, home, "--quick-generate-key", "ops stale classic test <ops@example.invalid>", "ed25519", "sign", "1d")
	fingerprint := firstPrimaryFingerprint(testGPG(t, home, "--with-colons", "--fingerprint", "--list-keys"))
	if !aurmeta.ValidFingerprint(fingerprint) {
		t.Fatalf("generated fingerprint=%q", fingerprint)
	}
	if _, err := os.Stat(filepath.Join(home, "pubring.kbx")); err != nil {
		t.Fatalf("classic keyring was not created: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "common.conf"), []byte("use-keyboxd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "public-keys.d")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("keyboxd storage unexpectedly exists: %v", err)
	}
	before := directorySnapshot(t, home)
	present, err := (Manager{Runner: run.Exec{}, Home: home}).Has(context.Background(), fingerprint)
	if err != nil || present {
		t.Fatalf("present=%v err=%v", present, err)
	}
	if after := directorySnapshot(t, home); !reflect.DeepEqual(after, before) {
		t.Fatalf("active keyboxd inspection consulted or changed stale classic state: before=%#v after=%#v", before, after)
	}
	if _, err := os.Stat(filepath.Join(home, "public-keys.d")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active keyboxd inspection created storage: %v", err)
	}
}

func firstPrimaryFingerprint(metadata string) string {
	for _, line := range strings.Split(metadata, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 9 && fields[0] == "fpr" {
			return fields[9]
		}
	}
	return ""
}

func TestHasFailsClosedForUnsupportedOrUnsafeGnuPGHomes(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{name: "keyboxd storage symlink", setup: func(t *testing.T) string {
			home := initializedHome(t)
			storage := filepath.Join(t.TempDir(), "public-keys.d")
			if err := os.Mkdir(storage, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(storage, filepath.Join(home, "public-keys.d")); err != nil {
				t.Fatal(err)
			}
			return home
		}},
		{name: "world-readable home", setup: func(t *testing.T) string {
			home := initializedHome(t)
			if err := os.Chmod(home, 0o755); err != nil {
				t.Fatal(err)
			}
			return home
		}},
		{name: "symlink", setup: func(t *testing.T) string {
			target := initializedHome(t)
			home := filepath.Join(t.TempDir(), "gnupg")
			if err := os.Symlink(target, home); err != nil {
				t.Fatal(err)
			}
			return home
		}},
		{name: "non-directory", setup: func(t *testing.T) string {
			home := filepath.Join(t.TempDir(), "gnupg")
			if err := os.WriteFile(home, []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
			return home
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := (Manager{Runner: &keyRunner{}, Home: test.setup(t)}).Has(context.Background(), testFingerprint); err == nil {
				t.Fatal("unsafe GnuPG home was accepted")
			}
		})
	}
}

func TestImportCreatesMissingGnuPGHomeOnlyAfterApprovedKeyWasVerified(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "gnupg")
	runner := &keyRunner{listErr: &run.Error{Stderr: "gpg: error reading key: No public key", Err: errors.New("exit status 2")}}
	runner.onRecv = func() {
		runner.listErr = nil
		runner.listOutput = primaryFingerprint(testFingerprint)
	}
	if err := (Manager{Runner: runner, Home: home}).Import(context.Background(), testFingerprint); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("created GnuPG home mode=%#o directory=%v", info.Mode().Perm(), info.IsDir())
	}
	if len(runner.calls) != 6 || !strings.Contains(strings.Join(runner.calls[3].Args, " "), "--homedir "+home+" --import") {
		t.Fatalf("unexpected missing-home import sequence: %#v", runner.calls)
	}
}

func TestImportRejectsUnsafeDestinationAndMismatchedPostcondition(t *testing.T) {
	for _, test := range []struct {
		name   string
		home   func(t *testing.T) string
		mutate func(r *keyRunner)
		want   string
	}{
		{name: "symlink", home: func(t *testing.T) string {
			target := initializedHome(t)
			home := filepath.Join(t.TempDir(), "gnupg")
			if err := os.Symlink(target, home); err != nil {
				t.Fatal(err)
			}
			return home
		}, want: "GnuPG home must not be a symlink"},
		{name: "non-directory", home: func(t *testing.T) string {
			home := filepath.Join(t.TempDir(), "gnupg")
			if err := os.WriteFile(home, []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
			return home
		}, want: "GnuPG home is not a directory"},
		{name: "postcondition mismatch", home: func(t *testing.T) string { return initializedHome(t) }, mutate: func(r *keyRunner) {
			r.onImport = func() { r.listOutput = primaryFingerprint("FEDCBA9876543210FEDCBA9876543210FEDCBA98") }
		}, want: "does not match"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := test.home(t)
			runner := &keyRunner{listErr: &run.Error{Stderr: "gpg: error reading key: No public key", Err: errors.New("exit status 2")}}
			runner.onRecv = func() {
				runner.listErr = nil
				runner.listOutput = primaryFingerprint(testFingerprint)
			}
			if test.mutate != nil {
				test.mutate(runner)
			}
			err := (Manager{Runner: runner, Home: home}).Import(context.Background(), testFingerprint)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
