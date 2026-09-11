package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/version"
)

// Run the real dispatcher in a child so its exit status and output streams are
// checked without requiring an Arch host, a configuration file, or a terminal.
func TestCLIProcess(t *testing.T) {
	if os.Getenv("OPS_CLI_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"ops"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	os.Exit(99)
}

func TestCLIHelpVersionAndInvalidCommands(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		kind string
	}{
		{[]string{"--help"}, "help"},
		{[]string{"-h"}, "help"},
		{[]string{"--version"}, "version"},
		{[]string{"-v"}, "version"},
		{[]string{"unknown"}, "invalid"},
		{[]string{"--unknown"}, "invalid"},
		{[]string{"doctor", "--help"}, "invalid"},
		{[]string{"update", "--help"}, "invalid"},
		{[]string{"doctor", "extra"}, "invalid"},
		{[]string{"update", "extra"}, "invalid"},
		{[]string{"--help", "extra"}, "invalid"},
		{[]string{"--version", "extra"}, "invalid"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			cmd := exec.Command(binary, append([]string{"-test.run=^TestCLIProcess$", "--"}, tc.args...)...)
			cmd.Env = append(os.Environ(), "OPS_CLI_TEST_PROCESS=1")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			wantCode := 0
			if tc.kind == "invalid" {
				wantCode = 2
			}
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != wantCode {
				t.Fatalf("want exit %d: %v; stdout=%q stderr=%q", wantCode, err, &stdout, &stderr)
			}
			if tc.kind == "invalid" {
				if stdout.Len() != 0 || stderr.String() != "ops: invalid command; run 'ops --help'\n" {
					t.Fatalf("stdout=%q stderr=%q", &stdout, &stderr)
				}
				return
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr: %s", &stderr)
			}
			if tc.kind == "version" {
				if stdout.String() != "ops "+version.Value+"\n" {
					t.Fatalf("version=%q", &stdout)
				}
				return
			}
			roles := map[string][]string{
				"ops":            {"Reconcile", "applications", "configuration"},
				"ops doctor":     {"persistent", "readiness", "without changes"},
				"ops update":     {"ops itself", "verified", "signed", "stable release"},
				"ops --help,":    {"-h", "help"},
				"ops --version,": {"-v", "installed", "version"},
			}
			for command, words := range roles {
				found := false
				for _, line := range strings.Split(stdout.String(), "\n") {
					if !strings.HasPrefix(strings.TrimSpace(line), command+" ") {
						continue
					}
					matches := true
					for _, word := range words {
						matches = matches && strings.Contains(line, word)
					}
					found = found || matches
				}
				if !found {
					t.Errorf("missing role for %s: %s", command, &stdout)
				}
			}
			for _, want := range []string{"Arch Linux x86_64", "~/.config/ops/apps.toml", "normal user", "terminal approval"} {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("missing %q: %s", want, &stdout)
				}
			}
			for _, hidden := range []string{"Plan", "Progress", "Review", "Final"} {
				if strings.Contains(stdout.String(), hidden) {
					t.Errorf("internal lifecycle label %q in help", hidden)
				}
			}
		})
	}
}
