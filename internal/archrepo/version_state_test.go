package archrepo_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/testpkg"
)

func TestInstalledAuthenticityAndCurrency(t *testing.T) {
	for _, tc := range []struct {
		name, installed, current string
		forged, missing          bool
		authenticity             archrepo.Authenticity
		currency                 archrepo.Currency
		action                   archrepo.Action
	}{
		{"current", "1-1", "1-1", false, false, archrepo.VerifiedOfficial, archrepo.Current, archrepo.NoAction},
		{"outdated", "1-1", "2-1", false, false, archrepo.VerifiedOfficial, archrepo.OlderThanCurrent, archrepo.Update},
		{"pkgrel", "1-9", "1-10", false, false, archrepo.VerifiedOfficial, archrepo.OlderThanCurrent, archrepo.Update},
		{"epoch", "9-20", "1:1-1", false, false, archrepo.VerifiedOfficial, archrepo.OlderThanCurrent, archrepo.Update},
		{"forged old", "1-1", "2-1", true, false, archrepo.InvalidOfficialContent, archrepo.OlderThanCurrent, archrepo.Repair},
		{"unknown old", "1-1", "2-1", false, true, archrepo.AuthenticityInconclusive, archrepo.OlderThanCurrent, archrepo.Update},
		{"unknown forged old", "1-1", "2-1", true, true, archrepo.AuthenticityInconclusive, archrepo.OlderThanCurrent, archrepo.Update},
		{"source behind", "2:1-1", "1:99-9", false, false, archrepo.VerifiedOfficial, archrepo.NewerThanCurrent, archrepo.Manual},
		{"unknown ahead", "2-1", "1-1", false, true, archrepo.AuthenticityInconclusive, archrepo.NewerThanCurrent, archrepo.Manual},
		{"forged ahead", "2-1", "1-1", true, false, archrepo.InvalidOfficialContent, archrepo.NewerThanCurrent, archrepo.Manual},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := testpkg.NewPacmanFixture(t)
			old := testpkg.FixturePackage{Name: "git", Version: tc.installed, Packager: "Official", Payload: "genuine N"}
			f.Sync(t, "core")
			f.Sync(t, "extra", old)
			local := old
			if tc.forged {
				local.Payload = "forged N"
			}
			f.Local(t, local)
			current := old
			current.Version = tc.current
			if tc.current != tc.installed {
				current.Payload = "genuine current"
			}
			f.Sync(t, "extra", current)
			if tc.missing {
				f.ForgetEvidence("extra/git", tc.installed)
			}
			state, err := archrepo.InspectInstalled(context.Background(), f, "extra/git")
			if err != nil || state.Authenticity != tc.authenticity || state.Currency != tc.currency || state.Action() != tc.action {
				t.Fatalf("state=%+v action=%s err=%v", state, state.Action(), err)
			}
			if state.Ready() != (tc.action == archrepo.NoAction) {
				t.Fatal("incorrect readiness")
			}
			if tc.currency == archrepo.OlderThanCurrent && !tc.forged {
				// Successful full upgrade resolves currency and missing historical evidence.
				if err := os.RemoveAll(filepath.Join(f.Dir, "db/local/git-"+tc.installed)); err != nil {
					t.Fatal(err)
				}
				f.Local(t, current)
				for range 2 {
					state, err := archrepo.InspectInstalled(context.Background(), f, "extra/git")
					if err != nil || !state.Ready() {
						t.Fatalf("upgrade/idempotence: %+v %v", state, err)
					}
				}
			}
		})
	}
}
