// Package git manages only the global user name and email.
package git

import (
	"context"
	"errors"
	"strings"

	"github.com/luigiverona/ops/internal/run"
)

// Identity is the current global Git identity.
type Identity struct{ Name, Email string }

type Manager struct{ Runner run.Runner }

func (m Manager) Inspect(ctx context.Context) Identity {
	var identity Identity
	if result, err := m.Runner.Run(ctx, run.Spec{Name: "git", Args: []string{"config", "--global", "--get", "user.name"}}); err == nil {
		identity.Name = strings.TrimSpace(result.Stdout)
	}
	if result, err := m.Runner.Run(ctx, run.Spec{Name: "git", Args: []string{"config", "--global", "--get", "user.email"}}); err == nil {
		identity.Email = strings.TrimSpace(result.Stdout)
	}
	return identity
}

func (m Manager) SetMissing(ctx context.Context, current Identity, name, email string) error {
	if current.Name == "" {
		if strings.TrimSpace(name) == "" {
			return errors.New("Git user.name is required")
		}
		if _, err := m.Runner.Run(ctx, run.Spec{Name: "git", Args: []string{"config", "--global", "user.name", strings.TrimSpace(name)}}); err != nil {
			return err
		}
	}
	if current.Email == "" {
		if strings.TrimSpace(email) == "" || !strings.Contains(email, "@") {
			return errors.New("valid Git user.email is required")
		}
		if _, err := m.Runner.Run(ctx, run.Spec{Name: "git", Args: []string{"config", "--global", "user.email", strings.TrimSpace(email)}}); err != nil {
			return err
		}
	}
	verified := m.Inspect(ctx)
	if verified.Name == "" || verified.Email == "" {
		return errors.New("Git identity verification failed")
	}
	return nil
}
