package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/run"
	sshops "github.com/luigiverona/ops/internal/ssh"
)

type doctorRunner struct {
	calls      []run.Spec
	home       string
	managedKey string
}

func (f *doctorRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, spec)
	if spec.Name == "uname" {
		return run.Result{Stdout: "x86_64\n"}, nil
	}
	if spec.Name == "pacman" && len(spec.Args) > 0 && spec.Args[0] == "-Qq" {
		return run.Result{Stdout: "git\nopenssh\ngithub-cli\nflatpak\nbase-devel\n"}, nil
	}
	if spec.Name == "pacman" && len(spec.Args) > 0 && spec.Args[0] == "-Qqm" {
		return run.Result{}, nil
	}
	if spec.Name == "paru" {
		return run.Result{Stdout: "paru v2\n"}, nil
	}
	if spec.Name == "flatpak" && len(spec.Args) > 0 && spec.Args[0] == "remotes" {
		return run.Result{Stdout: "flathub\n"}, nil
	}
	if spec.Name == "flatpak" {
		return run.Result{}, nil
	}
	if spec.Name == "git" && spec.Args[len(spec.Args)-1] == "user.name" {
		return run.Result{Stdout: "User\n"}, nil
	}
	if spec.Name == "git" && spec.Args[len(spec.Args)-1] == "user.email" {
		return run.Result{Stdout: "user@example.com\n"}, nil
	}
	if spec.Name == "gh" {
		if len(spec.Args) > 1 && spec.Args[0] == "auth" && spec.Args[1] == "status" {
			return run.Result{}, nil
		}
		if len(spec.Args) > 0 && spec.Args[0] == "api" {
			if f.managedKey != "" {
				return run.Result{Stdout: fmt.Sprintf(`[{"id":1,"title":"managed","key":%q}]`, f.managedKey)}, nil
			}
			return run.Result{Stdout: "[]"}, nil
		}
		return run.Result{}, fmt.Errorf("unexpected mutating gh call")
	}
	if spec.Name == "ssh-add" {
		return run.Result{Stderr: "Could not open a connection to your authentication agent."}, fmt.Errorf("unavailable")
	}
	if spec.Name == "ssh-keygen" && f.managedKey != "" {
		fingerprint, _ := sshops.PublicFingerprint(f.managedKey)
		return run.Result{Stdout: "256 " + fingerprint + " managed (ED25519)\n"}, nil
	}
	if spec.Name == "ssh" && len(spec.Args) > 0 && spec.Args[0] == "-G" {
		return run.Result{Stdout: "host github.com\nuser git\nhostname github.com\nidentitiesonly yes\nstricthostkeychecking true\nidentityfile " + filepath.Join(f.home, ".ssh", "ops") + "\nuserknownhostsfile " + filepath.Join(f.home, ".ssh", "ops_known_hosts") + "\n"}, nil
	}
	return run.Result{}, fmt.Errorf("unavailable")
}

func TestDoctorReportsUnavailableHostKeyFreshnessWithoutMutation(t *testing.T) {
	home := t.TempDir()
	path := config.Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(config.Default), 0o600); err != nil {
		t.Fatal(err)
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	managedKey := wirePublic(11)
	if err := os.WriteFile(filepath.Join(sshDir, "ops"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nfixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "ops.pub"), []byte(managedKey+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &doctorRunner{home: home, managedKey: managedKey}
	hostKeyFields := strings.Fields(wirePublic(12))
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ssh_keys":["`+hostKeyFields[0]+` `+hostKeyFields[1]+`"]}`)
	}))
	manager := sshops.Manager{Home: home, Runner: fake, HTTP: metadata.Client(), MetadataURL: metadata.URL}
	if err := manager.ConfigureGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	metadata.Close()
	before := readDoctorSSHFiles(t, sshDir)
	fake.calls = nil
	osRelease := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(osRelease, []byte("ID=arch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	runtime := Runtime{
		Runner: fake, Out: &out, Err: &out, Home: home, EUID: func() int { return 1000 }, OSRelease: osRelease,
		SSHHTTP: metadata.Client(), SSHMetadataURL: metadata.URL,
	}
	code := runtime.Doctor(context.Background())
	if code != Issues || !strings.Contains(out.String(), "GitHub SSH host-key freshness  unavailable  retry later") {
		t.Fatalf("code=%d\n%s", code, out.String())
	}
	after := readDoctorSSHFiles(t, sshDir)
	for name, content := range before {
		if string(after[name]) != string(content) {
			t.Fatalf("doctor modified %s", name)
		}
	}
	for _, call := range fake.calls {
		args := strings.Join(call.Args, " ")
		if call.Name == "sudo" || strings.Contains(args, "enable --now") || call.Interactive ||
			(call.Name == "gh" && !strings.HasPrefix(args, "auth status ") && !strings.HasPrefix(args, "api ")) ||
			(call.Name == "ssh-add" && args != "-L") {
			t.Fatalf("doctor mutated state: %#v", call)
		}
	}
}

func readDoctorSSHFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	for _, name := range []string{"config", "ops", "ops.pub", "ops_config", "ops_known_hosts", "ops_user_config"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = data
	}
	return files
}

func TestDoctorIsReadOnlyAndNeverUsesSudo(t *testing.T) {
	home := t.TempDir()
	path := config.Path(home)
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte(config.Default), 0o600)
	osRelease := filepath.Join(t.TempDir(), "os-release")
	_ = os.WriteFile(osRelease, []byte("ID=arch\n"), 0o600)
	before, _ := os.ReadFile(path)
	var out bytes.Buffer
	fake := &doctorRunner{}
	runtime := Runtime{Runner: fake, Out: &out, Err: &out, Home: home, EUID: func() int { return 1000 }, OSRelease: osRelease}
	code := runtime.Doctor(context.Background())
	if code != Issues {
		t.Fatalf("code=%d output=%s", code, out.String())
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("doctor modified configuration")
	}
	for _, call := range fake.calls {
		args := strings.Join(call.Args, " ")
		if call.Name == "sudo" || strings.Contains(args, "enable --now") || call.Interactive ||
			(call.Name == "gh" && !strings.HasPrefix(args, "auth status ") && !strings.HasPrefix(args, "api ")) ||
			(call.Name == "ssh-add" && args != "-L") {
			t.Fatalf("doctor mutated state: %#v", call)
		}
	}
}
