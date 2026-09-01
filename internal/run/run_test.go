package run

import (
	"bytes"
	"context"
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
