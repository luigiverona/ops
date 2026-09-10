package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
	"time"
)

// Inspect through SyscallConn: File.Fd itself can clear O_NONBLOCK.
func inputFlags(t *testing.T, file *os.File) uintptr {
	t.Helper()
	conn, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags uintptr
	if err := conn.Control(func(fd uintptr) {
		var errno syscall.Errno
		flags, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
		if errno != 0 {
			t.Fatal(errno)
		}
	}); err != nil {
		t.Fatal(err)
	}
	return flags
}

func TestPromptPreservesPreexistingNonblockingFlags(t *testing.T) {
	for _, mode := range []string{"success", "eof", "validation", "cancel", "read error"} {
		t.Run(mode, func(t *testing.T) {
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer writer.Close()
			if mode == "read error" {
				input, err = os.OpenFile(t.TempDir()+"/write-only", os.O_CREATE|os.O_WRONLY|syscall.O_NONBLOCK, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				defer input.Close()
			}
			conn, err := input.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.Control(func(fd uintptr) {
				flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
				if errno == 0 {
					_, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETFL, flags|syscall.O_APPEND)
				}
				if errno != 0 {
					t.Fatal(errno)
				}
			}); err != nil {
				t.Fatal(err)
			}
			before := inputFlags(t, input)
			if before&syscall.O_NONBLOCK == 0 {
				t.Fatal("fixture must start nonblocking")
			}
			ctx := context.Background()
			if mode == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 10*time.Millisecond)
				defer cancel()
			}
			switch mode {
			case "success":
				_, err = writer.Write([]byte("value\n"))
			case "validation":
				_, err = writer.Write([]byte("\n"))
			case "eof":
				err = writer.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			value, err := (UI{In: input, Out: io.Discard}).Ask(ctx, "Value:")
			if mode == "success" && (value != "value" || err != nil) {
				t.Fatalf("value=%q err=%v", value, err)
			}
			if mode != "success" && err == nil {
				t.Fatal("expected error")
			}
			if mode == "cancel" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if mode == "read error" && !errors.Is(err, syscall.EBADF) {
				t.Fatalf("expected actual read error: %v", err)
			}
			if after := inputFlags(t, input); after != before {
				t.Fatalf("flags changed: before=%#x after=%#x", before, after)
			}
		})
	}
}
