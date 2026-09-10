package ui

import (
	"regexp"
	"strings"
)

// A dozen lines retains compiler context and makepkg's final error without
// turning default output into a build log. Apply the byte bound after escaping
// so control characters and very long lines cannot expand the display limit.
const DiagnosticLines = 12
const DiagnosticBytes = 2048

var sensitiveDiagnostic = regexp.MustCompile(`(?im)(authorization:|proxy-authorization:|cookie:|private key|\b(?:[a-z_][a-z0-9_]*token|token|password|passwd|secret|credential|api[_-]?key)["']?\s*[:=]|^[ \t+]*(?:export[ \t]+)?[A-Z_][A-Z0-9_]*=|https?://[^\s/]+@|[?&](?:token|key|secret|password)=|\bgh[pousr]_[A-Za-z0-9_]+|\bgithub_pat_[A-Za-z0-9_]+|^[A-Za-z0-9+/=]{64,}$)`)

// DiagnosticExcerpt treats external text only as information. Sensitive-looking
// output is withheld as a whole, including multiline private key material.
// Callers must still opt out of credentials and configuration-dump commands;
// pattern matching cannot make arbitrary output safe to disclose.
func DiagnosticExcerpt(value string, omitted bool) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if sensitiveDiagnostic.MatchString(value) {
		return "[output withheld: potentially sensitive content]"
	}
	// Bound work even for injected runners and non-command errors.
	if len(value) > 16*1024 {
		value, omitted = value[len(value)-16*1024:], true
	}
	value = strings.Trim(value, "\n")
	lines := strings.Split(value, "\n")
	if len(lines) > DiagnosticLines {
		lines, omitted = lines[len(lines)-DiagnosticLines:], true
	}
	for i := range lines {
		lines[i] = PrintableASCII(lines[i])
	}
	value = strings.Join(lines, "\n")
	if len(value) > DiagnosticBytes {
		value, omitted = value[len(value)-DiagnosticBytes:], true
	}
	if omitted {
		value = "[earlier output omitted]\n" + value
	}
	return value
}
