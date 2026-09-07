// Package app orchestrates detection, inspection, resolution, planning, application, and verification.
package app

import (
	"context"
	"io"
	"net/http"
	"os"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/inspect"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/system"
)

const (
	Success = 0
	Issues  = 1
	Fatal   = 2

	actionInstall      = "install"
	actionConfigure    = "configure"
	actionUpgrade      = "upgrade"
	actionEnable       = "enable"
	actionAuthenticate = "authenticate"
	actionReview       = "review"
	actionInspect      = "inspect"

	fullUpgradeDetail = "pacman; confirm transaction in pacman"
)

// Runtime holds process-scoped dependencies.
type Runtime struct {
	Runner         run.Runner
	Out            io.Writer
	Err            io.Writer
	Home           string
	EUID           func() int
	OSRelease      string
	PacmanConf     string
	SSHHTTP        *http.Client
	SSHMetadataURL string
	presentation   *presentation
}

type issue struct {
	State, Name, Source, Stage, Cause, Impact, Action string
}

type presentation struct {
	progressStarted bool
	reviewActive    bool
}

func (a Runtime) detect(ctx context.Context) error {
	return (system.Detector{EUID: a.EUID, OSRelease: a.OSRelease, Runner: a.Runner}).Detect(ctx)
}

func (a Runtime) inspectState(ctx context.Context, cfg config.Config) (plan.State, error) {
	return (inspect.Workstation{
		Applications: cfg.Applications, Runner: a.Runner, Home: a.Home, PacmanConf: a.PacmanConf,
		SSHHTTP: a.SSHHTTP, SSHMetadataURL: a.SSHMetadataURL,
	}).State(ctx)
}

func DefaultRuntime() Runtime {
	home, _ := os.UserHomeDir()
	return Runtime{Runner: run.Exec{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}, Out: os.Stdout, Err: os.Stderr, Home: home, EUID: os.Geteuid}
}
