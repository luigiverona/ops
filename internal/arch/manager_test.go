package arch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

type managerRunner struct{ calls []run.Spec }

func (f *managerRunner) Run(_ context.Context, spec run.Spec) (run.Result, error) {
	f.calls = append(f.calls, spec)
	args := spec.Args
	if spec.Name == "sudo" && len(args) > 0 && args[0] == "-n" {
		args = args[1:]
	}
	if spec.Name == "sudo" && len(args) > 0 {
		switch args[0] {
		case "install":
			data, err := os.ReadFile(args[len(args)-2])
			if err == nil {
				err = os.WriteFile(args[len(args)-1], data, 0o644)
			}
			return run.Result{}, err
		case "mv":
			return run.Result{}, os.Rename(args[len(args)-2], args[len(args)-1])
		case "rm":
			for _, path := range args[4:] {
				_ = os.Remove(path)
			}
		}
	}
	return run.Result{}, nil
}

func TestPacmanCommandsNeverCreatePartialUpgrade(t *testing.T) {
	f := &managerRunner{}
	m := Manager{Runner: f}
	if err := m.FullUpgrade(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(context.Background(), []string{"firefox"}, false); err != nil {
		t.Fatal(err)
	}
	first := strings.Join(f.calls[0].Args, " ")
	second := strings.Join(f.calls[1].Args, " ")
	if !strings.Contains(first, "pacman -Syu") || strings.Contains(first, "pacman -Sy ") {
		t.Fatalf("unsafe upgrade: %s", first)
	}
	if !strings.Contains(second, "pacman -S --needed") || strings.Contains(second, " -Sy") {
		t.Fatalf("unsafe install: %s", second)
	}
}

func TestManagerEnablesFixtureAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pacman.conf")
	_ = os.WriteFile(path, []byte("[core]\nInclude = /mirror\n#[multilib]\n#Include = /mirror\n"), 0o644)
	f := &managerRunner{}
	m := Manager{Runner: f, PacmanConf: path}
	if err := m.EnableMultilib(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	enabled, err := MultilibEnabled(data)
	if err != nil || !enabled {
		t.Fatalf("enabled=%v err=%v data=%s", enabled, err, data)
	}
	for _, call := range f.calls {
		if call.Name == "sudo" && len(call.Args) > 0 && call.Args[0] != "-n" {
			t.Fatalf("sudo could reprompt: %#v", call.Args)
		}
	}
}
