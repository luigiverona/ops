// Package arch implements safe Arch Linux system preparation.
package arch

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// MultilibEnabled reports whether exactly one usable multilib section is enabled.
func MultilibEnabled(data []byte) (bool, error) {
	sections, err := multilibSections(data)
	if err != nil {
		return false, err
	}
	if len(sections) > 1 {
		return false, errors.New("pacman.conf contains duplicate multilib sections")
	}
	if len(sections) == 0 || sections[0].commented {
		return false, nil
	}
	return sections[0].include, nil
}

type section struct {
	start, end int
	commented  bool
	include    bool
}

func multilibSections(data []byte) ([]section, error) {
	lines := strings.Split(string(data), "\n")
	var result []section
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		content := trimmed
		commented := false
		if strings.HasPrefix(content, "#") {
			commented = true
			content = strings.TrimSpace(strings.TrimPrefix(content, "#"))
		}
		if content != "[multilib]" {
			continue
		}
		s := section{start: i, end: len(lines), commented: commented}
		for j := i + 1; j < len(lines); j++ {
			next := strings.TrimSpace(lines[j])
			plain := strings.TrimSpace(strings.TrimPrefix(next, "#"))
			if strings.HasPrefix(plain, "[") && strings.HasSuffix(plain, "]") {
				s.end = j
				break
			}
			if !strings.HasPrefix(next, "#") && strings.HasPrefix(next, "Include") {
				left, _, ok := strings.Cut(next, "=")
				s.include = s.include || (ok && strings.TrimSpace(left) == "Include")
			}
		}
		result = append(result, s)
	}
	return result, nil
}

// EnableMultilib minimally uncomments or appends the canonical section.
func EnableMultilib(data []byte) ([]byte, error) {
	sections, err := multilibSections(data)
	if err != nil {
		return nil, err
	}
	if len(sections) > 1 {
		return nil, errors.New("pacman.conf contains duplicate multilib sections")
	}
	if len(sections) == 1 && !sections[0].commented && sections[0].include {
		return append([]byte(nil), data...), nil
	}
	lines := strings.Split(string(data), "\n")
	if len(sections) == 0 {
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "[multilib]", "Include = /etc/pacman.d/mirrorlist", "")
	} else {
		s := sections[0]
		for i := s.start; i < s.end; i++ {
			trimmed := strings.TrimSpace(lines[i])
			plain := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
			if i == s.start || strings.HasPrefix(plain, "Include") {
				indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " \t"))]
				lines[i] = indent + plain
			}
		}
		if !strings.Contains(strings.Join(lines[s.start:s.end], "\n"), "Include") {
			lines = append(lines[:s.start+1], append([]string{"Include = /etc/pacman.d/mirrorlist"}, lines[s.start+1:]...)...)
		}
	}
	result := []byte(strings.Join(lines, "\n"))
	enabled, err := MultilibEnabled(result)
	if err != nil || !enabled {
		return nil, fmt.Errorf("generated pacman.conf did not enable multilib: %w", err)
	}
	return result, nil
}

// ValidatePacmanConf performs conservative structural validation independent of pacman-conf.
func ValidatePacmanConf(data []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	line := 0
	for scanner.Scan() {
		line++
		value := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(value, "[") && !strings.HasSuffix(value, "]") {
			return fmt.Errorf("malformed section at line %d", line)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_, err := MultilibEnabled(data)
	return err
}
