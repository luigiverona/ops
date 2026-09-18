package resolve

import (
	"context"
	"github.com/luigiverona/ops/internal/archrepo"
	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/testpkg"
	"strings"
	"testing"
)

func TestAURProviderVersionDrift(t *testing.T) {
	for _, requirement := range []string{"builder", "builder>=1", "builder>=2", "compiler", "compiler>=1", "compiler>=2"} {
		for _, mode := range []string{"verified", "forged", "inconclusive"} {
			t.Run(requirement+"/"+mode, func(t *testing.T) {
				f := testpkg.NewPacmanFixture(t)
				f.Sync(t, "custom")
				old := testpkg.FixturePackage{Name: "builder", Version: "1-1", Packager: "Official", Payload: "old", Provides: "compiler=1", Depends: "library"}
				library := testpkg.FixturePackage{Name: "library", Version: "1-9", Packager: "Official", Payload: "library"}
				base := testpkg.FixturePackage{Name: "base-devel", Version: "1-1", Packager: "Official", Payload: "base"}
				f.Sync(t, "core", base)
				f.Sync(t, "extra", old, library)
				f.Local(t, base)
				f.Local(t, old)
				f.Local(t, library)
				next := old
				next.Version = "2-1"
				next.Provides = "compiler=2"
				next.Payload = "new"
				nextLib := library
				nextLib.Version = "1-10"
				nextLib.Payload = "new library"
				f.Sync(t, "extra", next, nextLib)
				if mode == "forged" {
					old.Payload = "forged"
					f.Local(t, old)
				}
				if mode == "inconclusive" {
					f.ForgetEvidence("extra/builder", "1-1")
				}
				r := Resolver{Runner: f}
				binding, err := r.OfficialDependency(context.Background(), requirement)
				if mode == "inconclusive" {
					if err == nil {
						t.Fatal("unknown provider accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if binding.Satisfied || binding.Provider != "extra/builder" {
					t.Fatalf("%+v", binding)
				}
				want := archrepo.VerifiedOfficial
				if mode == "forged" {
					want = archrepo.InvalidOfficialContent
				}
				if binding.States["extra/builder"].Authenticity != want || binding.States["extra/library"].Authenticity != archrepo.VerifiedOfficial {
					t.Fatalf("%+v", binding)
				}
				metadata, err := aurmeta.Parse([]byte("pkgbase = example\npkgver = 1\npkgrel = 1\nmakedepends = " + requirement + "\npkgname = example\n"))
				if err != nil {
					t.Fatal(err)
				}
				_, _, packages, err := resolveAURBuild(context.Background(), r, plan.AURSource{Metadata: metadata}, "example", nil, map[string]bool{"builder": true, "library": true, "base-devel": true}, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				for _, pkg := range packages {
					if pkg.Name == "builder" && pkg.Repair != (mode == "forged") {
						t.Fatalf("%+v", pkg)
					}
					if pkg.Name == "library" && (pkg.Repair || !pkg.Update) {
						t.Fatalf("transitive drift %+v", pkg)
					}
				}
				if !strings.Contains(binding.States["extra/library"].Description(), "update available") {
					t.Fatal(binding)
				}
			})
		}
	}
}
