package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/run"
)

type doctorRunner struct{ calls []run.Spec }

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
		return run.Result{}, nil
	}
	return run.Result{}, fmt.Errorf("unavailable")
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
		if call.Name == "sudo" || strings.Contains(strings.Join(call.Args, " "), "enable --now") {
			t.Fatalf("doctor mutated state: %#v", call)
		}
	}
}
