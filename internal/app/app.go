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
}

type issue struct {
	State, Name, Source, Stage, Cause, Impact, Action string
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
