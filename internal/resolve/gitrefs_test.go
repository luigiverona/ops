package resolve

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAdvertisedHEADFailsClosed(t *testing.T) {
	const oid = "0123456789012345678901234567890123456789"
	const prefix = "001e# service=git-upload-pack\n0000"
	head := packet(oid + " HEAD\x00object-format=sha1\n")
	for name, data := range map[string]string{
		"HTML": "<html>challenge</html>", "truncated": prefix + "00ffbroken", "missing flush": prefix + head,
		"trailing": prefix + head + "0000extra", "duplicate": prefix + head + packet(oid+" HEAD\n") + "0000",
		"zero oid":         prefix + packet(strings.Repeat("0", 40)+" HEAD\x00x\n") + "0000",
		"no HEAD":          prefix + packet(oid+" refs/heads/main\x00x\n") + "0000",
		"bad capabilities": prefix + packet(oid+" HEAD\n") + "0000", "delimiter": prefix + "0001",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := advertisedHEAD([]byte(data)); err == nil {
				t.Fatal("accepted malformed advertisement")
			}
		})
	}
}

func TestAURUnavailableAndMalformedMetadata(t *testing.T) {
	for _, tt := range []struct {
		name, body     string
		status         int
		transportError bool
	}{
		{"outage", "", 503, false}, {"offline", "", 0, true}, {"count mismatch", `{"resultcount":1,"results":[]}`, 200, false},
		{"multiple JSON values", `{} {}`, 200, false}, {"oversize", strings.Repeat("x", 2*1024*1024+1), 200, false},
		{"unsafe base", `{"resultcount":1,"results":[{"Name":"pkg","PackageBase":"../escape"}]}`, 200, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
				if tt.transportError {
					return nil, errors.New("offline")
				}
				return &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			})}
			if _, _, err := (Resolver{Client: client}).AUR(context.Background(), "pkg"); err == nil {
				t.Fatal("accepted unavailable or malformed source")
			}
		})
	}
}
