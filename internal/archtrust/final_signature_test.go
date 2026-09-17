package archtrust

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFinalSignatureLifetime(t *testing.T) {
	now := time.Now().Unix()
	for _, tc := range []struct {
		start, end string
		valid      bool
	}{
		{fmt.Sprint(now - 1), "0", true}, {fmt.Sprint(now + 3600), "0", false},
		{"bad", "0", false}, {"0", "0", false}, {"1", "bad", false},
		{"1", fmt.Sprint(now - 1), false}, {"1", fmt.Sprint(now + 3600), true},
	} {
		status := fmt.Sprintf("[GNUPG:] GOODSIG 0123456789ABCDEF signer\n[GNUPG:] VALIDSIG %s 2026-09-16 %s %s 4 0 22 8 00 %s\n", testFingerprint, tc.start, tc.end, testFingerprint)
		_, err := signatureStatus(status, nil)
		if (err == nil) != tc.valid {
			t.Fatalf("start=%s end=%s err=%v", tc.start, tc.end, err)
		}
	}
}

func TestFinalCertificationPolicy(t *testing.T) {
	now := int64(2000)
	roots := map[string]bool{"root1": true, "root2": true, "root3": true}
	record := func(kind, validity, created, expires, class, issuer, hash string) string {
		f := make([]string, 17)
		f[0] = kind
		f[1] = validity
		f[5] = created
		f[6] = expires
		f[10] = class
		f[12] = issuer
		f[15] = hash
		return strings.Join(f, ":") + "\n"
	}
	header := "pub:-:::::::::\nfpr:::::::::" + testFingerprint + ":\n"
	uid := record("uid", "-", "100", "", "", "", "")
	cert := func(root string) string { return record("sig", "!", "100", "", "13x", root, "8") }
	three := cert("root1") + cert("root2") + cert("root3")
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"three", uid + three, true}, {"duplicate", uid + cert("root1") + cert("root1") + cert("root2"), false},
		{"non-main", uid + cert("root1") + cert("root2") + cert("custom"), false},
		{"split UIDs", uid + cert("root1") + cert("root2") + uid + cert("root3"), false},
		{"usable second UID", uid + cert("root1") + uid + three, true},
		{"UID revoked", record("uid", "r", "100", "", "", "", "") + three, false},
		{"UID expired", record("uid", "-", "100", "1000", "", "", "") + three, false},
		{"cert revoked", uid + three + record("rev", "!", "1000", "", "30x", "root3", "8"), false},
		{"cert expired", uid + cert("root1") + cert("root2") + record("sig", "!", "100", "1000", "13x", "root3", "8"), false},
		{"cert future", uid + cert("root1") + cert("root2") + record("sig", "!", "3000", "", "13x", "root3", "8"), false},
		{"cert weak", uid + cert("root1") + cert("root2") + record("sig", "!", "100", "", "13x", "root3", "2"), false},
		{"invalid cert", uid + cert("root1") + cert("root2") + record("sig", "-", "100", "", "13x", "root3", "8"), false},
		{"malformed", "uid:bad\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := certifiedSigner(header+tc.body, testFingerprint, roots, now)
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}
