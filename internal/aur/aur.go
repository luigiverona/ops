// Package aur installs reviewed AUR content as the normal user.
package aur

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/luigiverona/ops/internal/run"
)

type Review func(name string, files map[string]string) error

type Manager struct {
	Runner run.Runner
	Review Review
}

func (m Manager) BootstrapParu(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "ops-paru-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	repo := filepath.Join(dir, "paru")
	if _, err := m.Runner.Run(ctx, run.Spec{Name: "git", Args: []string{"clone", "--depth", "1", "--", "https://aur.archlinux.org/paru.git", repo}}); err != nil {
		return err
	}
	files, err := trackedFiles(ctx, m.Runner, repo)
	if err != nil {
		return err
	}
	if m.Review == nil {
		return fmt.Errorf("AUR review is unavailable")
	}
	if err := m.Review("paru", files); err != nil {
		return err
	}
	_, err = m.Runner.Run(ctx, run.Spec{Name: "makepkg", Args: []string{"-si"}, Dir: repo, Interactive: true})
	return err
}

func (m Manager) Install(ctx context.Context, name string) error {
	return m.install(ctx, name, false)
}

func (m Manager) InstallDependency(ctx context.Context, name string) error {
	return m.install(ctx, name, true)
}

func (m Manager) install(ctx context.Context, name string, asDependency bool) error {
	// Paru's built-in interactive review occurs before building. --aur prevents
	// an official-repository package with the same name from changing source.
	args := []string{"-S", "--aur", "--needed"}
	if asDependency {
		args = append(args, "--asdeps")
	}
	args = append(args, "--", name)
	_, err := m.Runner.Run(ctx, run.Spec{Name: "paru", Args: args, Interactive: true})
	return err
}

func trackedFiles(ctx context.Context, runner run.Runner, dir string) (map[string]string, error) {
	result, err := runner.Run(ctx, run.Spec{Name: "git", Args: []string{"-C", dir, "ls-files"}})
	if err != nil {
		return nil, err
	}
	names := strings.Fields(result.Stdout)
	if len(names) > 128 {
		return nil, errors.New("AUR package contains too many tracked build files to review safely")
	}
	sort.Strings(names)
	files := make(map[string]string, len(names))
	total := int64(0)
	for _, name := range names {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if filepath.Dir(path) != dir && !strings.HasPrefix(filepath.Clean(path), filepath.Clean(dir)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("unsafe AUR path %q", name)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		total += info.Size()
		if total > 2*1024*1024 {
			return nil, errors.New("AUR build files exceed the safe review size limit")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		files[name] = sanitize(string(data))
	}
	return files, nil
}

func sanitize(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r == '\n' || r == '\t' || (r >= 0x20 && r <= 0x7e) {
			b.WriteRune(r)
		} else {
			quoted := strconv.QuoteToASCII(string(r))
			b.WriteString(strings.Trim(quoted, "\""))
		}
	}
	return b.String()
}
