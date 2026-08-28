// Package ui implements stable ASCII-first terminal interaction.
package ui

import (
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
	for {
		if _, err := fmt.Fprint(u.Out, question+suffix); err != nil {
			return false, err
		}
		line, err := readLine(u.In)
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
			if _, writeErr := fmt.Fprintln(u.Out, "Enter yes or no."); writeErr != nil {
				return false, writeErr
			}
		}
	}
}

// Ask reads a required trimmed value.
func (u UI) Ask(question string) (string, error) {
	if _, err := fmt.Fprint(u.Out, question+" "); err != nil {
		return "", err
	}
	line, err := readLine(u.In)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return "", errors.New("a value is required")
	}
	return value, nil
}

func readLine(reader io.Reader) (string, error) {
	var b strings.Builder
	buffer := []byte{0}
	for {
		n, err := reader.Read(buffer)
		if n == 1 {
			if buffer[0] == '\n' {
				return b.String(), nil
			}
			b.WriteByte(buffer[0])
		}
		if err != nil {
			return b.String(), err
		}
	}
}
