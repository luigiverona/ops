package inspect

import (
	"context"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
)

type customNativeRunner struct{ stateRunner }

func (r *customNativeRunner) Run(ctx context.Context, s run.Spec) (run.Result, error) {
	result, err := r.stateRunner.Run(ctx, s)
	if s.Name == "pacman" {
		switch s.Args[0] {
		case "-Qq", "-Qeq":
			result.Stdout += "firefox\n"
		case "-Sl":
			result.Stdout = strings.ReplaceAll(result.Stdout, "extra firefox 1-1", "custom firefox 1-1 [installed]")
		}
	}
	return result, err
}
func TestLocalInspectionDoesNotUseNativeAsOfficialEvidence(t *testing.T) {
	app := config.Application{Source: config.Pacman, Identifier: "firefox"}
	state, err := (Workstation{Home: t.TempDir(), PacmanConf: testPacmanConf(t), Runner: &customNativeRunner{}, Applications: []config.Application{app}}).Local(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed["firefox"] || state.Foreign["firefox"] {
		t.Fatal("fixture must represent custom native package")
	}
	if plan.IsInstalled(app, state) || state.OfficialMatches["firefox"] != "" {
		t.Fatal("custom native package accepted as official")
	}
}
