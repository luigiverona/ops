// Package run provides the single external-command execution boundary.
package run

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Spec describes one command without shell interpolation.
type Spec struct {
	Name        string
	Args        []string
	Dir         string
	Env         []string
	Stdin       io.Reader
	Interactive bool
}

// Result contains captured output. Output is limited by callers when reported.
type Result struct {
	Stdout string
	Stderr string
}

// Runner is implemented by Exec and test fakes.
type Runner interface {
	Run(context.Context, Spec) (Result, error)
}

// Exec executes commands directly and never through a shell.
type Exec struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

func (e Exec) Run(ctx context.Context, spec Spec) (Result, error) {
	cmd := exec.CommandContext(ctx, spec.Name, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), spec.Env...)
	cmd.Stdin = spec.Stdin
	if cmd.Stdin == nil {
		cmd.Stdin = e.In
	}
	var stdout, stderr tailBuffer
	if spec.Interactive {
		cmd.Stdout = io.MultiWriter(&stdout, e.Out)
		cmd.Stderr = io.MultiWriter(&stderr, e.Err)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}
	err := cmd.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		return result, &Error{Name: spec.Name, Args: append([]string(nil), spec.Args...), Stderr: strings.TrimSpace(result.Stderr), Err: err}
	}
	return result, nil
}

const captureLimit = 64 * 1024

type tailBuffer struct{ data []byte }

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= captureLimit {
		b.data = append(b.data[:0], p[n-captureLimit:]...)
		return n, nil
	}
	if len(b.data)+n > captureLimit {
		drop := len(b.data) + n - captureLimit
		copy(b.data, b.data[drop:])
		b.data = b.data[:len(b.data)-drop]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (b *tailBuffer) String() string { return string(b.data) }

// Error preserves actionable stderr while avoiding shell-formatted commands.
type Error struct {
	Name   string
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s failed: %s", e.Name, e.Stderr)
	}
	return fmt.Sprintf("%s failed: %v", e.Name, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }
