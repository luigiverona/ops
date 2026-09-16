package archtrust

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/luigiverona/ops/internal/run"
)

var fingerprint = regexp.MustCompile(`^[0-9A-F]{40}$`)

type keyMaterial struct {
	public, trusted, revoked []byte
}

func systemKeys() (keyMaterial, error) {
	root, err := os.Open("/")
	if err != nil {
		return keyMaterial{}, err
	}
	defer root.Close()
	var keys keyMaterial
	for name, dest := range map[string]*[]byte{"archlinux.gpg": &keys.public, "archlinux-trusted": &keys.trusted, "archlinux-revoked": &keys.revoked} {
		*dest, err = readRegular(root, "usr/share/pacman/keyrings/"+name, 16<<20)
		if err != nil {
			return keyMaterial{}, fmt.Errorf("read official keyring %s: %w", name, err)
		}
	}
	return keys, nil
}

// officialSignature uses only distribution public keys and main-key roots. It
// creates no secret key and cannot contact a keyserver. All GnuPG writes are to
// a disposable home, never the user's home or pacman's live GPG directory.
func officialSignature(ctx context.Context, runner run.Runner, keys keyMaterial, signature []byte, archive io.Reader) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	roots, revoked, err := trustPolicy(keys)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "ops-arch-signature-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	for name, data := range map[string][]byte{"archive.sig": signature, "official.gpg": keys.public} {
		if len(data) == 0 || len(data) > 16<<20 {
			return "", fmt.Errorf("empty or oversized official signature material")
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return "", err
		}
	}
	if bytes.HasPrefix(keys.public, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
		_, err := runner.Run(ctx, run.Spec{Name: "gpg", Args: []string{"--no-options", "--homedir", dir, "--batch", "--yes", "--no-autostart", "--dearmor", "--output", filepath.Join(dir, "official.gpg")}, Stdin: bytes.NewReader(keys.public)})
		if err != nil {
			return "", fmt.Errorf("decode official public keyring: %w", err)
		}
	}
	base := []string{"--no-options", "--homedir", dir, "--batch", "--no-tty", "--no-auto-key-retrieve", "--no-auto-key-import", "--no-autostart", "--no-default-keyring", "--keyring", filepath.Join(dir, "official.gpg"), "--trust-model", "always", "--no-auto-check-trustdb"}
	invoke := func(args []string, input io.Reader) (run.Result, error) {
		return runner.Run(ctx, run.Spec{Name: "gpg", Args: append(append([]string(nil), base...), args...), Stdin: input})
	}
	result, err := invoke([]string{"--status-fd", "1", "--verify", filepath.Join(dir, "archive.sig"), "-"}, archive)
	if err != nil {
		return "", fmt.Errorf("official archive signature failed: %w (%s)", err, strings.TrimSpace(result.Stderr))
	}
	signer, err := signatureStatus(result.Stdout, revoked)
	if err != nil {
		return "", err
	}
	// Membership alone is insufficient: require verified certifications by at
	// least three distinct current Arch main keys on the same user ID.
	rootArgs := append([]string{"--with-colons", "--list-keys", "--"}, strings.Fields(roots)...)
	rootResult, err := invoke(rootArgs, nil)
	if err != nil {
		return "", err
	}
	current, err := currentRoots(rootResult.Stdout, revoked, time.Now().Unix())
	if err != nil {
		return "", err
	}
	certs, err := invoke([]string{"--with-colons", "--no-sig-cache", "--check-sigs", "--", signer}, nil)
	if err != nil {
		return "", err
	}
	if err := certifiedSigner(certs.Stdout, signer, current, time.Now().Unix()); err != nil {
		return "", err
	}
	return signer, nil
}

func trustPolicy(keys keyMaterial) (string, map[string]bool, error) {
	revoked := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(keys.revoked)), "\n") {
		if line == "" {
			continue
		}
		if !fingerprint.MatchString(line) || revoked[line] {
			return "", nil, fmt.Errorf("malformed official revoked key list")
		}
		revoked[line] = true
	}
	var trust strings.Builder
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(keys.trusted)), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) != 3 || !fingerprint.MatchString(parts[0]) || parts[1] != "4" || parts[2] != "" || seen[parts[0]] || revoked[parts[0]] {
			return "", nil, fmt.Errorf("malformed or revoked official main-key root")
		}
		seen[parts[0]] = true
		trust.WriteString(parts[0] + "\n")
	}
	if len(seen) < 3 || len(keys.public) == 0 {
		return "", nil, fmt.Errorf("incomplete official keyring")
	}
	return trust.String(), revoked, nil
}

func signatureStatus(output string, revoked map[string]bool) (string, error) {
	valid, good := 0, 0
	var signer string
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "[GNUPG:]" {
			return "", fmt.Errorf("malformed official signature status")
		}
		switch fields[1] {
		case "VALIDSIG":
			// fingerprint, date, timestamp, expiry, version, reserved, pubkey
			// algorithm, hash algorithm, signature class, primary fingerprint.
			if len(fields) != 12 || !fingerprint.MatchString(fields[2]) || !fingerprint.MatchString(fields[11]) || revoked[fields[2]] || revoked[fields[11]] || (fields[9] != "8" && fields[9] != "9" && fields[9] != "10" && fields[9] != "11") || fields[10] != "00" {
				return "", fmt.Errorf("unsupported or revoked official archive signer")
			}
			valid++
			signer = fields[11]
		case "GOODSIG":
			good++
		case "NEWSIG", "KEY_CONSIDERED", "SIG_ID":
		default:
			return "", fmt.Errorf("unacceptable official signature status %s", fields[1])
		}
	}
	if valid != 1 || good != 1 {
		return "", fmt.Errorf("missing or ambiguous official signature trust")
	}
	return signer, nil
}

func sameKeys(a, b keyMaterial) bool {
	return bytes.Equal(a.public, b.public) && bytes.Equal(a.trusted, b.trusted) && bytes.Equal(a.revoked, b.revoked)
}

// GnuPG's colon format supplies full fingerprints, current key expiration and
// revocation state. Only requested main roots can appear in this inventory.
func currentRoots(output string, revoked map[string]bool, now int64) (map[string]bool, error) {
	roots := map[string]bool{}
	usable := false
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, ":")
		if f[0] == "tru" {
			continue
		}
		if len(f) < 10 {
			return nil, fmt.Errorf("malformed main-key inventory")
		}
		switch f[0] {
		case "pub":
			usable = f[1] != "r" && f[1] != "e" && f[1] != "d" && validTime(f[5], f[6], now)
		case "fpr":
			if usable && fingerprint.MatchString(f[9]) && !revoked[f[9]] {
				roots[f[9]] = true
			}
			usable = false
		case "sub":
			usable = false
		}
	}
	if len(roots) < 3 {
		return nil, fmt.Errorf("insufficient current official main keys")
	}
	return roots, nil
}

func validTime(created, expires string, now int64) bool {
	start, err := strconv.ParseInt(created, 10, 64)
	if err != nil || start <= 0 || start > now {
		return false
	}
	if expires == "" || expires == "0" {
		return true
	}
	end, err := strconv.ParseInt(expires, 10, 64)
	return err == nil && end > now
}

func certifiedSigner(output, signer string, roots map[string]bool, now int64) error {
	type certification struct {
		when    int64
		revoked bool
	}
	var groups []map[string]certification
	var group map[string]certification
	primary, pubs := "", 0
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, ":")
		if f[0] == "tru" {
			continue
		}
		if len(f) < 10 {
			return fmt.Errorf("malformed signer certification listing")
		}
		switch f[0] {
		case "pub":
			pubs++
			group = nil
		case "fpr":
			if primary == "" {
				primary = f[9]
			}
		case "uid":
			group = nil
			if f[1] != "r" && f[1] != "e" && validTime(f[5], f[6], now) {
				group = map[string]certification{}
				groups = append(groups, group)
			}
		case "sub", "uat":
			group = nil
		case "sig", "rev":
			if group == nil || len(f) < 16 || f[1] != "!" || !roots[f[12]] || !validTime(f[5], f[6], now) {
				continue
			}
			class := strings.Split(f[10], ",")[0]
			isRevoked := f[0] == "rev" && class == "30x"
			if !isRevoked && (f[0] != "sig" || (class != "10x" && class != "11x" && class != "12x" && class != "13x")) {
				continue
			}
			// SHA-1 certifications cannot introduce a package signer.
			if !isRevoked && f[15] != "8" && f[15] != "9" && f[15] != "10" && f[15] != "11" {
				continue
			}
			when, _ := strconv.ParseInt(f[5], 10, 64)
			old := group[f[12]]
			if when >= old.when || isRevoked {
				group[f[12]] = certification{when: when, revoked: old.revoked || isRevoked}
			}
		}
	}
	if pubs != 1 || primary != signer {
		return fmt.Errorf("ambiguous signer certification identity")
	}
	for _, group := range groups {
		valid := 0
		for _, cert := range group {
			if !cert.revoked {
				valid++
			}
		}
		if valid >= 3 {
			return nil
		}
	}
	return fmt.Errorf("package signer lacks three current official main-key certifications")
}
