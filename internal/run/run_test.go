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
		Name: "sh", Args: []string{"-c", "read value"},
	})
	if err == nil {
		t.Fatal("command unexpectedly read inherited input")
	}
	remaining, readErr := io.ReadAll(inherited)
	if readErr != nil || string(remaining) != "must remain unread\n" {
		t.Fatalf("remaining=%q err=%v", remaining, readErr)
	}
}

func TestExecRefusesTruncatedParsedOutputButAllowsBuildLogs(t *testing.T) {
	for _, allow := range []bool{false, true} {
		result, err := (Exec{}).Run(context.Background(), Spec{Name: "sh", Args: []string{"-c", "head -c 2097153 /dev/zero"}, AllowTruncatedOutput: allow})
		if (err == nil) != allow || len(result.Stdout) != captureLimit {
			t.Fatalf("allow=%v bytes=%d err=%v", allow, len(result.Stdout), err)
		}
	}
}

func TestExecUsesStableLocale(t *testing.T) {
	t.Setenv("LC_ALL", "invalid-locale")
	result, err := (Exec{}).Run(context.Background(), Spec{Name: "sh", Args: []string{"-c", `printf '%s' "$LC_ALL"`}})
	if err != nil || result.Stdout != "C" {
		t.Fatalf("locale=%q err=%v", result.Stdout, err)
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
	var command *Error
	if !errors.As(err, &command) || command.Stderr != "diagnostic" || strings.Contains(err.Error(), "diagnostic") {
		t.Fatalf("noninteractive diagnostic was lost: %v", err)
	}
}

func TestExecStreamsTransactionOutputWithoutConsumingInput(t *testing.T) {
	for _, fail := range []bool{false, true} {
		input := strings.NewReader("reserved for ops\n")
		var out, stderr bytes.Buffer
		command := "if read value; then exit 99; fi; printf transaction; printf diagnostic >&2"
		if fail {
			command += "; exit 1"
		}
		_, err := (Exec{In: input, Out: &out, Err: &stderr}).Run(context.Background(), Spec{
			Name: "sh", Args: []string{"-c", command}, StreamOutput: true,
		})
		if (err != nil) != fail || out.String() != "transaction" || stderr.String() != "diagnostic" || input.Len() != len("reserved for ops\n") {
			t.Fatalf("fail=%v err=%v out=%q stderr=%q unread=%d", fail, err, &out, &stderr, input.Len())
		}
		if fail {
			var commandErr *Error
			if !errors.As(err, &commandErr) || !commandErr.Presented || commandErr.Stderr != "diagnostic" {
				t.Fatalf("streamed failure lost stderr: %v", err)
			}
		}
	}
}

func TestStreamingDoesNotAuthorizeTruncatedInspection(t *testing.T) {
	for _, allow := range []bool{false, true} {
		result, err := (Exec{Out: io.Discard, Err: io.Discard}).Run(context.Background(), Spec{
			Name: "sh", Args: []string{"-c", "head -c 2097153 /dev/zero"},
			StreamOutput: true, AllowTruncatedOutput: allow,
		})
		if (err == nil) != allow || len(result.Stdout) != captureLimit {
			t.Fatalf("allow=%v len=%d err=%v", allow, len(result.Stdout), err)
		}
	}
}

func TestFailureEvidenceOptInAndStreams(t *testing.T) {
	for _, mode := range []FailureOutput{FailureNone, FailureStderr, FailureCombined} {
		for _, streamed := range []bool{false, true} {
			_, err := (Exec{Out: io.Discard, Err: io.Discard}).Run(context.Background(), Spec{
				Name: "sh", Args: []string{"-c", "printf 'src/main.c: undefined reference to symbol\n'; printf '==> ERROR: A failure occurred in build().\n' >&2; exit 1"},
				FailureOutput: mode, StreamOutput: streamed,
			})
			var failure *Error
			if !errors.As(err, &failure) {
				t.Fatal(err)
			}
			if strings.Contains(failure.Error(), "reference") || strings.Contains(failure.Error(), "ERROR") {
				t.Fatal("evidence embedded in cause")
			}
			if mode == FailureNone || streamed {
				if failure.Evidence != "" {
					t.Fatal("unapproved replay")
				}
				continue
			}
			if !strings.Contains(failure.Evidence, "A failure occurred") {
				t.Fatal("lost stderr")
			}
			if strings.Contains(failure.Evidence, "undefined reference") != (mode == FailureCombined) {
				t.Fatal("incorrect stdout policy")
			}
		}
	}
}

func TestDiagnosticRetentionIsBoundedAndKeepsTail(t *testing.T) {
	_, err := (Exec{}).Run(context.Background(), Spec{Name: "sh", Args: []string{"-c", "head -c 2097153 /dev/zero; printf 'final build error'; exit 1"}, AllowTruncatedOutput: true, FailureOutput: FailureCombined})
	var failure *Error
	if !errors.As(err, &failure) || len(failure.Evidence) != diagnosticLimit || !failure.EvidenceTruncated || !strings.HasSuffix(failure.Evidence, "final build error") {
		t.Fatalf("incorrect retained evidence: %v", err)
	}
}

func TestEvidenceDoesNotExposeUnapprovedOutputOrArguments(t *testing.T) {
	_, err := (Exec{}).Run(context.Background(), Spec{Name: "sh", Args: []string{"-c", "printf 'secret-token-from-stdout'; printf 'ordinary diagnostic' >&2; exit 1", "private-argument"}, FailureOutput: FailureStderr})
	var failure *Error
	if !errors.As(err, &failure) || failure.Evidence != "ordinary diagnostic" {
		t.Fatalf("evidence=%v", err)
	}
	if strings.Contains(err.Error(), "private-argument") || strings.Contains(failure.Evidence, "secret-token") {
		t.Fatal("private command context exposed")
	}
}
