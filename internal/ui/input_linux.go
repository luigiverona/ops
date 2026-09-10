package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// readLine owns input only until this line ends. File input uses bounded poll
// and nonblocking reads, including after a native child has used the TTY.
// Canonical mode, echo, and signal generation remain untouched. No reader
// goroutine can survive cancellation or consume a later tool's input.
// Non-file inputs are restricted to in-memory readers, which cannot block.
func readLine(ctx context.Context, in io.Reader) (line string, returnErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var read func([]byte) (int, error)
	switch input := in.(type) {
	case *os.File:
		conn, err := input.SyscallConn()
		if err != nil {
			return "", err
		}
		// Fd() can clear O_NONBLOCK before it can be saved. Keep all raw
		// descriptor use inside Control, including restoration, so Close
		// cannot invalidate the descriptor while a prompt owns it.
		err = conn.Control(func(fd uintptr) {
			line, returnErr = readFileLine(ctx, fd)
		})
		return line, errors.Join(returnErr, err)
	case *strings.Reader, *bytes.Reader, *bytes.Buffer:
		read = in.Read
	default:
		return "", errors.New("prompt input requires a file or in-memory reader")
	}
	return readPromptLine(ctx, read)
}

func readPromptLine(ctx context.Context, read func([]byte) (int, error)) (string, error) {
	var b strings.Builder
	buffer := []byte{0}
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := read(buffer)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil {
			return "", err
		}
		if n == 1 {
			if buffer[0] == '\n' {
				return b.String(), nil
			}
			b.WriteByte(buffer[0])
		}
	}
}

// Callers own the input exclusively; native children use it only between prompts.
func readFileLine(ctx context.Context, fd uintptr) (line string, returnErr error) {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
	if errno != 0 {
		return "", errno
	}
	if err := syscall.SetNonblock(int(fd), true); err != nil {
		return "", err
	}
	defer func() {
		_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETFL, flags)
		if errno != 0 {
			returnErr = errors.Join(returnErr, errno)
			line = ""
		}
	}()
	read := func(buffer []byte) (int, error) {
		for {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			poll := struct {
				fd              int32
				events, revents int16
			}{fd: int32(fd), events: 1} // POLLIN
			_, _, errno := syscall.Syscall(syscall.SYS_POLL, uintptr(unsafe.Pointer(&poll)), 1, 50)
			if errno == syscall.EINTR {
				continue
			}
			if errno != 0 {
				return 0, errno
			}
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if poll.revents == 0 {
				continue
			}
			n, err := syscall.Read(int(fd), buffer)
			if err == syscall.EAGAIN || err == syscall.EINTR {
				continue
			}
			if n == 0 && err == nil {
				err = io.EOF
			}
			return n, err
		}
	}
	return readPromptLine(ctx, read)
}
