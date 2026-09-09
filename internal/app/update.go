package app

import (
	"context"
	"fmt"

	"github.com/luigiverona/ops/internal/release"
	"github.com/luigiverona/ops/internal/run"
	sudoops "github.com/luigiverona/ops/internal/sudo"
	"github.com/luigiverona/ops/internal/ui"
	"github.com/luigiverona/ops/internal/version"
)

// Update installs a newer release only after signature and checksum verification.
func (a Runtime) Update(ctx context.Context) (code int) {
	a, finish := a.withInterruption(ctx, "update")
	defer finish(&code)
	if ctx.Err() != nil {
		return Fatal
	}
	if err := a.detect(ctx); err != nil {
		return a.updateFatal(err)
	}
	if _, err := release.CompareVersions(version.Value, version.Value); err != nil {
		return a.updateFatal(fmt.Errorf("current version %q is not updateable; install a stable release", version.Value))
	}
	client := release.Client{Runner: a.Runner, Trust: release.DefaultTrust()}
	latest, err := client.Latest(ctx)
	if err != nil {
		return a.updateFatal(fmt.Errorf("resolve latest release: %w", err))
	}
	comparison, err := release.CompareVersions(version.Value, latest)
	if err != nil {
		return a.updateFatal(err)
	}
	if ctx.Err() != nil {
		return Fatal
	}
	if comparison >= 0 {
		if !a.claimConclusion() {
			return Fatal
		}
		fmt.Fprintf(a.Out, "ops %s is up to date.\n", ui.PrintableASCII(version.Value))
		return Success
	}
	tty, err := ui.OpenTTY()
	if err != nil {
		return a.updateFatal(err)
	}
	defer tty.Close()
	if guarded, ok := a.Runner.(cancellationRunner); ok {
		if _, ok := guarded.Runner.(run.Exec); ok {
			a.Runner = cancellationRunner{run.Exec{In: tty, Out: a.Out, Err: a.Err}}
		}
	}
	terminal := ui.UI{In: tty, Out: tty}
	return a.installUpdate(ctx, client, latest, terminal)
}

// installUpdate retains one approval before download, verification, and sudo.
func (a Runtime) installUpdate(ctx context.Context, client release.Client, latest string, terminal ui.UI) (code int) {
	a, finish := a.withInterruption(ctx, "update")
	defer finish(&code)
	if ctx.Err() != nil {
		return Fatal
	}
	fmt.Fprintf(a.Out, "Update ops %s to %s.\n", ui.PrintableASCII(version.Value), ui.PrintableASCII(latest))
	ok, err := terminal.Confirm(ctx, "Download, verify, and install to /usr/local/bin/ops?", true)
	if err != nil {
		return a.updateFatal(err)
	}
	if !ok {
		if !a.claimConclusion() {
			return Fatal
		}
		fmt.Fprintln(a.Out, "No changes made.")
		return Success
	}
	a.progress("Downloading and verifying update...")
	verified, err := client.DownloadVerified(ctx, latest)
	if err != nil {
		return a.updateFatal(err)
	}
	defer verified.Close()
	fmt.Fprintf(a.Out, "ops %s verified.\n", ui.PrintableASCII(latest))
	a.progress("Installing verified update...")
	keeper, err := sudoops.Acquire(ctx, a.Runner)
	if err != nil {
		return a.updateFatal(fmt.Errorf("sudo authorization failed: %w", err))
	}
	defer keeper.Close()
	if err := a.beginMutation(ctx); err != nil {
		return Fatal
	}
	if err := release.Replace(ctx, a.Runner, verified.Binary, "/usr/local/bin/ops", latest); err != nil {
		if a.interrupted() {
			fmt.Fprintln(a.Err, ui.PrintableASCII(err.Error()))
		}
		a.renderFatal("ops update", "", err, "replacement did not complete; check ops --version and any reported recovery failure before retrying")
		return Fatal
	}
	if ctx.Err() != nil {
		return Fatal
	}
	if !a.claimConclusion() {
		return Fatal
	}
	fmt.Fprintf(a.Out, "Updated ops to %s.\n", ui.PrintableASCII(latest))
	return Success
}

func (a Runtime) updateFatal(err error) int {
	a.renderFatal("ops update", "", err, "the installed ops binary was not changed")
	return Fatal
}
