// Package ui implements stable ASCII-first terminal interaction.
package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// UI is a line-oriented terminal user interface.
type UI struct {
	In  io.Reader
	Out io.Writer
}

// TableRow is one plain-text presentation row with distinct value columns.
type TableRow struct {
	Item   string
	Action string
	Detail string
}

// Field is a labeled, plain-text value. RenderFields keeps every continuation
// aligned under its value so command errors cannot take over the terminal.
type Field struct {
	Name  string
	Value string
}

// RenderTable aligns rows from their actual content and never emits terminal controls.
func RenderTable(rows []TableRow) string {
	rows = append([]TableRow(nil), rows...)
	for i := range rows {
		rows[i].Item = printableASCII(rows[i].Item)
		rows[i].Action = printableASCII(rows[i].Action)
		rows[i].Detail = printableASCII(rows[i].Detail)
	}
	itemWidth, actionWidth := 0, 0
	for _, row := range rows {
		itemWidth = max(itemWidth, len(row.Item))
		actionWidth = max(actionWidth, len(row.Action))
	}
	var b strings.Builder
	for _, row := range rows {
		b.WriteString("  ")
		b.WriteString(row.Item)
		if row.Action != "" || row.Detail != "" {
			b.WriteString(strings.Repeat(" ", itemWidth-len(row.Item)+2))
			if row.Action != "" {
				b.WriteString(row.Action)
			}
		}
		if row.Detail != "" {
			b.WriteString(strings.Repeat(" ", actionWidth-len(row.Action)+2))
			b.WriteString(row.Detail)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func printableASCII(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 0x20 && r <= 0x7e {
			b.WriteRune(r)
			continue
		}
		b.WriteString(strings.Trim(strconv.QuoteToASCII(string(r)), `"`))
	}
	return b.String()
}

// PrintableASCII returns a terminal-safe ASCII representation of value.
func PrintableASCII(value string) string { return printableASCII(value) }

// RenderFields renders stable key/value fields. Newlines are preserved as
// aligned continuations; every other control or non-ASCII rune is escaped.
func RenderFields(fields []Field) string {
	width := 0
	for _, field := range fields {
		width = max(width, len(printableASCII(field.Name)))
	}
	var b strings.Builder
	for _, field := range fields {
		name := printableASCII(field.Name)
		lines := strings.Split(fieldValue(field.Value), "\n")
		for i, line := range lines {
			if i == 0 {
				fmt.Fprintf(&b, "  %-*s  %s\n", width, name, line)
				continue
			}
			b.WriteString("  ")
			b.WriteString(strings.Repeat(" ", width))
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func fieldValue(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r == '\n' {
			b.WriteByte('\n')
			continue
		}
		b.WriteString(printableASCII(string(r)))
	}
	return b.String()
}

// RenderReviewFile deliberately exposes reviewed source while escaping every
// terminal control character. Newlines remain structural content boundaries.
func RenderReviewFile(name, contents string) string {
	return "File " + printableASCII(name) + "\n" + fieldValue(contents) + "\n"
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
