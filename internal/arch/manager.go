package arch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/run"
)

// Manager performs privileged Arch mutations through sudo only.
type Manager struct {
	Runner     run.Runner
	PacmanConf string
}

const artifactStageParent = "/var/tmp"

var (
	artifactPackageName = regexp.MustCompile(`^[A-Za-z0-9@._+][A-Za-z0-9@._+-]*$`)
	artifactStageName   = regexp.MustCompile(`^ops-paru-[A-Za-z0-9]{8,}$`)
)

// EnableMultilib atomically replaces pacman.conf after local and pacman-conf validation.
func (m Manager) EnableMultilib(ctx context.Context) error {
	path := m.PacmanConf
	if path == "" {
		path = "/etc/pacman.conf"
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("pacman.conf is not a regular file")
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	updated, err := EnableMultilib(original)
	if err != nil {
		return err
	}
	if err := ValidatePacmanConf(updated); err != nil {
		return err
	}
	local, err := os.CreateTemp("", "ops-pacman.conf-*")
	if err != nil {
		return err
	}
	localPath := local.Name()
	defer os.Remove(localPath)
	if err := local.Chmod(0o600); err != nil {
		_ = local.Close()
		return err
	}
	if _, err := local.Write(updated); err != nil {
		_ = local.Close()
		return err
	}
	if err := local.Close(); err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(current) != sha256.Sum256(original) {
		return errors.New("pacman.conf changed during planning; rerun ops")
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	remoteTemp := filepath.Join(filepath.Dir(path), ".pacman.conf.ops-"+hex.EncodeToString(random))
	defer func() {
		_, _ = m.Runner.Run(context.Background(), run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: []string{"-n", "rm", "-f", "--", remoteTemp}})
	}()
	if _, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: []string{"-n", "install", "-m", "0644", "-o", "root", "-g", "root", "--", localPath, remoteTemp}}); err != nil {
		return fmt.Errorf("stage pacman.conf: %w", err)
	}
	if _, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: []string{"-n", "pacman-conf", "--config", remoteTemp}}); err != nil {
		return fmt.Errorf("validate pacman.conf with pacman-conf: %w", err)
	}
	if _, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: []string{"-n", "mv", "--", remoteTemp, path}}); err != nil {
		return fmt.Errorf("replace pacman.conf: %w", err)
	}
	verified, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	enabled, err := MultilibEnabled(verified)
	if err != nil || !enabled {
		return errors.New("multilib postcondition verification failed")
	}
	return nil
}

func (m Manager) FullUpgrade(ctx context.Context, targets ...string) error {
	args := []string{"-n", "pacman", "-Syu"}
	for _, target := range targets {
		if _, _, err := archrepo.Split(target); err != nil {
			return err
		}
	}
	if len(targets) > 0 {
		args = append(append(args, "--"), targets...)
	}
	// A full upgrade must include available rebuilds from every configured
	// repository. Filtering here can leave custom clients linked against an old
	// official library ABI. Managed targets remain qualified; their subsequent
	// sync installations use runOfficial and verify source identity separately.
	configuration, err := m.Runner.Run(ctx, run.Spec{Name: "pacman-conf", FailureOutput: run.FailureStderr})
	if err != nil {
		return err
	}
	if _, _, err := archrepo.OfficialConfig(configuration.Stdout); err != nil {
		return err
	}
	_, err = m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: args, Interactive: true, Interaction: "pacman transaction decisions"})
	return err
}

func (m Manager) Install(ctx context.Context, packages []string, asDeps bool) error {
	if len(packages) == 0 {
		return nil
	}
	transaction, err := archrepo.Transaction(ctx, m.Runner, packages)
	if err != nil {
		return fmt.Errorf("validate official installation transaction: %w", err)
	}
	if asDeps && len(transaction) != len(packages) {
		return errors.New("official dependency transaction changed after planning; rerun ops")
	}
	selected := map[string]bool{}
	for _, target := range transaction {
		selected[target] = true
	}
	for _, target := range packages {
		if !selected[target] {
			return fmt.Errorf("official transaction omitted requested target %s", target)
		}
	}
	// Keep requested targets qualified. runOfficial also excludes custom repos
	// from implicit dependency resolution. Leaving pulled dependencies implicit
	// preserves pacman's install reasons. Do not use --needed: an equal version
	// is not evidence of matching source metadata.
	args := []string{"-n", "pacman", "-S", "--noconfirm"}
	if asDeps {
		args = append(args, "--asdeps")
	}
	args = append(args, "--")
	args = append(args, packages...)
	err = m.runOfficial(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: args, StreamOutput: true, AllowTruncatedOutput: true})
	if err != nil {
		return err
	}
	for _, target := range transaction {
		match, err := archrepo.InstalledMatch(ctx, m.Runner, target)
		if err != nil {
			return err
		}
		if !match {
			return fmt.Errorf("installed package does not match official metadata for %s", target)
		}
	}
	return nil
}

// InstallArtifacts copies already-open normal-user artifacts into a protected
// root-owned directory, validates those copies, and installs only exact planned
// outputs with their intended explicit/dependency install reasons.
func (m Manager) InstallArtifacts(ctx context.Context, buildDir string, artifacts, targets, explicit []string) (returnErr error) {
	sources, err := openArtifactSources(buildDir, artifacts)
	if err != nil {
		return err
	}
	defer func() {
		for _, source := range sources {
			_ = source.Close()
		}
	}()
	if len(targets) == 0 {
		return errors.New("no package artifact identities were approved")
	}
	wanted := make(map[string]bool, len(targets))
	for _, target := range targets {
		if !artifactPackageName.MatchString(target) || wanted[target] {
			return errors.New("invalid or duplicate planned package artifact identity")
		}
		wanted[target] = true
	}
	explicitSet := make(map[string]bool, len(explicit))
	for _, target := range explicit {
		if !wanted[target] || explicitSet[target] {
			return errors.New("invalid or duplicate explicit package artifact identity")
		}
		explicitSet[target] = true
	}

	if err := m.validateStageParent(ctx); err != nil {
		return err
	}
	result, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: []string{"-n", "mktemp", "--directory", "--tmpdir=" + artifactStageParent, "ops-paru-XXXXXXXXXXXX"}})
	if err != nil {
		return fmt.Errorf("create protected package staging directory: %w", err)
	}
	stageDir, err := validatedStageDir(result.Stdout)
	if err != nil {
		return err
	}
	var staged []string
	defer func() {
		if cleanupErr := m.cleanupArtifactStage(stageDir, staged); cleanupErr != nil {
			returnErr = errors.Join(returnErr, cleanupErr)
		}
	}()
	if err := m.validateProtectedPath(ctx, stageDir, true); err != nil {
		return fmt.Errorf("validate protected package staging directory: %w", err)
	}

	for index, source := range sources {
		stagedPath := filepath.Join(stageDir, fmt.Sprintf("artifact-%03d.pkg.tar", index))
		staged = append(staged, stagedPath)
		if _, err := source.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind package artifact: %w", err)
		}
		_, err := m.Runner.Run(ctx, run.Spec{
			Name:  "sudo",
			Args:  []string{"-n", "install", "--mode=0600", "--", "/dev/stdin", stagedPath},
			Stdin: source,
		})
		if err != nil {
			return fmt.Errorf("copy package artifact into protected staging: %w", err)
		}
		if err := m.validateProtectedPath(ctx, stagedPath, false); err != nil {
			return fmt.Errorf("validate protected package artifact: %w", err)
		}
	}

	found := make(map[string]string, len(targets))
	for _, stagedPath := range staged {
		result, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: []string{"-n", "pacman", "-Qpq", "--", stagedPath}})
		if err != nil {
			return fmt.Errorf("inspect protected package artifact: %w", err)
		}
		name, err := exactArtifactPackageName(result.Stdout)
		if err != nil {
			return fmt.Errorf("inspect protected package artifact: %w", err)
		}
		if !wanted[name] {
			continue
		}
		if _, exists := found[name]; exists {
			return fmt.Errorf("multiple protected artifacts match planned package %q", name)
		}
		found[name] = stagedPath
	}
	selected := make([]string, 0, len(targets))
	for _, target := range targets {
		path, ok := found[target]
		if !ok {
			return fmt.Errorf("no protected artifact matches planned package %q", target)
		}
		selected = append(selected, path)
	}
	if err := m.installArtifacts(ctx, selected, true); err != nil {
		return err
	}
	if err := m.MarkExplicit(ctx, explicit); err != nil {
		return err
	}
	for _, target := range explicit {
		if _, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "pacman", Args: []string{"-Qe", target}}); err != nil {
			return fmt.Errorf("verify explicit package artifact %q: %w", target, err)
		}
	}
	return nil
}

func (m Manager) installArtifacts(ctx context.Context, paths []string, asDeps bool) error {
	if len(paths) == 0 {
		return nil
	}
	args := []string{"-n", "pacman", "-U", "--needed", "--noconfirm"}
	if asDeps {
		args = append(args, "--asdeps")
	} else {
		args = append(args, "--asexplicit")
	}
	args = append(args, "--")
	args = append(args, paths...)
	if err := m.runOfficial(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: args, StreamOutput: true, AllowTruncatedOutput: true}); err != nil {
		return err
	}
	return nil
}

func openArtifactSources(buildDir string, artifacts []string) ([]*os.File, error) {
	if len(artifacts) == 0 {
		return nil, errors.New("no package artifacts were reported")
	}
	root, err := filepath.EvalSymlinks(buildDir)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var sources []*os.File
	closeSources := func() {
		for _, source := range sources {
			_ = source.Close()
		}
	}
	seen := make(map[string]bool, len(artifacts))
	for _, value := range artifacts {
		path := value
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		path, err = filepath.Abs(filepath.Clean(path))
		if err != nil || !insidePath(root, path) || seen[path] {
			closeSources()
			return nil, fmt.Errorf("unsafe or duplicate package artifact path %q", value)
		}
		seen[path] = true
		pathInfo, lstatErr := os.Lstat(path)
		if errors.Is(lstatErr, os.ErrNotExist) {
			// makepkg --packagelist can predict outputs the build did not
			// produce. Planned identities must still match staged artifacts.
			continue
		}
		if lstatErr != nil || !pathInfo.Mode().IsRegular() {
			closeSources()
			return nil, fmt.Errorf("package artifact is unavailable or non-regular: %s", path)
		}
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil || resolved != path || !insidePath(root, resolved) {
			closeSources()
			return nil, fmt.Errorf("package artifact has an unsafe resolved path: %s", path)
		}
		// O_NONBLOCK ensures an attacker-controlled FIFO cannot hold preparation
		// open before fstat rejects it. It has no effect on regular-file reads.
		fd, openErr := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if openErr != nil {
			closeSources()
			return nil, fmt.Errorf("open package artifact without following symlinks: %w", openErr)
		}
		source := os.NewFile(uintptr(fd), path)
		info, statErr := source.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			_ = source.Close()
			closeSources()
			return nil, fmt.Errorf("package artifact is unavailable or non-regular: %s", path)
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func insidePath(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func (m Manager) validateStageParent(ctx context.Context) error {
	result, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: []string{"-n", "stat", "--format=%u\t%f\t%h", "--", artifactStageParent}})
	if err != nil {
		return fmt.Errorf("inspect protected staging parent: %w", err)
	}
	uid, mode, _, err := parseProtectedStat(result.Stdout)
	if err != nil || uid != 0 || mode&syscall.S_IFMT != syscall.S_IFDIR || mode&0o022 != 0 && mode&syscall.S_ISVTX == 0 {
		return errors.New("protected staging parent is not a trusted root-owned sticky directory")
	}
	return nil
}

func (m Manager) validateProtectedPath(ctx context.Context, path string, directory bool) error {
	result, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: []string{"-n", "stat", "--format=%u\t%f\t%h", "--", path}})
	if err != nil {
		return err
	}
	uid, mode, links, err := parseProtectedStat(result.Stdout)
	if err != nil || uid != 0 || mode&0o022 != 0 {
		return errors.New("protected staged path has unsafe ownership or permissions")
	}
	if directory {
		if mode&syscall.S_IFMT != syscall.S_IFDIR {
			return errors.New("protected staged path has an unexpected file type")
		}
		// Directory link counts include subdirectories and need not equal one.
		return nil
	}
	if mode&syscall.S_IFMT != syscall.S_IFREG {
		return errors.New("protected staged path has an unexpected file type")
	}
	if links != 1 {
		return errors.New("protected staged file has unsafe links")
	}
	return nil
}

func parseProtectedStat(output string) (uid, mode, links uint64, err error) {
	line := strings.TrimSuffix(output, "\n")
	if strings.ContainsAny(line, "\r\n") {
		return 0, 0, 0, errors.New("stat returned ambiguous output")
	}
	fields := strings.Split(line, "\t")
	if len(fields) != 3 {
		return 0, 0, 0, errors.New("stat returned invalid output")
	}
	uid, err = strconv.ParseUint(fields[0], 10, 32)
	if err != nil {
		return 0, 0, 0, err
	}
	mode, err = strconv.ParseUint(fields[1], 16, 32)
	if err != nil {
		return 0, 0, 0, err
	}
	links, err = strconv.ParseUint(fields[2], 10, 64)
	return uid, mode, links, err
}

func validatedStageDir(output string) (string, error) {
	path := strings.TrimSuffix(output, "\n")
	if strings.ContainsAny(path, "\r\n") || filepath.Dir(path) != artifactStageParent || !artifactStageName.MatchString(filepath.Base(path)) {
		return "", errors.New("privileged staging command returned an unsafe directory")
	}
	return path, nil
}

func exactArtifactPackageName(output string) (string, error) {
	name := strings.TrimSuffix(output, "\n")
	if strings.ContainsAny(name, "\r\n\t ") || !artifactPackageName.MatchString(name) {
		return "", errors.New("package artifact has ambiguous internal identity")
	}
	return name, nil
}

func (m Manager) cleanupArtifactStage(stageDir string, staged []string) error {
	var cleanupErrors []error
	if len(staged) > 0 {
		args := []string{"-n", "rm", "-f", "--"}
		args = append(args, staged...)
		if _, err := m.Runner.Run(context.Background(), run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: args}); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove protected staged artifacts: %w", err))
		}
	}
	if _, err := m.Runner.Run(context.Background(), run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: []string{"-n", "rmdir", "--", stageDir}}); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("remove protected package staging directory: %w", err))
	}
	return errors.Join(cleanupErrors...)
}

// MarkExplicit preserves the install reason for packages also requested as applications.
func (m Manager) MarkExplicit(ctx context.Context, packages []string) error {
	if len(packages) == 0 {
		return nil
	}
	args := []string{"-n", "pacman", "-D", "--asexplicit", "--"}
	args = append(args, packages...)
	_, err := m.Runner.Run(ctx, run.Spec{FailureOutput: run.FailureStderr, Name: "sudo", Args: args})
	return err
}
