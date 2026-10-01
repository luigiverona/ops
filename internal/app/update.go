package app

import (
	"context"
	"fmt"

	"github.com/luigiverona/ops/internal/archrepo"
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
		switch runner := guarded.Runner.(type) {
		case run.Exec:
			a.Runner = cancellationRunner{runner.WithIO(tty, a.Out, a.Err)}
		case *archrepo.TrustedRunner:
			if executor, ok := runner.Runner.(run.Exec); ok {
				a.Runner = cancellationRunner{runner.WithRunner(executor.WithIO(tty, a.Out, a.Err))}
			}
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
	if err := a.activateOwnership(ctx); err != nil {
		return a.updateFatal(err)
	}
	// Download/verification begins only after ownership; all verification commands
	// use the same controller and the rebound terminal as installation.
	client.Runner = a.Runner
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
	keeperClosed := false
	defer func() {
		if keeperClosed {
			return
		}
		if err := keeper.Close(); err != nil && run.OwnershipFailed(err) {
			a.renderFatal("ops update", "", err, "subprocess cleanup did not complete; check ops --version before retrying")
			code = Fatal
		}
	}()
	if err := a.beginMutation(ctx); err != nil {
		return a.updateFatal(err)
	}
	if err := release.Replace(ctx, a.Runner, verified.Binary, "/usr/local/bin/ops", latest); err != nil {
		if a.interrupted() {
			fmt.Fprintln(a.Err, ui.PrintableASCII(err.Error()))
		}
		a.renderFatal("ops update", "", err, "replacement did not complete; check ops --version and any reported recovery failure before retrying")
		return Fatal
	}
	keeperClosed = true
	if err := keeper.Close(); run.OwnershipFailed(err) {
		a.renderFatal("ops update", "", err, "subprocess cleanup did not complete; check ops --version before retrying")
		return Fatal
	}
	if err := a.mutationHealthy(ctx); err != nil {
		a.renderFatal("ops update", "", err, "check ops --version before retrying")
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
