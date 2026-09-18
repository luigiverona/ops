package archrepo

import (
	"context"
	"fmt"
	"github.com/luigiverona/ops/internal/run"
	"strconv"
	"strings"
)

type Authenticity string

const (
	VerifiedOfficial         Authenticity = "verified official"
	InvalidOfficialContent   Authenticity = "invalid official content"
	AuthenticityInconclusive Authenticity = "authenticity inconclusive"
)

type Currency string

const (
	Current             Currency = "current"
	OlderThanCurrent    Currency = "older than current"
	NewerThanCurrent    Currency = "newer than current"
	CurrencyUnavailable Currency = "currency unavailable"
)

type Action string

const (
	NoAction Action = "none"
	Update   Action = "update"
	Repair   Action = "repair"
	Manual   Action = "manual"
)

type InstalledState struct {
	Target, InstalledVersion, CurrentVersion string
	Authenticity                             Authenticity
	Currency                                 Currency
}

func (s InstalledState) Ready() bool {
	return s.Authenticity == VerifiedOfficial && s.Currency == Current
}
func (s InstalledState) Action() Action {
	// Never turn lagging source observations into a downgrade.
	if s.Currency == NewerThanCurrent || s.Currency == CurrencyUnavailable {
		return Manual
	}
	if s.Authenticity == InvalidOfficialContent {
		return Repair
	}
	if s.Currency == OlderThanCurrent {
		return Update
	}
	if s.Ready() {
		return NoAction
	}
	return Manual
}
func (s InstalledState) Description() string {
	if s.Authenticity == InvalidOfficialContent {
		if s.Currency == NewerThanCurrent {
			return "installed official package content is invalid; installed version is newer than the trusted source; reconcile manually (no automatic downgrade)"
		}
		return "installed official package content requires repair"
	}
	if s.Currency == NewerThanCurrent {
		return string(s.Authenticity) + "; installed version is newer than the trusted source; reconcile manually (no automatic downgrade)"
	}
	if s.Authenticity != VerifiedOfficial {
		if s.Currency == OlderThanCurrent {
			return "official package authenticity could not be established for historical installed version; official package update available"
		}
		return "official package authenticity could not be established"
	}
	if s.Currency == OlderThanCurrent {
		return "verified official package; official package update available"
	}
	if s.Ready() {
		return "ready"
	}
	return "official source currency unavailable; reconcile manually"
}
func CompareVersions(ctx context.Context, runner run.Runner, left, right string) (int, error) {
	result, err := runner.Run(ctx, run.Spec{Name: "vercmp", Args: []string{left, right}})
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(result.Stdout)
	if len(fields) != 1 {
		return 0, fmt.Errorf("vercmp returned ambiguous output")
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil || n < -1 || n > 1 {
		return 0, fmt.Errorf("vercmp returned invalid output")
	}
	return n, nil
}
