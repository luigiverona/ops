package archrepo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/luigiverona/ops/internal/archtrust"
	"github.com/luigiverona/ops/internal/run"
)

// InstalledMatch is the strict ready postcondition. A false result with no error
// means demonstrated invalid content; version drift is a distinct state error.
func InstalledMatch(ctx context.Context, runner run.Runner, target string) (bool, error) {
	state, err := InspectInstalled(ctx, runner, target)
	if err != nil {
		return false, err
	}
	if state.Authenticity == InvalidOfficialContent {
		return false, nil
	}
	if !state.Ready() {
		return false, fmt.Errorf("%s: %s", target, state.Description())
	}
	return true, nil
}

// InspectInstalled keeps exact-version authenticity separate from source currency.
func InspectInstalled(ctx context.Context, runner run.Runner, target string) (InstalledState, error) {
	state := InstalledState{Target: target, Authenticity: AuthenticityInconclusive, Currency: CurrencyUnavailable}
	repo, name, err := Split(target)
	if err != nil {
		return state, err
	}
	var records []map[string]string
	for _, args := range [][]string{{"-Qi", "--", name}, {"-Si", "--", target}} {
		var result run.Result
		if args[0] == "-Si" {
			result, err = Query(ctx, runner, args)
		} else {
			result, err = runner.Run(ctx, run.Spec{Name: "pacman", Args: args, FailureOutput: run.FailureStderr})
		}
		if err != nil {
			return state, err
		}
		record, e := ParseInfo(result.Stdout)
		if e != nil {
			return state, e
		}
		if record["Name"] != name {
			return state, fmt.Errorf("pacman returned a different package")
		}
		records = append(records, record)
	}
	if records[1]["Repository"] != repo {
		return state, fmt.Errorf("pacman returned a different repository for %s", target)
	}
	for _, key := range []string{"Version", "Architecture", "Build Date", "Packager"} {
		for _, record := range records {
			if record[key] == "" || record[key] == "None" || strings.ContainsAny(record[key], "\n\r\t") {
				return state, fmt.Errorf("missing or ambiguous pacman %s", key)
			}
		}
	}
	state.InstalledVersion, state.CurrentVersion = records[0]["Version"], records[1]["Version"]
	if state.InstalledVersion != state.CurrentVersion {
		comparison, e := CompareVersions(ctx, runner, state.InstalledVersion, state.CurrentVersion)
		if e != nil {
			return state, e
		}
		state.Currency = Current
		if comparison < 0 {
			state.Currency = OlderThanCurrent
		} else if comparison > 0 {
			state.Currency = NewerThanCurrent
		}
	} else {
		state.Currency = Current
	}
	// These preliminary checks are meaningful only for the exact same version.
	// They are never sufficient for success, and cannot compare N's build to N+1.
	metadataMatches := true
	if state.InstalledVersion == state.CurrentVersion {
		for _, key := range []string{"Architecture", "Build Date", "Packager"} {
			if records[0][key] != records[1][key] {
				metadataMatches = false
			}
		}
	}
	match, err := InstalledVersionContent(ctx, runner, target, state.InstalledVersion, state.CurrentVersion)
	if errors.Is(err, archtrust.ErrExactVersionUnavailable) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	state.Authenticity = InvalidOfficialContent
	if match && metadataMatches {
		state.Authenticity = VerifiedOfficial
	}
	return state, nil
}

// InstalledMatches queries the independently authenticated official snapshot.
// -Sl's inventory identifies exact names before -Si, whose failure alone cannot
// establish absence. Custom sync databases never provide readiness evidence.
func InstalledMatches(ctx context.Context, runner run.Runner, names []string) (map[string]string, error) {
	states, err := InstalledStates(ctx, runner, names)
	if err != nil {
		return nil, err
	}
	matches := map[string]string{}
	for name, state := range states {
		if state.Ready() {
			matches[name] = state.Target
		}
	}
	return matches, nil
}

func InstalledStates(ctx context.Context, runner run.Runner, names []string) (map[string]InstalledState, error) {
	matches := map[string]InstalledState{}
	if len(names) == 0 {
		return matches, nil
	}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	result, err := Query(ctx, runner, []string{"-Sl"})
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
			matches[name] = InstalledState{Authenticity: AuthenticityInconclusive, Currency: CurrencyUnavailable}
			continue
		}
		state, err := InspectInstalled(ctx, runner, target)
		if err != nil {
			return nil, fmt.Errorf("inspect authenticated official content for %s: %w", target, err)
		}
		matches[name] = state
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
	result, err := Query(ctx, runner, append(args, targets...))
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
