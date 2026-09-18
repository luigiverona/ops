package archtrust

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/luigiverona/ops/internal/run"
)

var ErrArchiveMismatch = errors.New("cache archive does not match authenticated official content")

type entry struct {
	name, kind, link string
	mode             uint32
	uid, gid         uint32
	size             int64
	digest           [32]byte
	backup           bool
}

// authenticatedArchive is not constructible outside this package. The manifest
// is read only after authenticating the full archive, not from local .MTREE.
type authenticatedArchive struct {
	pkg     Package
	entries []entry
}

func authenticateArchive(ctx context.Context, runner run.Runner, p Package, f *os.File, keys keyMaterial) (authenticatedArchive, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var empty authenticatedArchive
	before, err := f.Stat()
	if err != nil {
		return empty, fmt.Errorf("inspect official archive: %w", err)
	}
	if !before.Mode().IsRegular() || before.Size() != p.size || before.Sys().(*syscall.Stat_t).Nlink != 1 {
		return empty, ErrArchiveMismatch
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return empty, err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(contextReader{ctx, f}, p.size+1))
	if err != nil {
		return empty, fmt.Errorf("read official archive: %w", err)
	}
	if n != p.size || !bytes.Equal(h.Sum(nil), p.digest[:]) {
		return empty, ErrArchiveMismatch
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return empty, err
	}
	if _, err := officialSignature(ctx, runner, keys, p.signature, f); err != nil {
		return empty, err
	}
	// /proc refers to this process's open descriptor, so cache-path replacement
	// cannot switch the archive supplied to the native reader.
	archivePath := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), f.Fd())
	readMember := func(name string) ([]byte, error) {
		r, err := runner.Run(ctx, run.Spec{Name: "bsdtar", Args: []string{"-xOf", archivePath, "--", name}})
		if err != nil {
			return nil, err
		}
		return []byte(r.Stdout), nil
	}
	info, err := readMember(".PKGINFO")
	if err != nil {
		return empty, err
	}
	backups, err := packageInfo(p, info)
	if err != nil {
		return empty, err
	}
	mtree, err := readMember(".MTREE")
	if err != nil {
		return empty, err
	}
	entries, err := parseManifest(mtree, backups)
	if err != nil {
		return empty, err
	}
	after, err := f.Stat()
	if err != nil || !unchanged(before, after) {
		return empty, fmt.Errorf("official archive changed during authentication")
	}
	return authenticatedArchive{pkg: p, entries: entries}, nil
}

func packageInfo(p Package, data []byte) (map[string]bool, error) {
	backups, identity := map[string]bool{}, map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, " = ")
		if !ok || strings.ContainsAny(line, "\x00\r") {
			return nil, fmt.Errorf("malformed authenticated package metadata")
		}
		switch key {
		case "pkgname", "pkgver", "arch":
			if _, exists := identity[key]; exists {
				return nil, fmt.Errorf("ambiguous authenticated package identity")
			}
			identity[key] = value
		case "backup":
			if !safeManifestPath(value) || backups[value] {
				return nil, fmt.Errorf("unsafe authenticated backup path")
			}
			backups[value] = true
		}
	}
	if identity["pkgname"] != p.name || identity["pkgver"] != p.version || identity["arch"] != p.architecture {
		return nil, fmt.Errorf("archive identity does not match official source")
	}
	return backups, nil
}

func safeManifestPath(name string) bool {
	return name != "" && name != "." && name != ".." && !path.IsAbs(name) && path.Clean(name) == name && !strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\x00\r\n")
}

// mtree escapes bytes as backslash-octal. Splitting precedes decoding, so an
// escaped whitespace cannot create a new field or a path separator unnoticed.
func unescape(value string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			out.WriteByte(value[i])
			continue
		}
		if i+3 >= len(value) {
			return "", fmt.Errorf("invalid mtree escape")
		}
		v, err := strconv.ParseUint(value[i+1:i+4], 8, 8)
		if err != nil || v == 0 {
			return "", fmt.Errorf("invalid mtree escape")
		}
		out.WriteByte(byte(v))
		i += 3
	}
	return out.String(), nil
}

func parseManifest(data []byte, backups map[string]bool) ([]entry, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	limited := &io.LimitedReader{R: gz, N: 64 << 20}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	defaults, seen := map[string]string{}, map[string]bool{}
	var entries []entry
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("incomplete authenticated mtree entry")
		}
		attributes := map[string]string{}
		for k, v := range defaults {
			attributes[k] = v
		}
		local := map[string]bool{}
		for _, field := range fields[1:] {
			k, v, ok := strings.Cut(field, "=")
			if !ok || k == "" || v == "" || local[k] {
				return nil, fmt.Errorf("ambiguous authenticated mtree attribute")
			}
			local[k], attributes[k] = true, v
		}
		if fields[0] == "/set" {
			defaults = attributes
			continue
		}
		if !strings.HasPrefix(fields[0], "./") {
			return nil, fmt.Errorf("unsupported authenticated mtree path")
		}
		name, err := unescape(strings.TrimPrefix(fields[0], "./"))
		if err != nil || !safeManifestPath(name) || seen[name] {
			return nil, fmt.Errorf("unsafe or duplicate authenticated mtree path")
		}
		seen[name] = true
		if !strings.Contains(name, "/") && strings.HasPrefix(name, ".") {
			switch name {
			case ".PKGINFO", ".BUILDINFO", ".INSTALL", ".MTREE":
				continue
			default:
				return nil, fmt.Errorf("unsupported archive control entry")
			}
		}
		e := entry{name: name, kind: attributes["type"], backup: backups[name]}
		mode, e1 := strconv.ParseUint(attributes["mode"], 8, 12)
		uid, e2 := strconv.ParseUint(attributes["uid"], 10, 32)
		gid, e3 := strconv.ParseUint(attributes["gid"], 10, 32)
		if e1 != nil || e2 != nil || e3 != nil {
			return nil, fmt.Errorf("missing authenticated ownership or mode")
		}
		e.mode, e.uid, e.gid = uint32(mode), uint32(uid), uint32(gid)
		switch e.kind {
		case "file":
			e.size, err = strconv.ParseInt(attributes["size"], 10, 64)
			if err != nil || e.size < 0 || e.size > 32<<30 {
				return nil, fmt.Errorf("missing authenticated file size")
			}
			digest, err := hex.DecodeString(attributes["sha256digest"])
			if err != nil || len(digest) != 32 {
				return nil, fmt.Errorf("missing authenticated file digest")
			}
			copy(e.digest[:], digest)
		case "link":
			e.link, err = unescape(attributes["link"])
			if err != nil || e.link == "" || e.backup {
				return nil, fmt.Errorf("invalid authenticated link")
			}
		case "dir":
			if e.backup {
				return nil, fmt.Errorf("backup directory unsupported")
			}
		default:
			return nil, fmt.Errorf("unsupported authenticated file type %s", e.kind)
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if limited.N == 0 || len(entries) == 0 {
		return nil, fmt.Errorf("empty or oversized authenticated manifest")
	}
	for backup := range backups {
		if !seen[backup] {
			return nil, fmt.Errorf("backup missing from authenticated manifest")
		}
	}
	return entries, nil
}

// matches checks current managed state, not historical provenance. Backup file
// bytes may differ, but existence, regular type, ownership and mode still must
// match. Directory permissions must match too: sharing does not authorize an
// unsafe mode, and local ownership metadata cannot establish a safe exception.
func (a authenticatedArchive) matches(ctx context.Context, root *os.File) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return a.matchesWithin(ctx, root)
}

func (a authenticatedArchive) matchesWithin(ctx context.Context, root *os.File) (bool, error) {
	observed := make(map[string]os.FileInfo, len(a.entries))
	for _, e := range a.entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		f, err := openPath(root, e.name)
		if os.IsNotExist(err) || err == syscall.ENOTDIR || err == syscall.ELOOP {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		match, info, err := matchEntry(ctx, e, f)
		f.Close()
		if err != nil || !match {
			return match, err
		}
		observed[e.name] = info
	}

	type inode struct{ dev, ino uint64 }
	links := map[inode]uint64{}
	for _, info := range observed {
		if info.Mode().IsRegular() {
			st := info.Sys().(*syscall.Stat_t)
			links[inode{st.Dev, st.Ino}]++
		}
	}
	for _, info := range observed {
		if info.Mode().IsRegular() {
			st := info.Sys().(*syscall.Stat_t)
			if st.Nlink != links[inode{st.Dev, st.Ino}] {
				return false, nil
			}
		}
	}
	// Check the entire observed set again after hashing. This detects observed
	// replacement/renames/in-place writes; no atomic filesystem snapshot or
	// exclusion of concurrent root mutation after this return is claimed.
	for name, before := range observed {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		f, err := openPath(root, name)
		if err != nil {
			return false, fmt.Errorf("installed package changed during verification: %w", err)
		}
		after, err := f.Stat()
		f.Close()
		if err != nil || !unchanged(before, after) {
			return false, fmt.Errorf("installed package changed during verification")
		}
	}
	return true, nil
}

func matchEntry(ctx context.Context, e entry, f *os.File) (bool, os.FileInfo, error) {
	info, err := f.Stat()
	if err != nil {
		return false, nil, err
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Uid != e.uid || st.Gid != e.gid || st.Mode&07777 != e.mode {
		return false, info, nil
	}
	switch e.kind {
	case "dir":
		return info.IsDir(), info, nil
	case "link":
		if info.Mode()&os.ModeSymlink == 0 {
			return false, info, nil
		}
		// readlinkat(fd, "") reads the open symlink itself; never its target.
		buf := make([]byte, 4096)
		empty, _ := syscall.BytePtrFromString("")
		n, _, errno := syscall.Syscall6(syscall.SYS_READLINKAT, f.Fd(), uintptr(unsafe.Pointer(empty)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
		if errno != 0 {
			return false, info, errno
		}
		return int(n) < len(buf) && string(buf[:n]) == e.link, info, nil
	case "file":
		if !info.Mode().IsRegular() {
			return false, info, nil
		}
		if e.backup {
			return true, info, nil
		}
		if info.Size() != e.size {
			return false, info, nil
		}
		r, err := descriptorReader(f, true)
		if err != nil {
			return false, info, err
		}
		defer r.Close()
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(contextReader{ctx, r}, e.size+1))
		if err != nil {
			return false, info, err
		}
		return n == e.size && bytes.Equal(h.Sum(nil), e.digest[:]), info, nil
	}
	return false, info, fmt.Errorf("unsupported installed object")
}

// Prevent large local hashes from ignoring cancellation until the entire file
// has been consumed. Kernel filesystem I/O itself remains a platform boundary.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
