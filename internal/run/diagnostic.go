package run

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var sensitiveDiagnostic = regexp.MustCompile(`(?im)(authorization\s*:|proxy-authorization\s*:|cookie\s*:|private key|\b(?:[a-z_][a-z0-9_]*token|token|password|passwd|secret|credential|api[_-]?key)["']?\s*[:=]|--[a-z_-]*(?:token|password|passwd|secret|credential|api[_-]?key)\b|^[ \t+]*(?:export[ \t]+)?[A-Z_][A-Z0-9_]*=|https?://[^\s/]+@|[?&](?:token|key|secret|password)=|\bgh[pousr]_[A-Za-z0-9_]+|\bgithub_pat_[A-Za-z0-9_]+|^[A-Za-z0-9+/=]{64,}$)`)

var diagnosticANSI = regexp.MustCompile(`(?:\x1b\[|\x{009b})[0-?]*[ -/]*[@-~]|(?:\x1b\]|\x{009d})[^\a\x1b\x{009c}]*(?:\a|\x1b\\|\x{009c})`)

var diagnosticWhitespace = regexp.MustCompile(`\s+`)

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
			// Leave an incomplete UTF-8 rune for the next block. Mapping it
			// now would replace its bytes and lose a split control character.
			end := len(s.pending)
			start := end - 1
			for start > 0 && !utf8.RuneStart(s.pending[start]) {
				start--
			}
			if !utf8.FullRune(s.pending[start:]) {
				end = start
			}
			value := s.window + string(s.pending[:end])
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
			// Whitespace is unbounded in several marker patterns. Compact it
			// before retaining overlap, preserving line-start semantics.
			value = diagnosticWhitespace.ReplaceAllStringFunc(value, func(space string) string {
				if strings.ContainsRune(space, '\n') {
					return "\n"
				}
				return " "
			})
			s.withheld = s.withheld || SensitiveDiagnostic(value)
			// A very long or malformed sequence cannot safely be normalized
			// within our bounded overlap. Withhold instead of guessing.
			if i := strings.IndexAny(value, "\x1b\u009b\u009d"); i >= 0 && len(value)-i > 256 {
				s.withheld = true
			}
			s.window = value[max(0, len(value)-512):]
			s.pending = s.pending[:copy(s.pending, s.pending[end:])]
		}
	}
}

func (s *privacyScan) sensitive() bool {
	value := s.window + string(s.pending)
	plain := diagnosticANSI.ReplaceAllString(value, "")
	if i := strings.IndexAny(plain, "\x1b\u009b\u009d"); i >= 0 && len(plain)-i > 256 {
		return true
	}
	return s.withheld || SensitiveDiagnostic(value)
}
