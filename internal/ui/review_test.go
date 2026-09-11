package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReviewVisitsEveryPageAndKeepsApprovalInputSeparate(t *testing.T) {
	files := []ReviewFile{{"PKGBUILD", "build() { helper; }"}, {"fix.patch", strings.Repeat("+patched\n", 20)}, {"helper\x1b", "echo unsafe\r\x1b[31m\u2603\n" + strings.Repeat("x", 150)}}
	input := strings.NewReader("\nb\n\n\n\n\ny\n")
	var output bytes.Buffer
	if err := (UI{In: input, Out: &output}).Review(context.Background(), ReviewSource{Package: "example", PackageBase: "example", Revision: "abc"}, files); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PKGBUILD", "fix.patch", "+patched", "helper\\x1b", "unsafe\\r\\x1b[31m\\u2603", "finish review", "q: skip application"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q: %s", want, &output)
		}
	}
	if strings.Count(output.String(), "\nPKGBUILD\n") != 2 {
		t.Fatal("back navigation did not revisit the source")
	}
	if rest, _ := io.ReadAll(input); string(rest) != "y\n" {
		t.Fatalf("pager consumed approval: %q", rest)
	}
	if strings.ContainsAny(output.String(), "\x1b\r") {
		t.Fatalf("unsafe controls: %q", output.String())
	}
}

func TestReviewProvenanceCannotInjectTerminalChrome(t *testing.T) {
	var output bytes.Buffer
	source := ReviewSource{Package: "declared\nApprove?", PackageBase: "base\x1b[2J", Revision: "revision\r\x00"}
	err := (UI{In: strings.NewReader("\n"), Out: &output}).Review(context.Background(), source, []ReviewFile{{"PKGBUILD", "source"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Package: declared\nApprove?`, `Package base: base\x1b[2J`, `Revision: revision\r\x00`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing escaped %q: %s", want, &output)
		}
	}
	if strings.ContainsAny(output.String(), "\x1b\r\x00") {
		t.Fatalf("unsafe provenance: %q", output.String())
	}
}

type failedReviewWriter struct{}

func (failedReviewWriter) Write([]byte) (int, error) { return 0, errors.New("terminal unavailable") }

func TestReviewFailsClosedOnCancelEOFOrOutputFailure(t *testing.T) {
	files := []ReviewFile{{"PKGBUILD", "instructions"}}
	for _, input := range []string{"q\n", "", "y\n"} {
		var output bytes.Buffer
		err := (UI{In: strings.NewReader(input), Out: &output}).Review(context.Background(), ReviewSource{Package: "example", PackageBase: "example", Revision: "abc"}, files)
		if err == nil {
			t.Fatalf("input %q bypassed review", input)
		}
		if input == "q\n" && !errors.Is(err, ErrReviewCancelled) {
			t.Fatal(err)
		}
	}
	input := strings.NewReader("\ny\n")
	if err := (UI{In: input, Out: failedReviewWriter{}}).Review(context.Background(), ReviewSource{Package: "example", PackageBase: "example", Revision: "abc"}, files); err == nil || input.Len() != len("\ny\n") {
		t.Fatalf("failed output consumed input: %v", err)
	}
}

func TestReviewCancellationStopsBeforeApproval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		done <- (UI{In: input, Out: io.Discard}).Review(ctx, ReviewSource{}, []ReviewFile{{"PKGBUILD", "source"}})
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled review remained blocked")
	}
}

func TestReviewRestoresRealTerminalScreen(t *testing.T) {
	if os.Getenv("OPS_TEST_REVIEW_TTY") == "1" {
		tty, err := OpenTTY()
		if err != nil {
			t.Fatal(err)
		}
		defer tty.Close()
		fd := tty.Fd()
		flags := func() uintptr {
			value, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
			if errno != 0 {
				t.Fatal(errno)
			}
			return value
		}
		before := flags()
		approved, err := (UI{In: tty, Out: tty}).Confirm(context.Background(), "Continue?", true)
		if err != nil || approved {
			t.Fatalf("confirmation=%v err=%v", approved, err)
		}
		if flags() != before {
			t.Fatal("prompt leaked file flags")
		}
		native := exec.Command("sh", "-c", `IFS= read -r answer; test "$answer" = native`)
		native.Stdin, native.Stdout, native.Stderr = tty, tty, tty
		if err := native.Run(); err != nil {
			t.Fatalf("native terminal input: %v", err)
		}
		err = (UI{In: tty, Out: tty}).Review(context.Background(), ReviewSource{}, []ReviewFile{{"PKGBUILD", "echo safe\x1b[31m"}, {"helper.sh", "echo helper"}})
		if !errors.Is(err, ErrReviewCancelled) {
			t.Fatalf("err=%v", err)
		}
		if flags() != before {
			t.Fatal("review leaked file flags")
		}
		return
	}
	program, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script is unavailable")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := "'" + strings.ReplaceAll(binary, "'", "'\\''") + "' -test.run '^TestReviewRestoresRealTerminalScreen$'"
	cmd := exec.Command(program, "-q", "-e", "-c", command, "/dev/null")
	cmd.Env = append(os.Environ(), "OPS_TEST_REVIEW_TTY=1", "TERM=xterm")
	cmd.Stdin = strings.NewReader("n\nnative\n\nb\n\nq\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("terminal test: %v: %s", err, output)
	}
	for _, want := range []string{"\x1b[?1049h", "\x1b[H\x1b[2J", "safe\\x1b[31m", "\x1b[?1049l"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("missing terminal boundary %q in %q", want, output)
		}
	}
	if strings.Count(string(output), "AUR source review (1/2)") != 2 || strings.Count(string(output), "AUR source review (2/2)") != 2 {
		t.Fatalf("terminal Enter/back navigation failed: %q", output)
	}
}
