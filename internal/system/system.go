// Package system detects the exact supported execution environment.
package system

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/luigiverona/ops/internal/run"
)

// Detector permits platform checks to use test fixtures.
type Detector struct {
	OSRelease string
	EUID      func() int
	Runner    run.Runner
}

// Detect rejects root, derivatives, non-Linux systems, and non-x86_64 systems.
func (d Detector) Detect(ctx context.Context) error {
	if d.EUID == nil {
		d.EUID = os.Geteuid
	}
	if d.OSRelease == "" {
		d.OSRelease = "/etc/os-release"
	}
	if d.EUID() == 0 {
		return errors.New("running as root is unsupported; run ops as your normal user so user configuration, SSH keys, AUR builds, and user Flatpaks have the correct owner")
	}
	data, err := os.ReadFile(d.OSRelease)
	if err != nil {
		return fmt.Errorf("detect operating system: %w", err)
	}
	fields, err := parseOSRelease(string(data))
	if err != nil {
		return fmt.Errorf("detect operating system: %w", err)
	}
	if fields["ID"] != "arch" {
		return fmt.Errorf("unsupported operating system %q; ops supports only official Arch Linux", fields["ID"])
	}
	if d.Runner == nil {
		return errors.New("detect architecture: command runner is unavailable")
	}
	result, err := d.Runner.Run(ctx, run.Spec{Name: "uname", Args: []string{"-m"}})
	if err != nil {
		return fmt.Errorf("detect architecture: %w", err)
	}
	if strings.TrimSpace(result.Stdout) != "x86_64" {
		return fmt.Errorf("unsupported architecture %q; ops supports only x86_64", strings.TrimSpace(result.Stdout))
	}
	return nil
}

func parseOSRelease(data string) (map[string]string, error) {
	fields := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("malformed os-release line %q", line)
		}
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				return nil, err
			}
			value = decoded
		}
		fields[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return fields, nil
}
