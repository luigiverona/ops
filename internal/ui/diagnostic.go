package ui

import (
	"strings"

	"github.com/luigiverona/ops/internal/run"
)

// A dozen lines retains compiler context and makepkg's final error without
// turning default output into a build log. Apply the byte bound after escaping
// so control characters and very long lines cannot expand the display limit.
const DiagnosticLines = 12
const DiagnosticBytes = 2048

// DiagnosticExcerpt treats external text only as information. Sensitive-looking
// output is withheld as a whole, including multiline private key material.
// Callers must still opt out of credentials and configuration-dump commands;
// pattern matching cannot make arbitrary output safe to disclose.
func DiagnosticExcerpt(value string, omitted bool) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if value == run.WithheldDiagnostic || run.SensitiveDiagnostic(value) {
		return run.WithheldDiagnostic
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
