package run

import (
	"regexp"
	"strings"
	"unicode"
)

var sensitiveDiagnostic = regexp.MustCompile(`(?im)(authorization\s*:|proxy-authorization\s*:|cookie\s*:|private key|\b(?:[a-z_][a-z0-9_]*token|token|password|passwd|secret|credential|api[_-]?key)["']?\s*[:=]|--[a-z_-]*(?:token|password|passwd|secret|credential|api[_-]?key)\b|^[ \t+]*(?:export[ \t]+)?[A-Z_][A-Z0-9_]*=|https?://[^\s/]+@|[?&](?:token|key|secret|password)=|\bgh[pousr]_[A-Za-z0-9_]+|\bgithub_pat_[A-Za-z0-9_]+|^[A-Za-z0-9+/=]{64,}$)`)

var diagnosticANSI = regexp.MustCompile(`(?:\x1b\[|\x{009b})[0-?]*[ -/]*[@-~]|(?:\x1b\]|\x{009d})[^\a\x1b\x{009c}]*(?:\a|\x1b\\|\x{009c})`)

// SensitiveDiagnostic is a conservative additional filter, never permission
// to disclose arbitrary output. Command boundaries must opt in separately.
func SensitiveDiagnostic(value string) bool {
	if sensitiveDiagnostic.MatchString(value) {
		return true
	}
	// Check a second view so color codes and control characters cannot split
	// common secret markers. Presentation still escapes the original text.
	plain := diagnosticANSI.ReplaceAllString(value, "")
	plain = strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
			return -1
		}
		return r
	}, plain)
	return sensitiveDiagnostic.MatchString(plain)
}

const WithheldDiagnostic = "[output withheld: potentially sensitive content]"

// Remember sensitive markers before a tail buffer can drop them. The overlap
// handles markers split across writes; no complete log is retained.
type privacyScan struct {
	window   string
	pending  []byte
	withheld bool
}

func (s *privacyScan) write(p []byte) {
	for len(p) > 0 && !s.withheld {
		n := min(len(p), 512-len(s.pending))
		s.pending = append(s.pending, p[:n]...)
		p = p[n:]
		if len(s.pending) == 512 {
			value := s.window + string(s.pending)
			s.withheld = SensitiveDiagnostic(value)
			// Collapse complete escape sequences before keeping the overlap.
			// Repeated color/control bytes must not push a secret marker out
			// of the scanner before the remainder of that marker arrives.
			value = diagnosticANSI.ReplaceAllString(value, "")
			value = strings.Map(func(r rune) rune {
				// Keep escape introducers so a sequence split across blocks
				// can be recognized when its terminator arrives.
				if r != '\n' && r != '\t' && r != '\x1b' && r != '\u009b' && r != '\u009d' && r != '\u009c' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
					return -1
				}
				return r
			}, value)
			// A very long or malformed sequence cannot safely be normalized
			// within our bounded overlap. Withhold instead of guessing.
			if i := strings.IndexAny(value, "\x1b\u009b\u009d"); i >= 0 && len(value)-i > 256 {
				s.withheld = true
			}
			s.window = value[max(0, len(value)-512):]
			s.pending = s.pending[:0]
		}
	}
}

func (s *privacyScan) sensitive() bool {
	return s.withheld || SensitiveDiagnostic(s.window+string(s.pending))
}
