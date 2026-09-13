// Package git manages only the global user name and email.
package git

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/luigiverona/ops/internal/run"
)

// Identity is the current global Git identity.
type Identity struct{ Name, Email string }

type Manager struct{ Runner run.Runner }

// Inspect reads both global values. An error leaves no usable identity snapshot.
func (m Manager) Inspect(ctx context.Context) (Identity, error) {
	name, err := m.optionalGlobalValue(ctx, "user.name")
	if err != nil {
		return Identity{}, err
	}
	email, err := m.optionalGlobalValue(ctx, "user.email")
	if err != nil {
		return Identity{}, err
	}
	return Identity{Name: name, Email: email}, nil
}

func (m Manager) optionalGlobalValue(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("inspect Git %s: %w", key, err)
	}
	result, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "git", Args: []string{"config", "--global", "--get", key}})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", fmt.Errorf("inspect Git %s: %w", key, ctxErr)
	}
	if err == nil {
		if result.Stderr == "" {
			return strings.TrimSpace(result.Stdout), nil
		}
		// Git can return an XDG fallback value with exit 0 after warning
		// that the higher-priority ~/.gitconfig could not be read.
		err = &run.Error{Name: "git", Stderr: result.Stderr, Err: errors.New("global configuration inspection emitted diagnostics")}
	}
	// --get exits 1 for an unset value, but also for some read failures.
	// Only a silent exit with no value establishes absence.
	if result.Stdout == "" && result.Stderr == "" && silentMissingExit(err) {
		return "", nil
	}
	return "", fmt.Errorf("inspect Git %s: %w", key, err)
}

// Absence requires a single exit-1 cause. run.Exited can also
// find that exit inside a joined inspection failure, which is inconclusive.
func silentMissingExit(err error) bool {
	for err != nil {
		if commandErr, ok := err.(*run.Error); ok &&
			(commandErr.Stderr != "" || commandErr.Evidence != "" || commandErr.EvidenceTruncated) {
			return false
		}
		switch cause := err.(type) {
		case interface{ Unwrap() []error }:
			return false
		case interface{ Unwrap() error }:
			err = cause.Unwrap()
		default:
			exit, ok := err.(interface{ ExitCode() int })
			return ok && exit.ExitCode() == 1
		}
	}
	return false
}

func ValidName(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.ContainsAny(value, "\r\n\x00")
}

func ValidEmail(value string) bool {
	value = strings.TrimSpace(value)
	at := strings.IndexByte(value, '@')
	return at > 0 && at < len(value)-1 && !strings.ContainsAny(value, " \t\r\n\x00")
}

func (m Manager) SetMissing(ctx context.Context, current Identity, name, email string) error {
	if !ValidName(current.Name) {
		if !ValidName(name) {
			return errors.New("Git user.name is required")
		}
		if _, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "git", Args: []string{"config", "--global", "user.name", strings.TrimSpace(name)}}); err != nil {
			return err
		}
	}
	if !ValidEmail(current.Email) {
		if !ValidEmail(email) {
			return errors.New("valid Git user.email is required")
		}
		if _, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "git", Args: []string{"config", "--global", "user.email", strings.TrimSpace(email)}}); err != nil {
			return err
		}
	}
	verified, err := m.Inspect(ctx)
	if err != nil {
		return fmt.Errorf("verify Git identity: %w", err)
	}
	if !ValidName(verified.Name) || !ValidEmail(verified.Email) {
		return errors.New("Git identity verification failed")
	}
	return nil
}
