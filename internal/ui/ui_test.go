package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestConfirmationEOFNeverGrantsApproval(t *testing.T) {
	for _, input := range []string{"", "y", "invalid"} {
		ok, err := (UI{In: strings.NewReader(input), Out: io.Discard}).Confirm(context.Background(), "Proceed?", true)
		if ok || err == nil {
			t.Fatalf("input=%q approved=%v err=%v", input, ok, err)
		}
	}
	ok, err := (UI{In: strings.NewReader("\n"), Out: io.Discard}).Confirm(context.Background(), "Proceed?", true)
	if !ok || err != nil {
		t.Fatal("intentional blank line should retain documented default")
	}
}

func TestCancelledPromptsNeverReadOrPrint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	input := strings.NewReader("y\nname\n")
	var out bytes.Buffer
	u := UI{In: input, Out: &out}
	approved, err := u.Confirm(ctx, "Continue?", true)
	if approved || !errors.Is(err, context.Canceled) {
		t.Fatalf("approved=%v err=%v", approved, err)
	}
	if _, err := u.Ask(ctx, "Git name:"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := u.Review(ctx, ReviewSource{}, []ReviewFile{{"PKGBUILD", "source"}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if out.Len() != 0 || input.Len() != len("y\nname\n") {
		t.Fatal("cancelled prompt consumed input or printed")
	}
}

func TestAskRequiresCompleteLine(t *testing.T) {
	for _, input := range []string{"", "partial"} {
		value, err := (UI{In: strings.NewReader(input), Out: io.Discard}).Ask(context.Background(), "Git name:")
		if value != "" || !errors.Is(err, io.EOF) {
			t.Fatalf("input=%q value=%q err=%v", input, value, err)
		}
	}
}

func TestRepeatedCancellationLeavesNoReaderOrConsumedInput(t *testing.T) {
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	before := runtime.NumGoroutine()
	for range 30 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		approved, err := (UI{In: input, Out: io.Discard}).Confirm(ctx, "Continue?", true)
		cancel()
		if approved || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("approved=%v err=%v", approved, err)
		}
	}
	if _, err := writer.Write([]byte("y\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	approved, err := (UI{In: input, Out: io.Discard}).Confirm(ctx, "Continue?", false)
	if !approved || err != nil {
		t.Fatalf("stale reader consumed new input: %v %v", approved, err)
	}
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("reader goroutines leaked: before=%d after=%d", before, after)
	}
}

func TestRenderFieldsEscapesControlsAndAlignsMultilineValues(t *testing.T) {
	got := RenderFields([]Field{{Name: "cause", Value: "first\nsecond\x1b[31m\u2603"}, {Name: "impact", Value: "safe"}})
	want := "  cause   first\n          second\\x1b[31m\\u2603\n  impact  safe\n"
	if got != want {
		t.Fatalf("fields\n--- got ---\n%s--- want ---\n%s", got, want)
	}
	for _, value := range []byte(got) {
		if value > 0x7f || value == 0x1b {
			t.Fatalf("unsafe structured output: %q", got)
		}
	}
}
