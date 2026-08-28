package main

import (
	"fmt"
	"os"

	"github.com/luigiverona/ops/internal/version"
)

const help = `ops prepares an official Arch Linux x86_64 workstation.

Usage:
  ops
  ops doctor
  ops update
  ops --help
  ops --version`

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
	fmt.Fprintln(os.Stderr, "ops: implementation is incomplete")
	os.Exit(2)
}
