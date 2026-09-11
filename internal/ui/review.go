package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ReviewFile is presentation data, never input to a build or metadata parser.
type ReviewFile struct {
	Name, Contents string
}

// ReviewSource identifies the pinned source whose files are being displayed.
type ReviewSource struct{ Package, PackageBase, Revision string }

var ErrReviewCancelled = errors.New("source review cancelled")

// Review keeps build instructions in a separate, line-oriented terminal view.
// No shell, editor, pager hooks, temporary files, or additional packages are
// needed. Every page must be visited before returning to the install approval.
// Plain/dumb terminals retain pagination without screen-control sequences.
func (u UI) Review(ctx context.Context, source ReviewSource, files []ReviewFile) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	type page struct{ name, text string }
	var pages []page
	for _, file := range files {
		// Escape before wrapping so source cannot inject controls or prompts.
		var lines []string
		for _, line := range strings.Split(fieldValue(file.Contents), "\n") {
			for len(line) > 72 {
				lines = append(lines, line[:72])
				line = line[72:]
			}
			lines = append(lines, line)
		}
		for start := 0; start < len(lines); start += 12 {
			pages = append(pages, page{printableASCII(file.Name), strings.Join(lines[start:min(start+12, len(lines))], "\n")})
		}
	}
	if len(pages) == 0 {
		return errors.New("no build files to review")
	}
	alternate := false
	if terminal, ok := u.Out.(*os.File); ok && os.Getenv("TERM") != "" && os.Getenv("TERM") != "dumb" {
		info, err := terminal.Stat()
		alternate = err == nil && info.Mode()&os.ModeCharDevice != 0
	}
	if alternate {
		defer func() {
			_, err := io.WriteString(u.Out, "\x1b[?1049l")
			returnErr = errors.Join(returnErr, err)
		}()
		if _, err := io.WriteString(u.Out, "\x1b[?1049h"); err != nil {
			return err
		}
	}
	for index := 0; index < len(pages); {
		if err := ctx.Err(); err != nil {
			return err
		}
		if alternate {
			if _, err := io.WriteString(u.Out, "\x1b[H\x1b[2J"); err != nil {
				return err
			}
		}
		page := pages[index]
		provenance := "Package: " + printableASCII(source.Package) + "\n"
		if source.PackageBase != source.Package {
			provenance += "Package base: " + printableASCII(source.PackageBase) + "\n"
		}
		provenance += "Revision: " + printableASCII(source.Revision) + "\n"
		if _, err := fmt.Fprintf(u.Out, "AUR source review (%d/%d) - untrusted build instructions\n%s\n%s\n\n%s\n\n", index+1, len(pages), provenance, page.name, page.text); err != nil {
			return err
		}
		next := "next page"
		if index == len(pages)-1 {
			next = "finish review"
		}
		if _, err := fmt.Fprintf(u.Out, "Enter: %s; b: back; q: skip application > ", next); err != nil {
			return err
		}
		answer, err := readLine(ctx, u.In)
		if err != nil {
			return fmt.Errorf("source review interrupted: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "":
			index++
		case "b":
			index = max(0, index-1)
		case "q":
			return ErrReviewCancelled
		default:
			if _, err := fmt.Fprintln(u.Out, "Use Enter, b, or q. Installation approval comes after review."); err != nil {
				return err
			}
		}
	}
	return nil
}
