package archtrust

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
)

const testFingerprint = "0123456789ABCDEF0123456789ABCDEF01234567"

func TestSignatureStatusFailsClosed(t *testing.T) {
	valid := "[GNUPG:] NEWSIG\n[GNUPG:] GOODSIG 0123456789ABCDEF signer\n[GNUPG:] VALIDSIG " + testFingerprint + " 2026-09-15 1 0 4 0 22 8 00 " + testFingerprint + "\n"
	if _, err := signatureStatus(valid, nil); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"BADSIG", "ERRSIG", "NO_PUBKEY", "EXPKEYSIG", "EXPSIG", "REVKEYSIG", "TRUST_UNDEFINED", "TRUST_MARGINAL", "FAILURE", "ERROR"} {
		t.Run(status, func(t *testing.T) {
			if _, err := signatureStatus(valid+"[GNUPG:] "+status+" x\n", nil); err == nil {
				t.Fatal("accepted invalid status")
			}
		})
	}
	for _, data := range []string{"", valid + valid, valid + "[GNUPG:] TRUST_NEVER 0 pgp\n", strings.Replace(valid, "22 8 00", "22 2 00", 1), strings.Replace(valid, "22 8 00", "22 8 01", 1)} {
		if _, err := signatureStatus(data, nil); err == nil {
			t.Fatal("accepted malformed, weak, or ambiguous signature")
		}
	}
	if _, err := signatureStatus(valid, map[string]bool{testFingerprint: true}); err == nil {
		t.Fatal("accepted distribution-revoked signer")
	}
}

// These are isolated package-signing test keys, unrelated to ops releases and
// the actual pacman/user keyrings. Ordinary tests never contact the network.
func TestIsolatedOfficialCertificationTrust(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("GnuPG unavailable")
	}
	home := t.TempDir()
	invoke := func(input []byte, args ...string) []byte {
		t.Helper()
		base := []string{"--no-options", "--homedir", home, "--batch", "--no-tty", "--pinentry-mode", "loopback", "--passphrase", ""}
		cmd := exec.Command("gpg", append(base, args...)...)
		cmd.Stdin = bytes.NewReader(input)
		var diagnostics bytes.Buffer
		cmd.Stderr = &diagnostics
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("gpg %v: %v %s", args, err, diagnostics.String())
		}
		return out
	}
	t.Cleanup(func() {
		_ = exec.Command("gpgconf", "--homedir", home, "--kill", "gpg-agent").Run()
	})
	newKey := func(uid string) string {
		t.Helper()
		invoke(nil, "--quick-generate-key", uid, "ed25519", "cert,sign", "0")
		out := invoke(nil, "--with-colons", "--list-keys", uid)
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Split(line, ":")
			if len(fields) > 9 && fields[0] == "fpr" {
				return fields[9]
			}
		}
		t.Fatal("missing generated key fingerprint")
		return ""
	}
	var roots []string
	for _, uid := range []string{"Main One", "Main Two", "Main Three"} {
		roots = append(roots, newKey(uid+" <arch-test@example.invalid>"))
	}
	packager := newKey("Package Builder <package-test@example.invalid>")
	custom := newKey("Locally Trusted Custom <custom-test@example.invalid>")
	archive := []byte("isolated package archive content\n")
	sign := func(key string) []byte {
		t.Helper()
		file := filepath.Join(home, "package.sig")
		invoke(archive, "--yes", "--local-user", key+"!", "--output", file, "--detach-sign")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	trusted := strings.Join(roots, ":4:\n") + ":4:\n"
	material := func() keyMaterial {
		return keyMaterial{public: invoke(nil, "--export"), trusted: []byte(trusted)}
	}
	verify := func(keys keyMaterial, signature, data []byte) error {
		_, err := officialSignature(context.Background(), run.Exec{}, keys, signature, bytes.NewReader(data))
		return err
	}
	signature := sign(packager)
	for i, root := range roots {
		invoke(nil, "--local-user", root, "--quick-sign-key", packager)
		err := verify(material(), signature, archive)
		if i < 2 && err == nil {
			t.Logf("certifications: %s", invoke(nil, "--with-colons", "--check-sigs", packager))
			t.Fatalf("accepted only %d official certifications", i+1)
		}
		if i == 2 && err != nil {
			t.Fatalf("three supported main-key certifications rejected: %v", err)
		}
	}
	keys := material()
	// Exercise the complete archive -> signature -> authenticated manifest ->
	// installed payload path with real GnuPG and libarchive, without network.
	if _, err := exec.LookPath("bsdtar"); err != nil {
		t.Fatal(err)
	}
	payload := []byte("official executable")
	manifest := compressed(t, fmt.Sprintf("./program type=file mode=755 uid=%d gid=%d size=%d sha256digest=%x\n", os.Getuid(), os.Getgid(), len(payload), sha256.Sum256(payload)))
	var buffer bytes.Buffer
	tw := tar.NewWriter(&buffer)
	for _, member := range []struct {
		name string
		data []byte
	}{{".PKGINFO", []byte("pkgname = fixture\npkgver = 1-1\narch = any\n")}, {".MTREE", manifest}, {"program", payload}} {
		if err := tw.WriteHeader(&tar.Header{Name: member.name, Mode: 0755, Size: int64(len(member.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(member.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	archive = buffer.Bytes()
	p := Package{repository: "extra", name: "fixture", version: "1-1", architecture: "any", size: int64(len(archive)), digest: sha256.Sum256(archive), signature: sign(packager)}
	file, err := os.CreateTemp(t.TempDir(), "package-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(archive); err != nil {
		t.Fatal(err)
	}
	authenticated, err := authenticateArchive(context.Background(), run.Exec{}, p, file, keys)
	if err != nil {
		t.Fatal(err)
	}
	rootDir := t.TempDir()
	root, err := os.Open(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, content := range [][]byte{[]byte("forged executable"), payload, payload, []byte("later replacement")} {
		if err := os.WriteFile(filepath.Join(rootDir, "program"), content, 0755); err != nil {
			t.Fatal(err)
		}
		match, err := authenticated.matches(context.Background(), root)
		if err != nil || match != bytes.Equal(content, payload) {
			t.Fatalf("authenticated payload match=%v err=%v", match, err)
		}
	}
	// Continue the signer adversaries using signatures over the current archive.
	signature = sign(packager)
	if err := verify(keys, sign(custom), archive); err == nil {
		t.Fatal("custom locally trusted key accepted as official")
	}
	if err := verify(keys, signature, []byte("changed archive")); err == nil {
		t.Fatal("invalid signature accepted")
	}
	keys.revoked = []byte(packager + "\n")
	if err := verify(keys, signature, archive); err == nil {
		t.Fatal("revoked packager accepted")
	}
	keys = material()
	keys.public = invoke(nil, "--export", roots[0], roots[1], roots[2])
	if err := verify(keys, signature, archive); err == nil {
		t.Fatal("unknown packager accepted")
	}
	// Rotation is driven solely by the next distribution keyring observation.
	// An old cached archive must stop passing as soon as its signer is revoked.
	rotated := newKey("Rotated Package Builder <rotation-test@example.invalid>")
	for _, root := range roots {
		invoke(nil, "--local-user", root, "--quick-sign-key", rotated)
	}
	rotatedSignature := sign(rotated)
	if err := verify(keys, rotatedSignature, archive); err == nil {
		t.Fatal("new signer accepted before distribution keyring update")
	}
	keys = material()
	keys.revoked = []byte(packager + "\n")
	if err := verify(keys, rotatedSignature, archive); err != nil {
		t.Fatalf("certified replacement signer rejected after keyring update: %v", err)
	}
	if err := verify(keys, signature, archive); err == nil {
		t.Fatal("old cached archive survived signer revocation")
	}
	// Exercise real certification revocation packets, not only revoked-list
	// entries or synthetic status output. Two remaining roots are insufficient.
	invoke(nil, "--local-user", roots[0], "--quick-revoke-sig", rotated, roots[0])
	if err := verify(material(), rotatedSignature, archive); err == nil {
		t.Fatal("revoked main-key certification still authorized signer")
	}
}
