package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/luigiverona/ops/internal/app"
	"github.com/luigiverona/ops/internal/version"
)

const help = `ops reconciles an official Arch Linux x86_64 workstation.

Usage:
  ops                 Reconcile declared applications and managed configuration
  ops doctor          Check persistent workstation readiness without changes
  ops update          Update ops itself from a verified signed stable release
  ops --help, -h       Show this help
  ops --version, -v    Show the installed ops version

Applications: ~/.config/ops/apps.toml
Run as your normal user. Setup changes and updates require terminal approval.`

func main() {
	args := os.Args[1:]
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println(help)
		return
	}
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v") {
		fmt.Printf("ops %s\n", version.Value)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime := app.DefaultRuntime()
	code := app.Fatal
	switch {
	case len(args) == 0:
		code = runtime.Prepare(ctx)
	case len(args) == 1 && args[0] == "doctor":
		code = runtime.Doctor(ctx)
	case len(args) == 1 && args[0] == "update":
		code = runtime.Update(ctx)
	default:
		fmt.Fprintln(os.Stderr, "ops: invalid command; run 'ops --help'")
	}
	os.Exit(code)
}
