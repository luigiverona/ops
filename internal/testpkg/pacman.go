// Package testpkg supplies explicit package metadata fixtures for command fakes.
package testpkg

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/luigiverona/ops/internal/run"
)

func Info(target string) string {
	repo, name, ok := strings.Cut(target, "/")
	if !ok {
		name = target
		repo = "extra"
		if name == "openssh" {
			repo = "core"
		}
	}
	return "Repository : " + repo + "\nName : " + name + "\nVersion : 1-1\nArchitecture : x86_64\nBuild Date : Thu Jan 1 00:00:00 2026\nPackager : Arch fixture\nDepends On : None\n"
}
func Query(s run.Spec) (run.Result, bool) {
	if result, ok := OfficialStage(s); ok {
		return result, true
	}
	if s.Name == "pacman-conf" && len(s.Args) == 0 {
		return run.Result{Stdout: "[options]\nArchitecture = x86_64\n[core]\n[extra]\n[multilib]\n"}, true
	}
	if s.Name != "pacman" || len(s.Args) == 0 {
		return run.Result{}, false
	}
	switch s.Args[0] {
	case "-Qi", "-Si":
		return run.Result{Stdout: Info(s.Args[len(s.Args)-1])}, true
	case "-Sl":
		return run.Result{Stdout: "extra git 1-1\ncore openssh 1-1\nextra github-cli 1-1\nextra flatpak 1-1\nextra base-devel 1-1\nextra firefox 1-1\n"}, true
	case "-Sp":
		if len(s.Args) > 3 && s.Args[3] == "%r/%n" {
			for i, arg := range s.Args {
				if arg == "--" {
					return run.Result{Stdout: strings.Join(s.Args[i+1:], "\n") + "\n"}, true
				}
			}
		}
	}
	return run.Result{}, false
}

const Flathub = `[{"options":"","name":"flathub","url":"https://dl.flathub.org/repo/"}]`

func FlatpakApps(ids ...string) string {
	rows := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, map[string]string{"origin": "flathub", "application_id": id})
	}
	data, _ := json.Marshal(rows)
	return string(data)
}

// FakeContent is an explicit synthetic content assertion for command-only unit
// tests. Native adversarial tests use actual payload comparisons instead.
func FakeContent(_ context.Context, _ run.Runner, _ string) (bool, error) {
	// This test double declares its synthetic payload correct. Metadata drift is
	// still tested by InstalledMatch; real content attacks use PacmanFixture.
	return true, nil
}
