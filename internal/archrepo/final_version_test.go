package archrepo_test

import (
	"context"
	"testing"

	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/testpkg"
)

// Preserved D-F5 regression for installed-version evidence and currency. Repository movement alone must not
// return the same definite mismatch used to plan forged-content repair.
func TestFinalRepositoryAdvanceIsNotInstalledContentMismatch(t *testing.T) {
	f := testpkg.NewPacmanFixture(t)
	old := testpkg.FixturePackage{Name: "git", Version: "1-1", Packager: "Official", Payload: "genuine installed N"}
	f.Sync(t, "core")
	f.Sync(t, "extra", old)
	f.Local(t, old)
	if match, err := archrepo.InstalledMatch(context.Background(), f, "extra/git"); err != nil || !match {
		t.Fatalf("genuine N control failed: %v %v", match, err)
	}
	next := old
	next.Version, next.Payload = "2-1", "genuine N+1"
	f.Sync(t, "extra", next)
	// No installed metadata, payload, ownership, permissions or cache changed.
	state, err := archrepo.InspectInstalled(context.Background(), f, "extra/git")
	if err != nil || state.Authenticity != archrepo.VerifiedOfficial || state.Currency != archrepo.OlderThanCurrent || state.Action() != archrepo.Update {
		t.Fatalf("D-F5: exact installed N evidence lost: %+v %v", state, err)
	}

	match, err := archrepo.InstalledMatch(context.Background(), f, "extra/git")
	if !match && err == nil {
		t.Fatal("D-F5: genuine unchanged version N classified as definite content mismatch solely because the repository advanced to N+1")
	}
}
