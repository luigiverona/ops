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

	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/resolve"
	"github.com/luigiverona/ops/internal/run"
)

type Review func(name string, files map[string]string) error

type Manager struct {
	Runner run.Runner
	Review Review
}

// Build reviews and builds one exact AUR source as the normal user, then
// delegates only installation of exact selected artifacts to its caller.
func (m Manager) Build(ctx context.Context, source plan.AURSource, target string, outputs []string, afterReview func() error, install func(string, []string) error) error {
	if !gitObject(source.Commit) || !aurmeta.ValidPackageName(source.Metadata.PackageBase) || !aurmeta.ValidPackageName(target) {
		return errors.New("invalid planned AUR source")
	}
	if len(outputs) == 0 {
		return errors.New("invalid planned AUR outputs")
	}
	seenOutputs := make(map[string]bool, len(outputs))
	for _, output := range outputs {
		if !aurmeta.ValidPackageName(output) || seenOutputs[output] {
			return errors.New("invalid planned AUR output")
		}
		seenOutputs[output] = true
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "ops-aur-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// Remote package identity must not determine filesystem containment.
	repo := filepath.Join(dir, "checkout")
	if _, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "git", Args: []string{"init", "--quiet", repo}}); err != nil {
		return err
	}
	repository := "https://aur.archlinux.org/" + source.Metadata.PackageBase + ".git"
	if _, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "git", Args: []string{"-C", repo, "fetch", "--quiet", "--depth", "1", repository, source.Commit}}); err != nil {
		return err
	}
	if _, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "git", Args: []string{"-C", repo, "checkout", "--quiet", "--detach", "FETCH_HEAD"}}); err != nil {
		return err
	}
	result, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "git", Args: []string{"-C", repo, "rev-parse", "HEAD"}})
	if err != nil {
		return fmt.Errorf("verify reviewed AUR revision: %w", err)
	}
	if strings.TrimSpace(result.Stdout) != source.Commit {
		return errors.New("reviewed AUR source does not match the planned commit")
	}
	files, err := trackedFiles(ctx, m.Runner, repo)
	if err != nil {
		return err
	}
	if _, ok := files[".SRCINFO"]; !ok {
		return errors.New("reviewed AUR source does not track .SRCINFO")
	}

	srcinfo, err := safeFile(repo, filepath.Join(repo, ".SRCINFO"))
	if err != nil {
		return err
	}
	reviewed, err := aurmeta.Parse(srcinfo)
	if err != nil {
		return fmt.Errorf("parse reviewed .SRCINFO: %w", err)
	}
	if !aurmeta.PlanningEqual(reviewed, source.Metadata) {
		return errors.New("reviewed AUR metadata changed after planning; rerun ops")
	}
	compareVersions := func(left, right string) (int, error) {
		return (resolve.Resolver{Runner: m.Runner}).CompareVersions(ctx, left, right)
	}
	reviewedOutputs, err := reviewed.OutputClosure(target, compareVersions)
	if err != nil {
		return fmt.Errorf("verify reviewed AUR outputs: %w", err)
	}
	if strings.Join(reviewedOutputs, "\x00") != strings.Join(outputs, "\x00") {
		return errors.New("reviewed AUR output set changed after planning; rerun ops")
	}
	if m.Review == nil {
		return fmt.Errorf("AUR review is unavailable")
	}
	if err := m.Review(target, sanitizedFiles(files)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result, err = m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "git", Args: []string{"-C", repo, "rev-parse", "HEAD"}})
	if err != nil {
		return fmt.Errorf("verify reviewed AUR revision: %w", err)
	}
	if strings.TrimSpace(result.Stdout) != source.Commit {
		return errors.New("reviewed AUR source changed during review")
	}
	reviewFiles, err := trackedFiles(ctx, m.Runner, repo)
	if err != nil {
		return fmt.Errorf("verify reviewed AUR files during review: %w", err)
	}
	if !sameFiles(files, reviewFiles) {
		return errors.New("reviewed AUR files changed during review")
	}

	if afterReview == nil || install == nil {
		return errors.New("AUR build mutation hooks are unavailable")
	}
	if err := afterReview(); err != nil {
		return err
	}
	result, err = m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "git", Args: []string{"-C", repo, "rev-parse", "HEAD"}})
	if err != nil {
		return fmt.Errorf("verify reviewed AUR revision: %w", err)
	}
	if strings.TrimSpace(result.Stdout) != source.Commit {
		return errors.New("reviewed AUR source changed before build")
	}
	currentFiles, err := trackedFiles(ctx, m.Runner, repo)
	if err != nil {
		return fmt.Errorf("verify reviewed AUR files before build: %w", err)
	}
	if !sameFiles(files, currentFiles) {
		return errors.New("reviewed AUR files changed before build")
	}
	// Both invocations source reviewed shell code, which can print arbitrary
	// private data. Build approval does not authorize diagnostic disclosure.
	if _, err := m.Runner.Run(ctx, run.Spec{Name: "makepkg", Dir: repo, AllowTruncatedOutput: true, Stdin: strings.NewReader("")}); err != nil {
		return err
	}
	result, err = m.Runner.Run(ctx, run.Spec{Name: "makepkg", Args: []string{"--packagelist"}, Dir: repo, Stdin: strings.NewReader("")})
	if err != nil {
		return err
	}
	artifacts, err := artifactPaths(result.Stdout)
	if err != nil {
		return err
	}
	return install(repo, artifacts)
}

func artifactPaths(output string) ([]string, error) {
	lines := strings.Split(output, "\n")
	var paths []string
	for index, line := range lines {
		if index == len(lines)-1 && line == "" {
			continue
		}
		if line == "" || strings.ContainsAny(line, "\r\x00") {
			return nil, errors.New("makepkg returned an invalid package artifact path")
		}
		paths = append(paths, line)
	}
	if len(paths) == 0 {
		return nil, errors.New("makepkg returned no package artifact paths")
	}
	return paths, nil
}

func sameFiles(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for name, contents := range left {
		if right[name] != contents {
			return false
		}
	}
	return true
}

func safeFile(root, path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !inside(root, path) {
		return nil, errors.New("reviewed .SRCINFO is unavailable or unsafe")
	}
	return os.ReadFile(path)
}

func inside(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func gitObject(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func trackedFiles(ctx context.Context, runner run.Runner, dir string) (map[string]string, error) {
	result, err := runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "git", Args: []string{"-C", dir, "ls-files", "-z"}})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range strings.Split(result.Stdout, "\x00") {
		if name == "" {
			continue
		}
		for _, r := range name {
			if r < 0x20 || r == 0x7f {
				return nil, errors.New("AUR package contains an unsafe tracked filename")
			}
		}
		names = append(names, name)
	}
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
			return nil, fmt.Errorf("AUR tracked file is unavailable or non-regular: %s", name)
		}
		total += info.Size()
		if total > 2*1024*1024 {
			return nil, errors.New("AUR build files exceed the safe review size limit")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		// Keep raw bytes for the pre-build equality check. Sanitization is only
		// for terminal review output and must not weaken that comparison.
		files[name] = string(data)
	}
	return files, nil
}

func sanitizedFiles(files map[string]string) map[string]string {
	result := make(map[string]string, len(files))
	for name, contents := range files {
		result[name] = sanitize(contents)
	}
	return result
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
