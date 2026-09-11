package ui

import (
	"fmt"
	"strings"
	"testing"
)

func TestDiagnosticExcerptContracts(t *testing.T) {
	var build strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&build, "Compiling module %02d\n", i)
	}
	build.WriteString("src/main.c:42: undefined reference to symbol\ncollect2: error: ld returned 1 exit status\n==> ERROR: A failure occurred in build().\n    Aborting...\n")
	for _, tc := range []struct {
		name, input, want string
		omitted           bool
	}{
		{"empty", "", "", false},
		{"whitespace", " \n\t", "", false},
		{"package", "error: failed to commit transaction (conflicting files)\n", "error: failed to commit transaction (conflicting files)", false},
		{"lines", build.String(), "    Aborting...", true},
		{"bytes", strings.Repeat("long/path/", 700) + " fatal linker failure", " fatal linker failure", true},
		{"controls", "\x1b[31mERROR\x1b[0m\n\x1b]0;hostile title\a\rrewrite\x00\x7f\u009b2J\u009dtitle\u009c", `\x1b[31mERROR\x1b[0m`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DiagnosticExcerpt(tc.input, false)
			if got != DiagnosticExcerpt(tc.input, false) {
				t.Fatal("nondeterministic excerpt")
			}
			if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
				t.Fatalf("excerpt=%q", got)
			}
			if strings.Contains(got, "[earlier output omitted]") != tc.omitted {
				t.Fatalf("truncation=%q", got)
			}
			content := strings.TrimPrefix(got, "[earlier output omitted]\n")
			if len(content) > DiagnosticBytes || len(strings.Split(content, "\n")) > DiagnosticLines {
				t.Fatalf("unbounded excerpt: %d bytes", len(content))
			}
			for _, r := range got {
				if r != '\n' && (r < 32 || r > 126) {
					t.Fatalf("unsafe rune %U", r)
				}
			}
			if tc.name == "lines" && strings.Contains(got, "module 00") {
				t.Fatal("selected head instead of tail")
			}
		})
	}
	if got := DiagnosticExcerpt("last error", true); got != "[earlier output omitted]\nlast error" {
		t.Fatal(got)
	}
}

func TestDiagnosticWithholdsSensitiveContent(t *testing.T) {
	for _, line := range []string{
		"Authorization: Bearer secret-value", "Proxy-Authorization: basic opaque", "Cookie: session=secret",
		"GH_TOKEN=secret-value", "PASSWORD=secret-value", "api_key: secret-value", "https://user:secret@example.org/file",
		"https://example.org/file?token=secret", "ghp_1234567890abcdef", "github_pat_1234_secret",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nopaque material\n-----END OPENSSH PRIVATE KEY-----",
		"Author\x1b[31mization\x1b[0m: Bearer secret-value",
		"pass\rword: secret-value", "--access-token secret-value",
		"--password secret-value", "--api-key secret-value",
	} {
		got := DiagnosticExcerpt("build started\n"+line+"\n==> ERROR: Aborting", false)
		if got != "[output withheld: potentially sensitive content]" {
			t.Fatalf("sensitive evidence=%q", got)
		}
	}
}

func TestDiagnosticEscapesEveryTerminalControl(t *testing.T) {
	var input strings.Builder
	for r := rune(0); r < 32; r++ {
		if r != '\n' {
			input.WriteRune(r)
		}
	}
	for r := rune(0x7f); r <= 0x9f; r++ {
		input.WriteRune(r)
	}
	got := DiagnosticExcerpt(input.String(), false)
	for _, r := range got {
		if r < 32 || r > 126 {
			t.Fatalf("raw terminal control %U in %q", r, got)
		}
	}
	for _, want := range []string{`\r`, `\x1b`, `\u009b`, `\u009d`, `\u009c`} {
		if !strings.Contains(got, want) {
			t.Fatalf("control disappeared: %s", want)
		}
	}
}

func TestDiagnosticPreservesOrdinarySourceQueryReason(t *testing.T) {
	value := `could not query the declared source: Get "https://archlinux.org/packages/search/json/?name=example&arch=x86_64": network unavailable`
	if got := DiagnosticExcerpt(value, false); got != value {
		t.Fatalf("ordinary query was hidden: %s", got)
	}
}
