// Package release downloads and verifies ops releases with an isolated GPG trust root and SHA-256.
package release

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/luigiverona/ops/internal/run"
)

const (
	BinaryName    = "ops-linux-x86_64"
	ChecksumsName = "checksums.txt"
	SignatureName = "checksums.txt.sig"
	DefaultBase   = "https://ops.luigiverona.dev/releases"
)

// These are intentionally unconfigured until the project creates its offline
// primary key and release-signing subkey. Release builds must set both.
var SigningFingerprint = ""
var SigningPublicKey = ""

type Trust struct {
	Fingerprint string
	PublicKey   string
}

type Client struct {
	HTTP    *http.Client
	Runner  run.Runner
	BaseURL string
	Trust   Trust
}

type Verified struct {
	Version string
	Binary  string
	Dir     string
}

func DefaultTrust() Trust { return Trust{Fingerprint: SigningFingerprint, PublicKey: SigningPublicKey} }

func (c Client) Latest(ctx context.Context) (string, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = DefaultBase
	}
	data, err := c.download(ctx, base+"/latest", 128)
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(data))
	if !validVersion(version) {
		return "", fmt.Errorf("release service returned invalid version %q", version)
	}
	return version, nil
}

// DownloadVerified downloads all artifacts, verifies manifest signature first,
// then verifies the binary checksum and executable version.
func (c Client) DownloadVerified(ctx context.Context, version string) (*Verified, error) {
	if !validVersion(version) {
		return nil, errors.New("invalid release version")
	}
	trust := c.Trust
	if trust.Fingerprint == "" && trust.PublicKey == "" {
		trust = DefaultTrust()
	}
	if !validFingerprint(trust.Fingerprint) || !strings.Contains(trust.PublicKey, "BEGIN PGP PUBLIC KEY BLOCK") {
		return nil, errors.New("release trust is not configured; refusing unverified release")
	}
	dir, err := os.MkdirTemp("", "ops-release-*")
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = DefaultBase
	}
	base += "/" + version
	artifacts := []struct {
		name  string
		limit int64
	}{
		{ChecksumsName, 1 << 20}, {SignatureName, 1 << 20}, {BinaryName, 256 << 20},
	}
	for _, artifact := range artifacts {
		data, err := c.download(ctx, base+"/"+artifact.name, artifact.limit)
		if err != nil {
			return nil, fmt.Errorf("download %s: %w", artifact.name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, artifact.name), data, 0o600); err != nil {
			return nil, err
		}
	}
	keyPath := filepath.Join(dir, "signing-key.asc")
	keyring := filepath.Join(dir, "trustedkeys.gpg")
	if err := os.WriteFile(keyPath, []byte(trust.PublicKey), 0o600); err != nil {
		return nil, err
	}
	show, err := c.Runner.Run(ctx, run.Spec{Name: "gpg", Args: []string{"--batch", "--with-colons", "--show-keys", keyPath}})
	if err != nil {
		return nil, fmt.Errorf("inspect release signing key: %w", err)
	}
	if !fingerprintPresent(show.Stdout, trust.Fingerprint) {
		return nil, errors.New("release signing key fingerprint does not match pinned fingerprint")
	}
	if _, err := c.Runner.Run(ctx, run.Spec{Name: "gpg", Args: []string{"--batch", "--no-default-keyring", "--keyring", keyring, "--import", keyPath}, Env: []string{"GNUPGHOME=" + dir}}); err != nil {
		return nil, fmt.Errorf("import isolated release key: %w", err)
	}
	verifiedSignature, err := c.Runner.Run(ctx, run.Spec{Name: "gpgv", Args: []string{"--status-fd", "1", "--keyring", keyring, filepath.Join(dir, SignatureName), filepath.Join(dir, ChecksumsName)}, Env: []string{"GNUPGHOME=" + dir}})
	if err != nil {
		return nil, fmt.Errorf("release signature verification failed: %w", err)
	}
	if !validSignaturePresent(verifiedSignature.Stdout, trust.Fingerprint) {
		return nil, errors.New("release was not signed by the pinned release-signing key")
	}
	expected, err := ManifestChecksum(filepath.Join(dir, ChecksumsName), BinaryName)
	if err != nil {
		return nil, err
	}
	binary := filepath.Join(dir, BinaryName)
	actual, err := FileChecksum(binary)
	if err != nil {
		return nil, err
	}
	if actual != expected {
		return nil, errors.New("release binary checksum verification failed")
	}
	if err := os.Chmod(binary, 0o755); err != nil {
		return nil, err
	}
	result, err := c.Runner.Run(ctx, run.Spec{Name: binary, Args: []string{"--version"}})
	if err != nil || strings.TrimSpace(result.Stdout) != "ops "+version {
		return nil, errors.New("verified release binary reports an unexpected version")
	}
	ok = true
	return &Verified{Version: version, Binary: binary, Dir: dir}, nil
}

func (v *Verified) Close() error { return os.RemoveAll(v.Dir) }

func (c Client) download(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ops/1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	reader := io.LimitReader(resp.Body, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("download exceeds size limit")
	}
	return data, nil
}

func ManifestChecksum(path, filename string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var found string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return "", errors.New("invalid checksum manifest")
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name != filename {
			continue
		}
		if found != "" {
			return "", errors.New("duplicate binary entry in checksum manifest")
		}
		if len(fields[0]) != 64 {
			return "", errors.New("invalid SHA-256 in checksum manifest")
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", errors.New("invalid SHA-256 in checksum manifest")
		}
		found = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("binary is absent from checksum manifest")
	}
	return found, nil
}

func FileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func CompareVersions(a, b string) (int, error) {
	if !validVersion(a) || !validVersion(b) {
		return 0, errors.New("invalid semantic version")
	}
	pa := versionParts(a)
	pb := versionParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] < pb[i] {
			return -1, nil
		}
		if pa[i] > pb[i] {
			return 1, nil
		}
	}
	return 0, nil
}

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
var fingerprintPattern = regexp.MustCompile(`^[0-9A-F]{40}$`)

func validVersion(value string) bool { return versionPattern.MatchString(value) }
func validFingerprint(value string) bool {
	return fingerprintPattern.MatchString(strings.ToUpper(value))
}
func versionParts(value string) [3]int {
	fields := strings.Split(value, ".")
	var result [3]int
	for i := range result {
		result[i], _ = strconv.Atoi(fields[i])
	}
	return result
}
func fingerprintPresent(output, want string) bool {
	want = strings.ToUpper(want)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 9 && fields[0] == "fpr" && strings.ToUpper(fields[9]) == want {
			return true
		}
	}
	return false
}

func validSignaturePresent(output, want string) bool {
	want = strings.ToUpper(want)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "[GNUPG:]" && fields[1] == "VALIDSIG" && strings.ToUpper(fields[2]) == want {
			return true
		}
	}
	return false
}
