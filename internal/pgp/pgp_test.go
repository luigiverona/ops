package pgp

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

const testFingerprint = "0123456789ABCDEF0123456789ABCDEF01234567"

type keyRunner struct {
	calls      []run.Spec
	listOutput string
	listErr    error
	recvErr    error
	onRecv     func()
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
		return run.Result{}, nil
	}
	return run.Result{}, errors.New("unexpected gpg arguments")
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
			home := t.TempDir()
			runner := &keyRunner{listOutput: test.output}
			present, err := (Manager{Runner: runner, Home: home}).Has(context.Background(), testFingerprint)
			if present != test.want || (err != nil) != test.wantErr {
				t.Fatalf("present=%v err=%v", present, err)
			}
			if len(runner.calls) != 1 || runner.calls[0].Interactive || runner.calls[0].Stdin == nil || !strings.Contains(strings.Join(runner.calls[0].Args, " "), "--no-tty --homedir "+home) {
				t.Fatalf("unsafe key inspection spec=%#v", runner.calls)
			}
			if input, err := io.ReadAll(runner.calls[0].Stdin); err != nil || len(input) != 0 {
				t.Fatalf("gpg inherited input=%q err=%v", input, err)
			}
		})
	}
}

func TestHasRecognizesOnlyKnownMissingKeyError(t *testing.T) {
	home := t.TempDir()
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
	home := t.TempDir()
	runner := &keyRunner{listErr: &run.Error{Stderr: "gpg: error reading key: No public key", Err: errors.New("exit status 2")}}
	runner.onRecv = func() {
		runner.listErr = nil
		runner.listOutput = primaryFingerprint(testFingerprint)
	}
	if err := (Manager{Runner: runner, Home: home}).Import(context.Background(), testFingerprint); err != nil {
		t.Fatal(err)
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
	home := t.TempDir()
	runner := &keyRunner{listErr: &run.Error{Stderr: "gpg: error reading key: No public key", Err: errors.New("exit status 2")}}
	runner.onRecv = func() {
		runner.listErr = nil
		runner.listOutput = primaryFingerprint("FEDCBA9876543210FEDCBA9876543210FEDCBA98")
	}
	if err := (Manager{Runner: runner, Home: home}).Import(context.Background(), testFingerprint); err == nil || !strings.Contains(err.Error(), "did not provide") {
		t.Fatalf("wrong key error=%v", err)
	}
	for _, fingerprint := range []string{"0123456789ABCDEF", strings.ToLower(testFingerprint)} {
		runner := &keyRunner{}
		if _, err := (Manager{Runner: runner, Home: home}).Has(context.Background(), fingerprint); err == nil || len(runner.calls) != 0 {
			t.Fatalf("fingerprint=%q err=%v calls=%#v", fingerprint, err, runner.calls)
		}
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
