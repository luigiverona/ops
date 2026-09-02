package ui

import (
	"strings"
	"testing"
)

func TestRenderTableUsesActualContentWidths(t *testing.T) {
	rows := []TableRow{
		{Item: "bitwarden", Action: "install", Detail: "pacman"},
		{Item: "com.tutanota.Tutanota", Action: "install", Detail: "flatpak"},
		{Item: "an-application-identifier-longer-than-the-other-rows", Action: "review", Detail: "aur"},
	}
	want := "  bitwarden                                             install  pacman\n" +
		"  com.tutanota.Tutanota                                 install  flatpak\n" +
		"  an-application-identifier-longer-than-the-other-rows  review   aur\n"
	if got := RenderTable(rows); got != want {
		t.Fatalf("table mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
	for _, line := range strings.Split(strings.TrimSuffix(want, "\n"), "\n") {
		if strings.Contains(line, "Tutanotainstall") || strings.Contains(line, "rowsreview") {
			t.Fatalf("columns collided: %q", line)
		}
	}
}

func TestRenderTableIsPlainASCII(t *testing.T) {
	output := RenderTable([]TableRow{{Item: "bad\x1b[31m\u2603", Action: "configure", Detail: "managed\tidentity"}})
	for _, value := range []byte(output) {
		if value > 0x7f {
			t.Fatalf("non-ASCII byte in %q", output)
		}
	}
	if want := "  bad\\x1b[31m\\u2603  configure  managed\\tidentity\n"; output != want {
		t.Fatalf("escaped table = %q, want %q", output, want)
	}
}

func TestRenderTableAlignsDetailWhenActionIsEmpty(t *testing.T) {
	rows := []TableRow{
		{Item: "short", Action: "install", Detail: "pacman"},
		{Item: "longer item", Detail: "detail only"},
	}
	want := "  short        install  pacman\n" +
		"  longer item           detail only\n"
	if got := RenderTable(rows); got != want {
		t.Fatalf("table mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
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
