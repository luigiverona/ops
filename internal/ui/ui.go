// Package ui implements stable ASCII-first terminal interaction.
package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// UI is a line-oriented terminal user interface.
type UI struct {
	In  io.Reader
	Out io.Writer
}

// OpenTTY opens the controlling terminal for interactive preparation.
func OpenTTY() (*os.File, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, errors.New("interactive workstation preparation requires a usable TTY")
	}
	return f, nil
}

// Confirm asks a yes/no question and returns the supplied default on blank input.
func (u UI) Confirm(question string, defaultYes bool) (bool, error) {
	suffix := " [y/N] "
	if defaultYes {
		suffix = " [Y/n] "
	}
	if _, err := fmt.Fprint(u.Out, question+suffix); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(u.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return defaultYes, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid response %q; enter yes or no", strings.TrimSpace(line))
	}
}

// Ask reads a required trimmed value.
func (u UI) Ask(question string) (string, error) {
	if _, err := fmt.Fprint(u.Out, question+" "); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(u.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return "", errors.New("a value is required")
	}
	return value, nil
}
