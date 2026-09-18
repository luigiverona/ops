// Package archtrust authenticates official Arch source and package evidence.
// It does not infer authenticity from pacman's installed metadata or repo labels.
package archtrust

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const oPath = 0x200000

// openPath walks every ancestor without following symlinks. O_PATH on the leaf
// permits inspecting a FIFO/device/symlink without opening it for I/O.
func openPath(root *os.File, path string) (*os.File, error) {
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." || path == ".." || strings.HasPrefix(path, "../") || strings.ContainsRune(path, 0) {
		return nil, fmt.Errorf("unsafe evidence path %q", path)
	}
	fd, err := syscall.Dup(int(root.Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	parts := strings.Split(path, "/")
	for i, part := range parts {
		flags := oPath | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
		if i < len(parts)-1 {
			flags |= syscall.O_DIRECTORY
		}
		next, e := syscall.Openat(fd, part, flags, 0)
		syscall.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

func regularReader(path *os.File) (*os.File, error) { return descriptorReader(path, false) }

func descriptorReader(path *os.File, allowLinks bool) (*os.File, error) {
	info, err := path.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || (!allowLinks && info.Sys().(*syscall.Stat_t).Nlink != 1) {
		return nil, fmt.Errorf("evidence must be a regular file with one link")
	}
	fd, err := syscall.Open(fmt.Sprintf("/proc/self/fd/%d", path.Fd()), syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path.Name()), nil
}

func readRegular(root *os.File, path string, limit int64) ([]byte, error) {
	f, err := openPath(root, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader, err := regularReader(f)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	before, err := reader.Stat()
	if err != nil {
		return nil, err
	}
	if before.Size() > limit {
		return nil, fmt.Errorf("evidence exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := reader.Stat()
	if err != nil || !unchanged(before, after) || int64(len(data)) != before.Size() || int64(len(data)) > limit {
		return nil, fmt.Errorf("evidence changed during reading")
	}
	current, err := openPath(root, path)
	if err != nil {
		return nil, err
	}
	defer current.Close()
	st, err := current.Stat()
	if err != nil || !unchanged(before, st) {
		return nil, fmt.Errorf("evidence path changed during reading")
	}
	return data, nil
}

func unchanged(a, b os.FileInfo) bool {
	if a == nil || b == nil || !os.SameFile(a, b) {
		return false
	}
	x, y := a.Sys().(*syscall.Stat_t), b.Sys().(*syscall.Stat_t)
	return x.Mode == y.Mode && x.Uid == y.Uid && x.Gid == y.Gid && x.Nlink == y.Nlink && x.Size == y.Size && x.Mtim == y.Mtim && x.Ctim == y.Ctim
}
