package flatpak

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

// This exact keyring was independently reproduced by two Flatpak 1.18.2
// bootstraps. A different serialization or key rotation requires review; we do
// not infer identity from a key ID or implement an OpenPGP parser.
const flathubKeyringSHA256 = "c504fa5dc891df6cfcc10021dd9addf08459007f1787f40b9c6fd3c7e58416ea"
const identityFileLimit = 1024 * 1024

// Open each directory relative to the previous descriptor: no symlinks, even
// in ancestor components, and no check/open race that could follow a link.
func openIdentityDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("Flatpak installation path must be absolute")
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/") {
		if part == "" {
			continue
		}
		next, e := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		syscall.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

// O_PATH inspects file type without opening a device or waiting on a FIFO.
// Reopening that descriptor through proc retains its identity. NOATIME keeps
// even access timestamps unchanged; inability to honor it fails closed.
func readIdentityFile(dir *os.File, name string) ([]byte, error) {
	const oPath = 0x200000 // Linux O_PATH (not exported by syscall on amd64).
	fd, err := syscall.Openat(int(dir.Fd()), name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > identityFileLimit {
		return nil, fmt.Errorf("unsupported Flatpak identity file type or size")
	}
	readFD, err := syscall.Open(fmt.Sprintf("/proc/self/fd/%d", fd), syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC|syscall.O_NOATIME, 0)
	if err != nil {
		return nil, err
	}
	reader := os.NewFile(uintptr(readFD), name)
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, identityFileLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > identityFileLimit {
		return nil, fmt.Errorf("Flatpak identity file exceeds size limit")
	}
	return data, nil
}

func userInstallation() (string, error) {
	if path := os.Getenv("FLATPAK_USER_DIR"); path != "" {
		return path, nil
	}
	if path := os.Getenv("XDG_DATA_HOME"); path != "" {
		return filepath.Join(path, "flatpak"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "flatpak"), nil
}

func inspectRemoteIdentity(remotes map[string]Remote) error {
	path, err := userInstallation()
	if err != nil {
		return err
	}
	dir, err := openIdentityDirectory(filepath.Join(path, "repo"))
	if os.IsNotExist(err) && len(remotes) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	data, err := readIdentityFile(dir, "config")
	if os.IsNotExist(err) && len(remotes) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	groups, err := parseKeyfile(data)
	if err != nil {
		return err
	}
	fields, exists := groups[`remote "flathub"`]
	remote, listed := remotes["flathub"]
	if exists != listed {
		return fmt.Errorf("Flathub configuration changed during inspection; rerun ops")
	}
	// Parent repositories can supply inherited options and keyrings, including
	// remotes absent from this file. They are outside the supported user contract.
	if groups["core"]["parent"] != "" {
		return fmt.Errorf("inherited Flatpak repository configuration is unsupported")
	}
	if !exists {
		return nil
	}
	key, err := readIdentityFile(dir, "flathub.trustedkeys.gpg")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	trusted, disabled, err := supportedFlathub(groups, key)
	if err != nil {
		return err
	}
	// A cookie jar can impose source access restrictions independently of config.
	cookies, err := readIdentityFile(dir, "flathub.cookies.txt")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(cookies) != 0 {
		trusted = false
	}
	after, err := readIdentityFile(dir, "config")
	if err != nil {
		return err
	}
	if !bytes.Equal(data, after) || remote.URL != fields["url"] || remote.Enabled == disabled {
		return fmt.Errorf("Flathub configuration changed during inspection; rerun ops")
	}
	for name, before := range map[string][]byte{"flathub.trustedkeys.gpg": key, "flathub.cookies.txt": cookies} {
		after, err := readIdentityFile(dir, name)
		if (err != nil && !os.IsNotExist(err)) || !bytes.Equal(before, after) {
			return fmt.Errorf("Flathub trust material changed during inspection; rerun ops")
		}
	}
	current, err := openIdentityDirectory(filepath.Join(path, "repo"))
	if err != nil {
		return err
	}
	defer current.Close()
	beforeInfo, beforeErr := dir.Stat()
	afterInfo, afterErr := current.Stat()
	if beforeErr != nil || afterErr != nil || !os.SameFile(beforeInfo, afterInfo) {
		return fmt.Errorf("Flatpak repository changed during inspection; rerun ops")
	}
	remote.SourceTrusted = trusted
	remotes["flathub"] = remote
	return nil
}

// The contract is the full, normal OSTree Flathub bootstrap, with the observed
// public keyring, commit verification and summary verification, and no collection
// ID. Collection-based trust is a different contract, not a substitute for the
// summary verification required here. Missing gpg-verify defaults true in OSTree;
// missing gpg-verify-summary defaults false. Empty collection/subset/filter strings
// are normalized by Flatpak to absence. A stored subset "-" is NOT absence.
func supportedFlathub(groups map[string]map[string]string, key []byte) (trusted, disabled bool, err error) {
	fields := groups[`remote "flathub"`]
	trusted = fields["url"] == FlathubRepositoryURL && fmt.Sprintf("%x", sha256.Sum256(key)) == flathubKeyringSHA256
	core := groups["core"]
	if core["repo_version"] != "1" || core["mode"] != "bare-user-only" {
		trusted = false
	}
	for k := range core {
		switch k {
		case "repo_version", "mode", "min-free-space-size", "min-free-space-percent", "fsync", "locking", "lock-timeout-secs":
		default:
			trusted = false
		}
	}
	for k, raw := range fields {
		switch k {
		case "url":
		case "gpg-verify", "gpg-verify-summary", "xa.disable", "xa.oci", "tls-permissive", "xa.noenumerate", "xa.nodeps", "xa.subset-is-set",
			"xa.title-is-set", "xa.comment-is-set", "xa.description-is-set", "xa.homepage-is-set", "xa.icon-is-set":
			value, e := keyfileBoolean(raw)
			if e != nil {
				return false, false, fmt.Errorf("malformed Flathub boolean %s", k)
			}
			switch k {
			case "gpg-verify", "gpg-verify-summary":
				trusted = trusted && value
			case "xa.disable":
				disabled = value
			case "xa.oci", "tls-permissive", "xa.noenumerate", "xa.nodeps":
				trusted = trusted && !value
			}
		case "collection-id", "xa.subset", "xa.filter":
			value, e := keyfileString(raw)
			if e != nil {
				return false, false, e
			}
			trusted = trusted && value == ""
		case "xa.title", "xa.comment", "xa.description", "xa.homepage", "xa.icon":
			if _, e := keyfileString(raw); e != nil {
				return false, false, e
			}
		default:
			// Unknown options fail closed, including contenturl, metalink, gpgkeypath,
			// custom-backend, TLS anchors/client credentials, branches, authenticators,
			// default branch/token type, signature lookaside and future xa.* options.
			trusted = false
		}
	}
	if _, exists := fields["gpg-verify-summary"]; !exists {
		trusted = false
	}
	return trusted, disabled, nil
}

func keyfileBoolean(value string) (bool, error) {
	switch strings.Trim(value, " \t\r") {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("invalid keyfile boolean")
	}
}

func keyfileString(value string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\\' {
			i++
			if i == len(value) {
				return "", fmt.Errorf("incomplete keyfile escape")
			}
			switch value[i] {
			case 's':
				c = ' '
			case 'n':
				c = '\n'
			case 't':
				c = '\t'
			case 'r':
				c = '\r'
			case '\\':
				c = '\\'
			default:
				return "", fmt.Errorf("unsupported keyfile string escape")
			}
		}
		out.WriteByte(c)
	}
	return out.String(), nil
}

// A deliberately narrow GLib keyfile reader. Values retain trailing whitespace
// and escapes until interpreted by their typed getter. No INI inline comments,
// quoting, interpolation or continuation. Duplicate groups/keys are rejected
// rather than adopting GLib's last-value-wins behavior for security evidence.
func parseKeyfile(data []byte) (map[string]map[string]string, error) {
	if len(data) > identityFileLimit || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("invalid Flatpak keyfile size or encoding")
	}
	groups := map[string]map[string]string{}
	var group map[string]string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimLeft(line, " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line[0] == '[' {
			header := strings.TrimRight(line, " \t\r")
			if !strings.HasSuffix(header, "]") {
				return nil, fmt.Errorf("malformed Flatpak keyfile group")
			}
			name := header[1 : len(header)-1]
			if name == "" || strings.ContainsAny(name, "[]\r\t") {
				return nil, fmt.Errorf("unsupported Flatpak keyfile group")
			}
			if _, exists := groups[name]; exists {
				return nil, fmt.Errorf("duplicate Flatpak keyfile group")
			}
			group = map[string]string{}
			groups[name] = group
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimRight(key, " \t\r")
		if !ok || group == nil || key == "" || strings.ContainsAny(key, " []\t\r") {
			return nil, fmt.Errorf("malformed Flatpak keyfile entry")
		}
		if _, exists := group[key]; exists {
			return nil, fmt.Errorf("duplicate Flatpak keyfile key")
		}
		group[key] = strings.TrimLeft(value, " \t")
	}
	return groups, nil
}
