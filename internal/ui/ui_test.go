package ui

import (
	"io"
	"strings"
	"testing"
)

func TestConfirmationEOFNeverGrantsApproval(t *testing.T) {
	for _, input := range []string{"", "y", "invalid"} {
		ok, err := (UI{In: strings.NewReader(input), Out: io.Discard}).Confirm("Proceed?", true)
		if ok || err == nil {
			t.Fatalf("input=%q approved=%v err=%v", input, ok, err)
		}
	}
	ok, err := (UI{In: strings.NewReader("\n"), Out: io.Discard}).Confirm("Proceed?", true)
	if !ok || err != nil {
		t.Fatal("intentional blank line should retain documented default")
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
