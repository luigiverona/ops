package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/release"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/ui"
)

// Exercise canonical terminal input and the kernel's Ctrl-C signal delivery,
// keeping the slave open to compare terminal settings after the child exits.
const promptPTY = `
import os, pty, subprocess, sys, select, termios, fcntl, time, signal
master, slave = pty.openpty()
before = termios.tcgetattr(slave)
def terminal():
    os.setsid()
    fcntl.ioctl(0, termios.TIOCSCTTY, 0)
p = subprocess.Popen([sys.argv[1], '-test.run=^TestPromptTerminalHelper$'], stdin=slave, stdout=slave, stderr=slave, preexec_fn=terminal)
output = b''
def until(marker):
    global output
    deadline = time.monotonic() + 5
    while marker not in output:
        if time.monotonic() > deadline: raise Exception('prompt timeout: ' + repr(output))
        if select.select([master], [], [], .05)[0]: output += os.read(master, 65536)
try:
    mode = os.environ['OPS_PROMPT_TEST']
    if mode.startswith('update'):
        until(b'Download, verify, and install to /usr/local/bin/ops? [Y/n] ')
    else:
        until(b'Continue? [Y/n] ')
        if mode.startswith('git') or mode.startswith('aur'):
            os.write(master, b'y\n')
            if mode.startswith('git'):
                until(b'Git name: ')
                if mode == 'git-email':
                    os.write(master, b'User\n')
                    until(b'Git email: ')
            else:
                until(b'q: skip application > ')
                if mode == 'aur-approval':
                    os.write(master, b'\n')
                    until(b'Build and install paru? [y/N] ')
    started = time.monotonic()
    if mode.endswith('sigterm'): p.send_signal(signal.SIGTERM)
    else: os.write(master, b'\x04' if mode.endswith('eof') else b'\x03')
    p.wait(timeout=3)
    assert time.monotonic() - started < 2, 'slow cancellation'
    while select.select([master], [], [], .05)[0]: output += os.read(master, 65536)
    assert p.returncode == 2, (p.returncode, output)
    assert before == termios.tcgetattr(slave), 'terminal mode leaked'
    assert b'NO LATER WORK' in output, output
    if mode != 'git-email': assert b'Git email:' not in output, output
    assert b'Workstation ready.' not in output and b'Workstation setup incomplete.' not in output, output
    if mode.endswith('eof'):
        assert b'Interrupted.' not in output and b'Update interrupted.' not in output, output
    else:
        if mode.startswith('update'): expected = b'Update interrupted.'
        elif mode == 'git-after' or mode.startswith('aur'): expected = b'Interrupted. Earlier changes may remain. Run ops doctor before retrying.'
        else: expected = b'Interrupted. No workstation changes made.'
        assert output.count(expected) == 1, output
        assert b'No changes made.' not in output and b'stopped.' not in output, output
        assert b'final verification' not in output and b'Issues' not in output, output
    if mode.startswith('aur'):
        assert b'\x1b[?1049h' in output and b'\x1b[?1049l' in output, output
    if mode == 'aur-review':
        assert b'Build and install paru?' not in output, output
    sys.stdout.buffer.write(output)
finally:
    if p.poll() is None: p.kill(); p.wait()
    os.close(master); os.close(slave)
`

func TestPromptTerminalCancellationAndEOF(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"setup", "setup-sigterm", "update", "git-before", "git-email", "git-after", "aur-review", "aur-approval", "setup-eof", "update-eof", "git-eof"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, python, "-c", promptPTY, binary)
			cmd.Env = append(os.Environ(), "OPS_PROMPT_TEST="+mode, "TERM=xterm")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("PTY: %v\n%s", err, output)
			}
		})
	}
}

func TestPromptTerminalHelper(t *testing.T) {
	mode := os.Getenv("OPS_PROMPT_TEST")
	if mode == "" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	tty, err := ui.OpenTTY()
	if err != nil {
		t.Fatal(err)
	}
	terminal := ui.UI{In: tty, Out: tty}
	fd := tty.Fd()
	flags := func() uintptr {
		value, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
		if errno != 0 {
			t.Fatal(errno)
		}
		return value
	}
	before := flags()
	runner := &prepareRunner{}
	a := Runtime{Runner: runner, Out: os.Stdout, Err: os.Stderr, Home: t.TempDir()}
	var code int
	if strings.HasPrefix(mode, "update") {
		code = a.installUpdate(ctx, release.Client{}, "9.0.0", terminal)
	} else {
		p := plan.Plan{ConfigureGit: true, CreateSSHIdentity: true}
		if mode == "git-after" {
			p.FullUpgrade = true
		}
		if strings.HasPrefix(mode, "aur") {
			p = declaredParuPlan(t)
			p.ConfigureGit = true
			// The fake's output observation is not needed on this cancelled path.
			ar := &aurOrderRunner{}
			a.Runner = ar
			code = a.preparePlan(ctx, config.Config{Version: 2}, p, terminal)
			if strings.Join(ar.events, ",") != "sudo-v,upgrade" {
				t.Fatalf("later AUR mutation: %v", ar.events)
			}
		} else {
			code = a.preparePlan(ctx, config.Config{Version: 2}, p, terminal)
		}
	}
	for _, call := range runner.calls {
		if (call.Name == "sudo" || call.Name == "pacman-conf") && mode == "git-after" {
			continue
		}
		if call.Name == "pacman" && len(call.Args) > 0 && (call.Args[0] == "-Q" || call.Args[0] == "-Qi" || call.Args[0] == "-Si") {
			continue
		}
		if call.Name == "git" && len(call.Args) > 2 && call.Args[2] == "--get" {
			continue
		}
		t.Fatalf("unexpected later work: %#v", call)
	}
	if flags() != before {
		t.Fatal("cancelled prompt leaked file flags")
	}
	if err := tty.Close(); err != nil {
		t.Fatal(err)
	}
	fmt.Println("NO LATER WORK")
	os.Exit(code)
}

type cancelAfterMutation struct {
	*prepareRunner
	cancel    context.CancelFunc
	mutations int
}

func (r *cancelAfterMutation) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	result, err := r.prepareRunner.Run(ctx, s)
	if s.Name == "sudo" && strings.Join(s.Args, " ") == "-n pacman -Syu" {
		r.mutations++
		r.cancel()
	}
	return result, err
}

func TestCancellationAfterMutationStopsEntireLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &cancelAfterMutation{prepareRunner: &prepareRunner{}, cancel: cancel}
	var out strings.Builder
	a := Runtime{Runner: runner, Out: &out, Err: &out}
	p := plan.Plan{FullUpgrade: true, CorePackages: []string{"git"}, ConfigureGit: true, CreateSSHIdentity: true}
	code := a.preparePlan(ctx, config.Config{}, p, ui.UI{In: strings.NewReader("y\n"), Out: &out})
	if code != Fatal || runner.mutations != 1 || len(runner.calls) != 3 || strings.Contains(out.String(), "Git name:") || strings.Count(out.String(), "Interrupted.") != 1 || strings.Contains(out.String(), "Issues") {
		t.Fatalf("code=%d calls=%v\n%s", code, runner.calls, &out)
	}
}

func TestCancellationWithPendingApprovalDoesNotStartWork(t *testing.T) {
	for _, command := range []string{"setup", "update", "aur"} {
		t.Run(command, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var out strings.Builder
			runner := &prepareRunner{}
			a := Runtime{Runner: runner, Out: &out, Err: &out}
			input := strings.NewReader("y\n")
			marker := "? [Y/n]"
			if command == "aur" {
				input = strings.NewReader("\ny\n")
				marker = "? [y/N]"
			}
			terminal := ui.UI{In: input, Out: cancelOnOutput{Writer: &out, cancel: cancel, marker: marker}}
			var code int
			switch command {
			case "setup":
				code = a.preparePlan(ctx, config.Config{}, plan.Plan{FullUpgrade: true, ConfigureGit: true}, terminal)
			case "update":
				code = a.installUpdate(ctx, release.Client{}, "9.0.0", terminal)
			case "aur":
				application := declaredParuPlan(t).Applications[0]
				if err := a.reviewAUR(ctx, terminal, application, map[string]string{"PKGBUILD": "source"}); err == nil || ctx.Err() == nil {
					t.Fatalf("cancelled review approved: %v", err)
				}
				code = Fatal
			}
			if code != Fatal || len(runner.calls) != 0 || input.Len() != len("y\n") || strings.Contains(out.String(), "Git name:") {
				t.Fatalf("code=%d calls=%v unread=%d\n%s", code, runner.calls, input.Len(), &out)
			}
		})
	}
}

func TestCancellationBetweenApprovalAndMutationDoesNotRunCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out strings.Builder
	runner := &prepareRunner{}
	a := Runtime{Runner: runner, Out: cancelOnOutput{Writer: &out, cancel: cancel, marker: "Updating system..."}, Err: &out}
	code := a.preparePlan(ctx, config.Config{}, plan.Plan{FullUpgrade: true, ConfigureGit: true}, ui.UI{In: strings.NewReader("y\n"), Out: &out})
	// Sudo authorization happened, but the cancelled upgrade must never start.
	if code != Fatal || len(runner.calls) != 1 || runner.calls[0].Name != "sudo" || strings.Join(runner.calls[0].Args, " ") != "-v" {
		t.Fatalf("code=%d calls=%v\n%s", code, runner.calls, &out)
	}
}

type cancelAfterGitName struct {
	*prepareRunner
	cancel context.CancelFunc
}

func (r cancelAfterGitName) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	result, err := r.prepareRunner.Run(ctx, s)
	if s.Name == "git" && strings.Join(s.Args, " ") == "config --global user.name User" {
		r.cancel()
	}
	return result, err
}

func TestCancellationBetweenGitWritesPreservesCompletedName(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out strings.Builder
	runner := cancelAfterGitName{prepareRunner: &prepareRunner{}, cancel: cancel}
	a := Runtime{Runner: runner, Out: &out, Err: &out}
	p := plan.Plan{ConfigureGit: true, CreateSSHIdentity: true}
	code := a.preparePlan(ctx, config.Config{}, p, ui.UI{In: strings.NewReader("y\nUser\nuser@example.com\n"), Out: &out})
	if code != Fatal || runner.gitName != "User" || runner.gitEmail != "" || !strings.Contains(out.String(), "Earlier changes may remain") || strings.Contains(out.String(), "Creating SSH key") {
		t.Fatalf("code=%d name=%q email=%q\n%s", code, runner.gitName, runner.gitEmail, &out)
	}
}

func TestSignalAfterFinalReportDoesNotAppendContradictoryConclusion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out strings.Builder
	a := Runtime{Out: cancelOnOutput{Writer: &out, cancel: cancel, marker: "Workstation already ready."}, Err: &out}
	p := plan.Plan{GitStatus: "ready", SSHStatus: "ready", GitHubStatus: "ready"}
	if code := a.preparePlan(ctx, config.Config{}, p, ui.UI{}); code != Success || out.String() != "Workstation already ready.\n" {
		t.Fatalf("code=%d output=%s", code, &out)
	}
}
