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
func (a Runtime) Update(ctx context.Context) int {
	if err := a.detect(ctx); err != nil {
		return a.fatal(err)
	}
	if _, err := release.CompareVersions(version.Value, version.Value); err != nil {
		return a.fatal(fmt.Errorf("current version %q is not updateable; install a stable release", version.Value))
	}
	client := release.Client{Runner: a.Runner, Trust: release.DefaultTrust()}
	latest, err := client.Latest(ctx)
	if err != nil {
		return a.fatal(fmt.Errorf("resolve latest release: %w", err))
	}
	comparison, err := release.CompareVersions(version.Value, latest)
	if err != nil {
		return a.fatal(err)
	}
	if comparison >= 0 {
		fmt.Fprintf(a.Out, "Update\n  current         %s\n  latest          %s\n  status          up to date\n", version.Value, latest)
		return Success
	}
	tty, err := ui.OpenTTY()
	if err != nil {
		return a.fatal(err)
	}
	defer tty.Close()
	if _, ok := a.Runner.(run.Exec); ok {
		a.Runner = run.Exec{In: tty, Out: a.Out, Err: a.Err}
	}
	terminal := ui.UI{In: tty, Out: tty}
	fmt.Fprintf(a.Out, "Update\n  current         %s\n  latest          %s\n  target          /usr/local/bin/ops\n", version.Value, latest)
	ok, err := terminal.Confirm("Download and verify this update?", true)
	if err != nil {
		return a.fatal(err)
	}
	if !ok {
		fmt.Fprintln(a.Out, "Update skipped.")
		return Success
	}
	verified, err := client.DownloadVerified(ctx, latest)
	if err != nil {
		return a.fatal(err)
	}
	defer verified.Close()
	fmt.Fprintf(a.Out, "Verified\n  release         %s\n  signature       valid\n  sha256          valid\n", latest)
	a.presentation = &presentation{}
	a.showProgress("sudo", actionConfigure, "install verified update")
	a.showExternal("sudo", "password prompt")
	keeper, err := sudoops.Acquire(ctx, a.Runner)
	if err != nil {
		return a.fatal(fmt.Errorf("sudo authorization failed: %w", err))
	}
	defer keeper.Close()
	if err := release.Replace(ctx, a.Runner, verified.Binary, "/usr/local/bin/ops", latest); err != nil {
		return a.fatal(err)
	}
	fmt.Fprintf(a.Out, "Updated ops to %s.\n", latest)
	return Success
}
