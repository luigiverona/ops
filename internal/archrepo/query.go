package archrepo

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/luigiverona/ops/internal/run"
)

// InstalledMatch establishes a current metadata match, never historical origin
// or installed-file authenticity. The local database does not retain a repo.
func InstalledMatch(ctx context.Context, runner run.Runner, target string) (bool, error) {
	repo, name, err := Split(target)
	if err != nil {
		return false, err
	}
	var records []map[string]string
	for _, args := range [][]string{{"-Qi", "--", name}, {"-Si", "--", target}} {
		result, err := runner.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
		if err != nil {
			return false, err
		}
		record, err := ParseInfo(result.Stdout)
		if err != nil {
			return false, err
		}
		if record["Name"] != name {
			return false, fmt.Errorf("pacman returned a different package")
		}
		records = append(records, record)
	}
	if records[1]["Repository"] != repo {
		return false, fmt.Errorf("pacman returned a different repository for %s", target)
	}
	matches := true
	for _, key := range []string{"Version", "Architecture", "Build Date", "Packager"} {
		for _, record := range records {
			if record[key] == "" || record[key] == "None" || strings.ContainsAny(record[key], "\n\r\t") {
				return false, fmt.Errorf("missing or ambiguous pacman %s", key)
			}
		}
		matches = matches && records[0][key] == records[1][key]
	}
	return matches, nil
}

// InstalledMatches queries each configured official repository explicitly.
// -Sl's inventory identifies exact names before -Si, whose failure alone cannot
// establish absence. Custom sync databases never provide readiness evidence.
func InstalledMatches(ctx context.Context, runner run.Runner, names []string) (map[string]string, error) {
	matches := map[string]string{}
	if len(names) == 0 {
		return matches, nil
	}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	result, err := runner.Run(ctx, run.Spec{Name: "pacman", Args: []string{"-Sl"}, FailureOutput: run.FailureStderr})
	if err != nil {
		return nil, err
	}
	candidates := map[string]string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(result.Stdout, "\n"), "\n") {
		if line == "" {
			return nil, fmt.Errorf("empty sync inventory record")
		}
		fields := strings.Split(line, " ")
		if len(fields) < 3 || len(fields) > 5 || fields[0] == "" || fields[1] == "" || fields[2] == "" || (len(fields) == 4 && fields[3] != "[installed]") || (len(fields) == 5 && (fields[3] != "[installed:" || !strings.HasSuffix(fields[4], "]"))) {
			return nil, fmt.Errorf("malformed sync inventory")
		}
		target := fields[0] + "/" + fields[1]
		if seen[target] {
			return nil, fmt.Errorf("duplicate sync inventory record")
		}
		seen[target] = true
		if !Official(fields[0]) || !wanted[fields[1]] {
			continue
		}
		if _, _, err := Split(target); err != nil {
			return nil, err
		}
		if candidates[fields[1]] != "" {
			return nil, fmt.Errorf("ambiguous official package %s", fields[1])
		}
		candidates[fields[1]] = target
	}
	names = append([]string(nil), names...)
	sort.Strings(names)
	for _, name := range names {
		target := candidates[name]
		if target == "" {
			continue
		}
		match, err := InstalledMatch(ctx, runner, target)
		if err != nil {
			return nil, fmt.Errorf("inspect official metadata match for %s: %w", target, err)
		}
		if match {
			matches[name] = target
		}
	}
	return matches, nil
}

// Transaction records every repository, including additional dependencies.
func Transaction(ctx context.Context, runner run.Runner, targets []string) ([]string, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	for _, target := range targets {
		if _, _, err := Split(target); err != nil {
			return nil, err
		}
	}
	args := []string{"-Sp", "--noconfirm", "--print-format", "%r/%n", "--"}
	result, err := runner.Run(ctx, run.Spec{Name: "pacman", Args: append(args, targets...), FailureOutput: run.FailureStderr})
	if err != nil {
		return nil, err
	}
	return ParseTransaction(result.Stdout)
}
func ParseTransaction(output string) ([]string, error) {
	var targets []string
	seen := map[string]bool{}
	lines := strings.Split(output, "\n")
	for i, line := range lines {
		if line == "" && i == len(lines)-1 {
			continue
		}
		_, name, err := Split(line)
		if err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate transaction package %s", name)
		}
		seen[name] = true
		targets = append(targets, line)
	}
	sort.Strings(targets)
	return targets, nil
}
