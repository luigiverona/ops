package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestExecCapturesNormalChildOutput(t *testing.T) {
	var out, errOut bytes.Buffer
	result, err := (Exec{Out: &out, Err: &errOut}).Run(context.Background(), Spec{Name: "sh", Args: []string{"-c", "printf stdout; printf stderr >&2"}})
	if err != nil || result.Stdout != "stdout" || result.Stderr != "stderr" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("noninteractive child leaked: out=%q err=%q", out.String(), errOut.String())
	}
}

func TestExecRequiresDeclaredTerminalBoundary(t *testing.T) {
	var out, errOut bytes.Buffer
	exec := Exec{Out: &out, Err: &errOut}
	if _, err := exec.Run(context.Background(), Spec{Name: "sh", Args: []string{"-c", "printf hidden"}, Interactive: true}); err == nil {
		t.Fatal("undeclared interactive child was allowed")
	}
	if _, err := exec.Run(context.Background(), Spec{Name: "sh", Args: []string{"-c", "printf visible; printf warning >&2"}, Interactive: true, Interaction: "test terminal ownership"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "visible" || errOut.String() != "warning" {
		t.Fatalf("declared interactive stream was not retained: out=%q err=%q", out.String(), errOut.String())
	}
}

func TestExecEOFStdinDoesNotConsumeInheritedInput(t *testing.T) {
	inherited := strings.NewReader("must remain unread\n")
	_, err := (Exec{In: inherited}).Run(context.Background(), Spec{
		Name: "sh", Args: []string{"-c", "read value"}, Stdin: strings.NewReader(""),
	})
	if err == nil {
		t.Fatal("command unexpectedly read inherited input")
	}
	remaining, readErr := io.ReadAll(inherited)
	if readErr != nil || string(remaining) != "must remain unread\n" {
		t.Fatalf("remaining=%q err=%v", remaining, readErr)
	}
}

func TestExecInteractiveErrorDoesNotRepeatPresentedStderr(t *testing.T) {
	var errOut bytes.Buffer
	result, err := (Exec{Err: &errOut}).Run(context.Background(), Spec{
		Name: "sh", Args: []string{"-c", "printf raw-diagnostic >&2; exit 1"},
		Interactive: true, Interaction: "test terminal ownership",
	})
	var commandErr *Error
	if !errors.As(err, &commandErr) || !commandErr.Presented || commandErr.Stderr != "raw-diagnostic" {
		t.Fatalf("err=%#v", err)
	}
	if result.Stderr != "raw-diagnostic" || errOut.String() != "raw-diagnostic" {
		t.Fatalf("result=%#v stderr=%q", result, errOut.String())
	}
	if strings.Contains(commandErr.Error(), "raw-diagnostic") {
		t.Fatalf("presented stderr was repeated in structured error: %q", commandErr)
	}
}

func TestExecNoninteractiveErrorRetainsCapturedStderr(t *testing.T) {
	_, err := (Exec{}).Run(context.Background(), Spec{Name: "sh", Args: []string{"-c", "printf diagnostic >&2; exit 1"}})
	if err == nil || !strings.Contains(err.Error(), "diagnostic") {
		t.Fatalf("noninteractive diagnostic was lost: %v", err)
	}
}
