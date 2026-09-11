package run

import (
	"strings"
	"testing"
	"time"
)

func TestWave3ReviewPrivacyBoundaries(t *testing.T) {
	markers := []string{"Authorization: Bearer abc", "token=abc", "access_token=abc", "password=abc", "secret=abc", "PRIVATE KEY", "BEGIN OPENSSH PRIVATE KEY", "AWS_SECRET_ACCESS_KEY=abc", "GH_TOKEN=abc", "GITHUB_TOKEN=abc", "Authorization" + strings.Repeat(" ", 1500) + ": Bearer abc"}
	for _, marker := range markers {
		for _, offset := range []int{0, 500, 507, 511, 16380} {
			value := strings.Repeat(". ", offset/2) + marker + "\n" + strings.Repeat("private material with spaces\n", 1000)
			var b diagnosticBuffer
			for i := 0; i < len(value); i++ {
				b.Write([]byte(value[i : i+1]))
			}
			if b.String() != WithheldDiagnostic {
				t.Errorf("marker %.30q offset %d leaked tail", marker, offset)
			}
		}
	}
	for _, control := range []string{"\u200b", "\u009b31m", "\u202e"} {
		for offset := 490; offset < 520; offset++ {
			value := strings.Repeat(".", offset) + " Author" + control + "ization: Bearer abc\n" + strings.Repeat("private material with spaces\n", 1000)
			var b diagnosticBuffer
			b.Write([]byte(value))
			if b.String() != WithheldDiagnostic {
				t.Errorf("unicode %q offset %d leaked tail", control, offset)
			}
		}
	}
}

func TestWave3ReviewStress(t *testing.T) {
	for _, tiny := range []bool{false, true} {
		var b diagnosticBuffer
		start := time.Now()
		if tiny {
			for i := 0; i < 200000; i++ {
				b.Write([]byte{'.'})
				if len(b.data) > diagnosticLimit {
					t.Fatal("unbounded")
				}
			}
		} else {
			b.Write([]byte(strings.Repeat("build output. ", 800000)))
		}
		b.Write([]byte("\nfinal failure"))
		if len(b.data) > diagnosticLimit || !strings.HasSuffix(b.String(), "final failure") {
			t.Fatal("tail lost")
		}
		t.Logf("tiny=%v duration=%s retained=%d capacity=%d", tiny, time.Since(start), len(b.data), cap(b.data))
	}
}

func TestDiagnosticRetentionExactBoundaries(t *testing.T) {
	for _, size := range []int{0, 1, diagnosticLimit - 1, diagnosticLimit, diagnosticLimit + 1, 2 * diagnosticLimit} {
		value := strings.Repeat(".", size)
		for _, chunk := range []int{1, 511, 512, diagnosticLimit + 1} {
			var b diagnosticBuffer
			for start := 0; start < len(value); start += chunk {
				b.Write([]byte(value[start:min(start+chunk, len(value))]))
			}
			if b.String() != value[max(0, len(value)-diagnosticLimit):] || b.truncated != (size > diagnosticLimit) {
				t.Fatalf("size=%d chunk=%d tail=%d truncated=%v", size, chunk, len(b.String()), b.truncated)
			}
			if len(b.privacy.window) > 512 || len(b.privacy.pending) > 512 || cap(b.data) > 2*diagnosticLimit {
				t.Fatal("unbounded allocation")
			}
		}
	}
}

func TestPrivacyWithholdsUnterminatedEscapeAtEnd(t *testing.T) {
	var b diagnosticBuffer
	b.Write([]byte("\x1b]0;" + strings.Repeat(".", 300)))
	if b.String() != WithheldDiagnostic {
		t.Fatal("ambiguous final escape was not withheld")
	}
}
