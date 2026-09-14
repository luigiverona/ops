// Package run provides the single external-command execution boundary.
package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Spec describes one command without shell interpolation.
type Spec struct {
	Name  string
	Args  []string
	Dir   string
	Env   []string
	Stdin io.Reader
	// ReadOnlyFilesystem prevents native inventory commands from initializing
	// or migrating host files. Requires bubblewrap; never falls back to a normal
	// process. This is for trusted query tools, not arbitrary hostile programs.
	ReadOnlyFilesystem bool
	// StreamOutput exposes native transaction output without granting stdin access.
	StreamOutput bool
	Interactive  bool
	// Interaction documents why this child, rather than ops, must own the
	// terminal. Interactive commands without it are rejected to prevent an
	// implementation shortcut from leaking arbitrary child output.
	Interaction string
	// AllowTruncatedOutput is only for logs, never parsed command output.
	AllowTruncatedOutput bool
	// FailureOutput opts in only at command boundaries whose output is safe to report.
	FailureOutput FailureOutput
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
	if spec.Interactive && spec.Interaction == "" {
		return Result{}, fmt.Errorf("interactive command %q has no declared terminal boundary", spec.Name)
	}
	name, args := spec.Name, spec.Args
	if spec.ReadOnlyFilesystem {
		if spec.Interactive || spec.StreamOutput || spec.Stdin != nil {
			return Result{}, errors.New("read-only inventory cannot request interaction or input")
		}
		name = "bwrap"
		args = append([]string{"--unshare-all", "--die-with-parent", "--new-session", "--ro-bind", "/", "/", "--proc", "/proc", "--setenv", "DBUS_SESSION_BUS_ADDRESS", "unix:path=/dev/null", "--setenv", "DBUS_SYSTEM_BUS_ADDRESS", "unix:path=/dev/null", "--", spec.Name}, spec.Args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = spec.Dir
	cmd.Env = append(append(os.Environ(), "LC_ALL=C"), spec.Env...)
	cmd.Stdin = spec.Stdin
	if cmd.Stdin == nil && spec.Interactive {
		cmd.Stdin = e.In
	}
	var stdout, stderr tailBuffer
	var diagnostic diagnosticBuffer
	if spec.Interactive || spec.StreamOutput {
		cmd.Stdout = io.MultiWriter(&stdout, e.Out)
		cmd.Stderr = io.MultiWriter(&stderr, e.Err)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}
	if !spec.Interactive && !spec.StreamOutput {
		if spec.FailureOutput == FailureCombined {
			cmd.Stdout = io.MultiWriter(&stdout, &diagnostic)
		}
		if spec.FailureOutput == FailureCombined || spec.FailureOutput == FailureStderr {
			cmd.Stderr = io.MultiWriter(&stderr, &diagnostic)
		}
	}
	err := cmd.Run()
	if !spec.Interactive && !spec.AllowTruncatedOutput && (stdout.truncated || stderr.truncated) {
		err = errors.Join(err, errors.New("command output exceeded capture limit; refusing incomplete inspection"))
	}
	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		return result, &Error{Name: spec.Name, Args: append([]string(nil), spec.Args...), Stderr: strings.TrimSpace(result.Stderr), Presented: spec.Interactive || spec.StreamOutput, Evidence: diagnostic.String(), EvidenceTruncated: diagnostic.truncated, Err: err}
	}
	return result, nil
}

const captureLimit = 2 * 1024 * 1024

type tailBuffer struct {
	data      []byte
	truncated bool
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= captureLimit {
		b.truncated = b.truncated || n > captureLimit || len(b.data) > 0
		b.data = append(b.data[:0], p[n-captureLimit:]...)
		return n, nil
	}
	if len(b.data)+n > captureLimit {
		b.truncated = true
		drop := len(b.data) + n - captureLimit
		copy(b.data, b.data[drop:])
		b.data = b.data[:len(b.data)-drop]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (b *tailBuffer) String() string { return string(b.data) }

// FailureOutput is an explicit privacy decision; authentication, key/configuration
// dumps and arbitrary command output are excluded by default.
type FailureOutput uint8

const (
	FailureNone FailureOutput = iota
	FailureStderr
	FailureCombined
)

// Diagnostic writers may receive stdout and stderr concurrently. Keep their
// arrival order, without changing the separate results used by parsers.
type diagnosticBuffer struct {
	sync.Mutex
	data      []byte
	truncated bool
	privacy   privacyScan
}

const diagnosticLimit = 16 * 1024

func (b *diagnosticBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	n := len(p)
	b.privacy.write(p)
	if b.privacy.withheld {
		b.data = nil
		return n, nil
	}
	if n >= diagnosticLimit {
		b.truncated = b.truncated || n > diagnosticLimit || len(b.data) > 0
		b.data = append(b.data[:0], p[n-diagnosticLimit:]...)
	} else {
		if drop := len(b.data) + n - diagnosticLimit; drop > 0 {
			b.truncated = true
			copy(b.data, b.data[drop:])
			b.data = b.data[:len(b.data)-drop]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (b *diagnosticBuffer) String() string {
	if b.privacy.sensitive() {
		return WithheldDiagnostic
	}
	return string(b.data)
}

// Error retains machine-readable stderr for existing local checks. Error() never
// embeds output or arguments; presentation owns bounded, opt-in evidence.
type Error struct {
	Name              string
	Args              []string
	Stderr            string
	Presented         bool
	Evidence          string
	EvidenceTruncated bool
	Err               error
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s failed: %v", e.Name, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// Exited reports a structured child exit code through wrapped command errors.
func Exited(err error, code int) bool {
	var exit interface{ ExitCode() int }
	return errors.As(err, &exit) && exit.ExitCode() == code
}
