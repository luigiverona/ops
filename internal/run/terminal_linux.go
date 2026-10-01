package run

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// sudo may put the caller's terminal into raw mode while relaying its PTY.
// cgroup.kill also kills sudo's monitor, so it cannot restore that mode itself.
// Snapshot only the supplied terminal; never change foreground groups/sessions.
func terminalRecovery(in io.Reader) (func() error, error) {
	f, ok := in.(*os.File)
	if !ok {
		return func() error { return nil }, nil
	}
	var state syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&state)))
	runtime.KeepAlive(f)
	if errno == syscall.ENOTTY {
		return func() error { return nil }, nil
	}
	if errno != 0 {
		return nil, fmt.Errorf("snapshot interactive terminal: %w", errno)
	}
	return func() error {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCSETS, uintptr(unsafe.Pointer(&state)))
		runtime.KeepAlive(f)
		if errno != 0 {
			return fmt.Errorf("restore interactive terminal: %w", errno)
		}
		return nil
	}, nil
}

func (e Exec) recoverTerminal(restore func() error, commandErr error) error {
	// Successful interactive commands keep their usual terminal semantics.
	if commandErr == nil || restore == nil {
		return commandErr
	}
	if err := restore(); err != nil {
		if e.Owner != nil {
			err = e.Owner.fail(err)
		}
		return errors.Join(commandErr, err)
	}
	return commandErr
}
