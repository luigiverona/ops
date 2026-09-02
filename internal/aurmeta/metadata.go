// Package aurmeta parses the declarative AUR metadata used for planning.
package aurmeta

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var dependencyNamePattern = regexp.MustCompile(`^[A-Za-z0-9@._+][A-Za-z0-9@._+-]*$`)
var fingerprintPattern = regexp.MustCompile(`^(?:[A-F0-9]{40}|[A-F0-9]{64})$`)

// ValidPackageName reports whether value is safe as an Arch package or package
// base identity. Callers use it before treating AUR metadata as a path or URL
// component.
func ValidPackageName(value string) bool {
	return dependencyNamePattern.MatchString(value)
}

// Package is one output declared by an AUR package base.
type Package struct {
	Name     string
	Depends  []string
	Provides []string
	Optional []string
}

// Metadata contains the planning-relevant fields from .SRCINFO.
type Metadata struct {
	PackageBase  string
	Version      string
	Depends      []string
	Provides     []string
	MakeDepends  []string
	CheckDepends []string
	Optional     []string
	ValidPGPKeys []string
	Packages     []Package
}

// VersionComparator compares two versions using Arch package version semantics.
type VersionComparator func(left, right string) (int, error)

// Dependency is one validated Arch dependency or provide expression.
type Dependency struct {
	Name     string
	Operator string
	Version  string
}

// Parse reads .SRCINFO without evaluating the corresponding PKGBUILD.
func Parse(data []byte) (Metadata, error) {
	var metadata Metadata
	var current *Package
	var epoch, pkgver, pkgrel string
	for number, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Metadata{}, fmt.Errorf("invalid .SRCINFO line %d", number+1)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key == "" || value == "" {
			return Metadata{}, fmt.Errorf("invalid .SRCINFO field on line %d", number+1)
		}
		switch key {
		case "pkgbase":
			if metadata.PackageBase != "" && metadata.PackageBase != value {
				return Metadata{}, errors.New(".SRCINFO contains multiple package bases")
			}
			metadata.PackageBase = value
		case "epoch":
			epoch = value
		case "pkgver":
			pkgver = value
		case "pkgrel":
			pkgrel = value
		case "pkgname":
			metadata.Packages = append(metadata.Packages, Package{Name: value})
			current = &metadata.Packages[len(metadata.Packages)-1]
		case "depends", "depends_x86_64":
			if current == nil {
				metadata.Depends = append(metadata.Depends, value)
			} else {
				current.Depends = append(current.Depends, value)
			}
		case "provides", "provides_x86_64":
			if current == nil {
				metadata.Provides = append(metadata.Provides, value)
			} else {
				current.Provides = append(current.Provides, value)
			}
		case "makedepends", "makedepends_x86_64":
			if current != nil {
				return Metadata{}, errors.New("package-specific makedepends is not valid planning metadata")
			}
			metadata.MakeDepends = append(metadata.MakeDepends, value)
		case "checkdepends", "checkdepends_x86_64":
			if current != nil {
				return Metadata{}, errors.New("package-specific checkdepends is not valid planning metadata")
			}
			metadata.CheckDepends = append(metadata.CheckDepends, value)
		case "optdepends", "optdepends_x86_64":
			if current == nil {
				metadata.Optional = append(metadata.Optional, value)
			} else {
				current.Optional = append(current.Optional, value)
			}
		case "validpgpkeys":
			if current != nil {
				return Metadata{}, errors.New("package-specific validpgpkeys is not valid planning metadata")
			}
			metadata.ValidPGPKeys = append(metadata.ValidPGPKeys, value)
		}
	}
	if metadata.PackageBase == "" || pkgver == "" || pkgrel == "" || len(metadata.Packages) == 0 {
		return Metadata{}, errors.New(".SRCINFO is missing required package identity fields")
	}
	if !ValidPackageName(metadata.PackageBase) {
		return Metadata{}, errors.New(".SRCINFO contains an invalid package base")
	}
	metadata.Version = pkgver + "-" + pkgrel
	if epoch != "" && epoch != "0" {
		metadata.Version = epoch + ":" + metadata.Version
	}
	metadata.Depends = normalized(metadata.Depends)
	metadata.Provides = normalized(metadata.Provides)
	metadata.MakeDepends = normalized(metadata.MakeDepends)
	metadata.CheckDepends = normalized(metadata.CheckDepends)
	metadata.Optional = normalized(metadata.Optional)
	metadata.ValidPGPKeys = normalized(metadata.ValidPGPKeys)
	seen := make(map[string]bool)
	for i := range metadata.Packages {
		if !ValidPackageName(metadata.Packages[i].Name) || seen[metadata.Packages[i].Name] {
			return Metadata{}, errors.New(".SRCINFO contains an invalid or duplicate output package")
		}
		seen[metadata.Packages[i].Name] = true
		metadata.Packages[i].Depends = normalized(metadata.Packages[i].Depends)
		metadata.Packages[i].Provides = normalized(metadata.Packages[i].Provides)
		metadata.Packages[i].Optional = normalized(metadata.Packages[i].Optional)
	}
	for _, values := range [][]string{metadata.Depends, metadata.MakeDepends, metadata.CheckDepends} {
		for _, value := range values {
			if _, err := ParseDependency(value); err != nil {
				return Metadata{}, err
			}
		}
	}
	for _, value := range metadata.Optional {
		if _, err := ParseOptionalDependency(value); err != nil {
			return Metadata{}, err
		}
	}
	for _, fingerprint := range metadata.ValidPGPKeys {
		if !ValidFingerprint(fingerprint) {
			return Metadata{}, fmt.Errorf("invalid validpgpkeys fingerprint %q", fingerprint)
		}
	}
	for _, value := range metadata.Provides {
		if _, err := ParseProvide(value); err != nil {
			return Metadata{}, err
		}
	}
	for _, pkg := range metadata.Packages {
		for _, value := range pkg.Depends {
			if _, err := ParseDependency(value); err != nil {
				return Metadata{}, err
			}
		}
		for _, value := range pkg.Provides {
			if _, err := ParseProvide(value); err != nil {
				return Metadata{}, err
			}
		}
		for _, value := range pkg.Optional {
			if _, err := ParseOptionalDependency(value); err != nil {
				return Metadata{}, err
			}
		}
	}
	sort.Slice(metadata.Packages, func(i, j int) bool { return metadata.Packages[i].Name < metadata.Packages[j].Name })
	return metadata, nil
}

// PlanningEqual compares every field that can affect dependency or artifact planning.
func PlanningEqual(a, b Metadata) bool {
	return a.PackageBase == b.PackageBase && a.Version == b.Version &&
		strings.Join(a.Depends, "\x00") == strings.Join(b.Depends, "\x00") &&
		strings.Join(a.Provides, "\x00") == strings.Join(b.Provides, "\x00") &&
		strings.Join(a.MakeDepends, "\x00") == strings.Join(b.MakeDepends, "\x00") &&
		strings.Join(a.CheckDepends, "\x00") == strings.Join(b.CheckDepends, "\x00") &&
		strings.Join(a.Optional, "\x00") == strings.Join(b.Optional, "\x00") &&
		strings.Join(a.ValidPGPKeys, "\x00") == strings.Join(b.ValidPGPKeys, "\x00") &&
		packagesEqual(a.Packages, b.Packages)
}

func packagesEqual(a, b []Package) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || strings.Join(a[i].Depends, "\x00") != strings.Join(b[i].Depends, "\x00") ||
			strings.Join(a[i].Provides, "\x00") != strings.Join(b[i].Provides, "\x00") ||
			strings.Join(a[i].Optional, "\x00") != strings.Join(b[i].Optional, "\x00") {
			return false
		}
	}
	return true
}

// OptionalRequirements returns the global and selected-output optdepends from
// this exact .SRCINFO revision. Their descriptions are intentionally excluded
// from the package-manager dependency expression.
func (m Metadata) OptionalRequirements(target string, compare VersionComparator) ([]Requirement, error) {
	outputs, err := m.OutputClosure(target, compare)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]bool, len(outputs))
	for _, output := range outputs {
		selected[output] = true
	}
	values := append([]string(nil), m.Optional...)
	for _, pkg := range m.Packages {
		if selected[pkg.Name] {
			values = append(values, pkg.Optional...)
		}
	}
	requirements := make([]Requirement, 0, len(values))
	for _, value := range values {
		expression, err := OptionalDependencyExpression(value)
		if err != nil {
			return nil, err
		}
		provided, err := m.selectedOutputProvider(expression, selected, compare)
		if err != nil {
			return nil, err
		}
		if provided {
			continue
		}
		requirements = append(requirements, Requirement{Expression: expression, Purpose: "optional"})
	}
	return uniqueRequirements(requirements), nil
}

// BuildRequirements returns the official-package dependencies needed to build
// and install target. Dependencies satisfied by another selected output from the
// same package base are excluded from the official repository transaction.
func (m Metadata) BuildRequirements(target string, runChecks bool, compare VersionComparator) ([]Requirement, error) {
	outputs, err := m.OutputClosure(target, compare)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]bool, len(outputs))
	for _, output := range outputs {
		selected[output] = true
	}
	var requirements []Requirement
	for _, value := range m.Depends {
		provided, err := m.selectedOutputProvider(value, selected, compare)
		if err != nil {
			return nil, err
		}
		if !provided {
			requirements = append(requirements, Requirement{Expression: value, Purpose: "runtime"})
		}
	}
	for _, pkg := range m.Packages {
		if !selected[pkg.Name] {
			continue
		}
		for _, value := range pkg.Depends {
			provided, err := m.selectedOutputProvider(value, selected, compare)
			if err != nil {
				return nil, err
			}
			if !provided {
				requirements = append(requirements, Requirement{Expression: value, Purpose: "runtime"})
			}
		}
	}
	for _, value := range m.MakeDepends {
		requirements = append(requirements, Requirement{Expression: value, Purpose: "build"})
	}
	if runChecks {
		for _, value := range m.CheckDepends {
			requirements = append(requirements, Requirement{Expression: value, Purpose: "check"})
		}
	}
	return uniqueRequirements(requirements), nil
}

// Requirement is one dependency expression and why it is required.
type Requirement struct {
	Expression string
	Purpose    string
}

// OutputClosure returns the exact requested output and required sibling packages.
func (m Metadata) OutputClosure(target string, compare VersionComparator) ([]string, error) {
	packages := make(map[string]Package, len(m.Packages))
	for _, pkg := range m.Packages {
		packages[pkg.Name] = pkg
	}
	if _, ok := packages[target]; !ok {
		return nil, fmt.Errorf("AUR metadata does not declare requested package %q", target)
	}
	selected := map[string]bool{target: true}
	changed := true
	for changed {
		changed = false
		for name := range selected {
			dependencies := append(append([]string(nil), m.Depends...), packages[name].Depends...)
			for _, dependency := range dependencies {
				sibling, exists, err := m.outputProvider(dependency, compare)
				if err != nil {
					return nil, err
				}
				if exists && !selected[sibling] {
					selected[sibling] = true
					changed = true
				}
			}
		}
	}
	result := make([]string, 0, len(selected))
	for name := range selected {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func (m Metadata) selectedOutputProvider(dependency string, selected map[string]bool, compare VersionComparator) (bool, error) {
	provider, found, err := m.outputProvider(dependency, compare)
	return found && selected[provider], err
}

func (m Metadata) outputProvider(dependency string, compare VersionComparator) (string, bool, error) {
	requirement, err := ParseDependency(dependency)
	if err != nil {
		return "", false, err
	}
	var providers []string
	for _, pkg := range m.Packages {
		if pkg.Name != requirement.Name {
			continue
		}
		matches, err := versionSatisfies(m.Version, requirement, compare)
		if err != nil {
			return "", false, err
		}
		if matches {
			providers = append(providers, pkg.Name)
		}
	}
	for _, pkg := range m.Packages {
		provides := append(append([]string(nil), m.Provides...), pkg.Provides...)
		for _, provided := range provides {
			provision, err := ParseProvide(provided)
			if err != nil {
				return "", false, err
			}
			if provision.Name != requirement.Name {
				continue
			}
			matches, err := provideSatisfies(provision, requirement, compare)
			if err != nil {
				return "", false, err
			}
			if matches {
				providers = appendUnique(providers, pkg.Name)
			}
		}
	}
	if len(providers) > 1 {
		return "", false, fmt.Errorf("multiple output packages provide dependency %q", dependency)
	}
	if len(providers) == 1 {
		return providers[0], true, nil
	}
	return "", false, nil
}

// DependencyName returns the validated package name portion of an Arch dependency.
func DependencyName(value string) string {
	dependency, err := ParseDependency(value)
	if err != nil {
		return ""
	}
	return dependency.Name
}

// ParseDependency parses a package dependency with an optional Arch comparison.
func ParseDependency(value string) (Dependency, error) {
	return parseExpression(value, false)
}

// OptionalDependencyExpression separates an optdepends expression from its
// human-readable description without treating an epoch colon as a separator.
func OptionalDependencyExpression(value string) (string, error) {
	value = strings.TrimSpace(value)
	if index := strings.Index(value, ": "); index >= 0 {
		value = strings.TrimSpace(value[:index])
	} else if _, err := ParseDependency(value); err != nil {
		value, _, _ = strings.Cut(value, ":")
		value = strings.TrimSpace(value)
	}
	if _, err := ParseDependency(value); err != nil {
		return "", err
	}
	return value, nil
}

// ParseOptionalDependency validates an optdepends value and returns only its
// package dependency expression.
func ParseOptionalDependency(value string) (Dependency, error) {
	expression, err := OptionalDependencyExpression(value)
	if err != nil {
		return Dependency{}, err
	}
	return ParseDependency(expression)
}

// ValidFingerprint accepts only canonical full OpenPGP fingerprints used by
// makepkg validpgpkeys: uppercase hexadecimal with no whitespace or key IDs.
func ValidFingerprint(value string) bool { return fingerprintPattern.MatchString(value) }

// ParseProvide parses an unversioned or exactly-versioned provides entry.
func ParseProvide(value string) (Dependency, error) {
	return parseExpression(value, true)
}

func parseExpression(value string, provide bool) (Dependency, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\t\r\n ") {
		return Dependency{}, fmt.Errorf("invalid dependency expression %q", value)
	}
	operator, index := "", -1
	for i := 0; i < len(value); i++ {
		if !strings.ContainsRune("<>=", rune(value[i])) {
			continue
		}
		index = i
		operator = value[i : i+1]
		if i+1 < len(value) && (value[i] == '<' || value[i] == '>') && value[i+1] == '=' {
			operator = value[i : i+2]
		}
		break
	}
	name, version := value, ""
	if index >= 0 {
		name = value[:index]
		version = value[index+len(operator):]
	}
	if !dependencyNamePattern.MatchString(name) || (operator != "" && version == "") || strings.ContainsAny(version, "<>=\t\r\n ") {
		return Dependency{}, fmt.Errorf("invalid dependency expression %q", value)
	}
	if provide && operator != "" && operator != "=" {
		return Dependency{}, fmt.Errorf("invalid provides expression %q", value)
	}
	return Dependency{Name: name, Operator: operator, Version: version}, nil
}

func versionSatisfies(version string, requirement Dependency, compare VersionComparator) (bool, error) {
	if requirement.Operator == "" {
		return true, nil
	}
	if compare == nil {
		return false, fmt.Errorf("Arch version comparison is unavailable for %q", requirement.Name+requirement.Operator+requirement.Version)
	}
	result, err := compare(version, requirement.Version)
	if err != nil {
		return false, err
	}
	return comparisonSatisfied(result, requirement.Operator), nil
}

func provideSatisfies(provision, requirement Dependency, compare VersionComparator) (bool, error) {
	if requirement.Operator == "" {
		return true, nil
	}
	if provision.Operator == "" {
		return false, nil
	}
	return versionSatisfies(provision.Version, requirement, compare)
}

func comparisonSatisfied(result int, operator string) bool {
	switch operator {
	case "=":
		return result == 0
	case ">=":
		return result >= 0
	case "<=":
		return result <= 0
	case ">":
		return result > 0
	case "<":
		return result < 0
	default:
		return false
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func normalized(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func uniqueRequirements(values []Requirement) []Requirement {
	seen := make(map[string]bool)
	result := make([]Requirement, 0, len(values))
	for _, value := range values {
		key := value.Expression + "\x00" + value.Purpose
		if !seen[key] {
			seen[key] = true
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Expression == result[j].Expression {
			return result[i].Purpose < result[j].Purpose
		}
		return result[i].Expression < result[j].Expression
	})
	return result
}
