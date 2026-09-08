package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestReviewVisitsEveryPageAndKeepsApprovalInputSeparate(t *testing.T) {
	files := []ReviewFile{{"PKGBUILD", "build() { helper; }"}, {"fix.patch", strings.Repeat("+patched\n", 20)}, {"helper\x1b", "echo unsafe\r\x1b[31m\u2603\n" + strings.Repeat("x", 150)}}
	input := strings.NewReader("\nb\n\n\n\n\ny\n")
	var output bytes.Buffer
	if err := (UI{In: input, Out: &output}).Review(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PKGBUILD", "fix.patch", "+patched", "helper\\x1b", "unsafe\\r\\x1b[31m\\u2603", "finish review"} {
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

type failedReviewWriter struct{}

func (failedReviewWriter) Write([]byte) (int, error) { return 0, errors.New("terminal unavailable") }

func TestReviewFailsClosedOnCancelEOFOrOutputFailure(t *testing.T) {
	files := []ReviewFile{{"PKGBUILD", "instructions"}}
	for _, input := range []string{"q\n", "", "y\n"} {
		var output bytes.Buffer
		err := (UI{In: strings.NewReader(input), Out: &output}).Review(context.Background(), files)
		if err == nil {
			t.Fatalf("input %q bypassed review", input)
		}
		if input == "q\n" && !errors.Is(err, ErrReviewCancelled) {
			t.Fatal(err)
		}
	}
	input := strings.NewReader("\ny\n")
	if err := (UI{In: input, Out: failedReviewWriter{}}).Review(context.Background(), files); err == nil || input.Len() != len("\ny\n") {
		t.Fatalf("failed output consumed input: %v", err)
	}
}

func TestReviewCancellationStopsBeforeApproval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() { done <- (UI{In: input, Out: io.Discard}).Review(ctx, []ReviewFile{{"PKGBUILD", "source"}}) }()
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
		err = (UI{In: tty, Out: tty}).Review(context.Background(), []ReviewFile{{"PKGBUILD", "echo safe\x1b[31m"}})
		if !errors.Is(err, ErrReviewCancelled) {
			t.Fatalf("err=%v", err)
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
	cmd.Stdin = strings.NewReader("q\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("terminal test: %v: %s", err, output)
	}
	for _, want := range []string{"\x1b[?1049h", "\x1b[H\x1b[2J", "safe\\x1b[31m", "\x1b[?1049l"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("missing terminal boundary %q in %q", want, output)
		}
	}
}
