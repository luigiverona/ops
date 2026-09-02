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
	calls      []run.Spec
	listOutput string
	listErr    error
	recvErr    error
	onRecv     func()
	onImport   func()
}

func (r *keyRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	r.calls = append(r.calls, spec)
	if spec.Name != "gpg" {
		return run.Result{}, errors.New("unexpected command")
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
		return run.Result{Stdout: "verified-keyblock"}, nil
	}
	if strings.Contains(strings.Join(spec.Args, " "), "--import") {
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
			if len(runner.calls) != 1 || runner.calls[0].Interactive || runner.calls[0].Stdin == nil || gpgHome(runner.calls[0]) == home || !strings.Contains(strings.Join(runner.calls[0].Args, " "), "--no-default-keyring --keyring") {
				t.Fatalf("unsafe key inspection spec=%#v", runner.calls)
			}
			if input, err := io.ReadAll(runner.calls[0].Stdin); err != nil || len(input) != 0 {
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
	if len(runner.calls) != 6 {
		t.Fatalf("calls=%#v", runner.calls)
	}
	recv := runner.calls[1]
	if recv.Interactive || recv.Stdin == nil || !strings.Contains(strings.Join(recv.Args, " "), "--keyserver "+keyserver+" --recv-keys "+testFingerprint) || strings.Contains(strings.Join(recv.Args, " "), "--homedir "+home+" ") {
		t.Fatalf("unsafe key retrieval spec=%#v", recv)
	}
	if imported := runner.calls[4]; imported.Interactive || !strings.Contains(strings.Join(imported.Args, " "), "--homedir "+home+" --import") {
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

func TestHasFailsClosedForUnsupportedOrUnsafeGnuPGHomes(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{name: "keyboxd storage", setup: func(t *testing.T) string {
			home := initializedHome(t)
			if err := os.Mkdir(filepath.Join(home, "public-keys.d"), 0o700); err != nil {
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
	if len(runner.calls) != 5 || !strings.Contains(strings.Join(runner.calls[3].Args, " "), "--homedir "+home+" --import") {
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
