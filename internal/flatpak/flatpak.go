// Package flatpak manages only user-scoped Flatpak state.
package flatpak

import (
	"context"

	"github.com/luigiverona/ops/internal/run"
)

const FlathubURL = "https://dl.flathub.org/repo/flathub.flatpakrepo"

type Manager struct{ Runner run.Runner }

func (m Manager) AddFlathub(ctx context.Context) error {
	_, err := m.Runner.Run(ctx, run.Spec{Name: "flatpak", Args: []string{"remote-add", "--user", "--if-not-exists", "flathub", FlathubURL}})
	return err
}

func (m Manager) Install(ctx context.Context, id string) error {
	_, err := m.Runner.Run(ctx, run.Spec{Name: "flatpak", Args: []string{"install", "--user", "--noninteractive", "flathub", id}})
	return err
}

func (m Manager) Ready(ctx context.Context, id string) bool {
	_, err := m.Runner.Run(ctx, run.Spec{Name: "flatpak", Args: []string{"info", "--user", id}})
	return err == nil
}
