package aurmeta

import (
	"fmt"
	"strings"
	"testing"
)

func testCompare(left, right string) (int, error) {
	ranks := map[string]int{"1-1": 1, "1-2": 2, "2": 3, "2-1": 4, "1:1.0-1": 5}
	l, lok := ranks[left]
	r, rok := ranks[right]
	if !lok || !rok {
		return 0, fmt.Errorf("unknown version %q or %q", left, right)
	}
	if l < r {
		return -1, nil
	}
	if l > r {
		return 1, nil
	}
	return 0, nil
}

func TestParseIncludesApplicableDependenciesAndNormalizes(t *testing.T) {
	metadata, err := Parse([]byte(`pkgbase = example
	pkgver = 2.0
	pkgrel = 3
	depends = runtime>=1
	depends_x86_64 = arch-runtime
	makedepends = compiler
	makedepends_x86_64 = linker
	checkdepends = test-tool
	checkdepends_x86_64 = arch-test

pkgname = example
`))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Version != "2.0-3" || strings.Join(metadata.Depends, ",") != "arch-runtime,runtime>=1" ||
		strings.Join(metadata.MakeDepends, ",") != "compiler,linker" ||
		strings.Join(metadata.CheckDepends, ",") != "arch-test,test-tool" {
		t.Fatalf("metadata=%#v", metadata)
	}
	withoutChecks, err := metadata.BuildRequirements("example", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	withChecks, err := metadata.BuildRequirements("example", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(withChecks) != len(withoutChecks)+2 {
		t.Fatalf("without checks=%v with checks=%v", withoutChecks, withChecks)
	}
}

func TestParseRetainsAllX8664PlanningFieldsAndSplitPackageValues(t *testing.T) {
	metadata, err := Parse([]byte(`pkgbase = suite
	epoch = 2
	pkgver = 1.4
	pkgrel = 3
	depends = global-runtime>=1
	depends_x86_64 = global-arch-runtime
	makedepends = global-build
	makedepends_x86_64 = global-arch-build
	checkdepends = global-check
	checkdepends_x86_64 = global-arch-check
	provides = global-virtual=2
	provides_x86_64 = global-arch-virtual=3

pkgname = suite-cli
	depends = cli-runtime
	depends_x86_64 = cli-arch-runtime
	provides = suite-cli-virtual
	provides_x86_64 = suite-cli-arch-virtual=1

pkgname = suite-lib
	depends = lib-runtime
	provides = suite-library=1.4
`))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Version != "2:1.4-3" ||
		strings.Join(metadata.Depends, ",") != "global-arch-runtime,global-runtime>=1" ||
		strings.Join(metadata.MakeDepends, ",") != "global-arch-build,global-build" ||
		strings.Join(metadata.CheckDepends, ",") != "global-arch-check,global-check" ||
		strings.Join(metadata.Provides, ",") != "global-arch-virtual=3,global-virtual=2" {
		t.Fatalf("base metadata=%#v", metadata)
	}
	if strings.Join(metadata.Packages[0].Depends, ",") != "cli-arch-runtime,cli-runtime" ||
		strings.Join(metadata.Packages[0].Provides, ",") != "suite-cli-arch-virtual=1,suite-cli-virtual" ||
		strings.Join(metadata.Packages[1].Provides, ",") != "suite-library=1.4" {
		t.Fatalf("split metadata=%#v", metadata.Packages)
	}
}

func TestBuildRequirementsIncludesSelectedPackageDependencies(t *testing.T) {
	metadata, err := Parse([]byte(`pkgbase = suite
	pkgver = 1
	pkgrel = 1
	depends = shared-runtime
	makedepends = compiler

pkgname = suite-cli
	depends = suite-libs=1-1
	depends = cli-runtime

pkgname = suite-libs
	depends = library-runtime

pkgname = suite-docs
	depends = docs-runtime
`))
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := metadata.BuildRequirements("suite-cli", true, testCompare)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, requirement := range requirements {
		got = append(got, requirement.Expression+":"+requirement.Purpose)
	}
	want := "cli-runtime:runtime,compiler:build,library-runtime:runtime,shared-runtime:runtime"
	if strings.Join(got, ",") != want {
		t.Fatalf("requirements=%v, want=%s", got, want)
	}
}

func TestOutputClosureSelectsOnlyRequiredSiblingPackages(t *testing.T) {
	metadata, err := Parse([]byte(`pkgbase = suite
	pkgver = 1
	pkgrel = 1

pkgname = suite-cli
	depends = suite-libs=1-1

pkgname = suite-libs

pkgname = suite-docs
`))
	if err != nil {
		t.Fatal(err)
	}
	closure, err := metadata.OutputClosure("suite-cli", testCompare)
	if err != nil || strings.Join(closure, ",") != "suite-cli,suite-libs" {
		t.Fatalf("closure=%v err=%v", closure, err)
	}
}

func TestOutputClosureFollowsExactSiblingProvides(t *testing.T) {
	metadata, err := Parse([]byte(`pkgbase = suite
	pkgver = 1
	pkgrel = 1

pkgname = suite-cli
	depends = suite-library

pkgname = suite-libs
	provides = suite-library=1

pkgname = suite-docs
`))
	if err != nil {
		t.Fatal(err)
	}
	closure, err := metadata.OutputClosure("suite-cli", nil)
	if err != nil || strings.Join(closure, ",") != "suite-cli,suite-libs" {
		t.Fatalf("closure=%v err=%v", closure, err)
	}
	requirements, err := metadata.BuildRequirements("suite-cli", true, nil)
	if err != nil || len(requirements) != 0 {
		t.Fatalf("sibling provide escaped into official requirements: %v, %v", requirements, err)
	}
}

func TestPlanningEqualRejectsDependencyAndOutputDrift(t *testing.T) {
	base, err := Parse([]byte("pkgbase = x\n\tpkgver = 1\n\tpkgrel = 1\n\tmakedepends = cargo\n\npkgname = x\n"))
	if err != nil {
		t.Fatal(err)
	}
	drift, err := Parse([]byte("pkgbase = x\n\tpkgver = 1\n\tpkgrel = 1\n\tmakedepends = go\n\npkgname = x-extra\n"))
	if err != nil {
		t.Fatal(err)
	}
	if PlanningEqual(base, drift) {
		t.Fatal("planning-relevant drift was accepted")
	}
}

func TestVersionAwareSiblingClosureAndProvides(t *testing.T) {
	for _, test := range []struct {
		name, version, dependency, provide string
		wantSibling                        bool
	}{
		{"equal", "1-1", "helper=1-1", "", true},
		{"greater-or-equal", "1-2", "helper>=1-1", "", true},
		{"less-or-equal", "1-1", "helper<=1-2", "", true},
		{"greater", "1-2", "helper>1-1", "", true},
		{"less", "1-1", "helper<1-2", "", true},
		{"insufficient exact name", "1-1", "helper>=1-2", "", false},
		{"versioned provide sufficient", "1-1", "virtual>=2", "virtual=2", true},
		{"versioned provide insufficient", "1-1", "virtual>2", "virtual=2", false},
		{"unversioned provide unversioned requirement", "1-1", "virtual", "virtual", true},
		{"unversioned provide rejects versioned requirement", "1-1", "virtual>=1", "virtual", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			parts := strings.Split(test.version, "-")
			data := "pkgbase = suite\n\tpkgver = " + parts[0] + "\n\tpkgrel = " + parts[1] + "\n\npkgname = suite\n\tdepends = " + test.dependency + "\n\npkgname = helper\n"
			if test.provide != "" {
				data += "\tprovides = " + test.provide + "\n"
			}
			metadata, err := Parse([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			closure, err := metadata.OutputClosure("suite", testCompare)
			if err != nil {
				t.Fatal(err)
			}
			hasSibling := strings.Contains(strings.Join(closure, ","), "helper")
			if hasSibling != test.wantSibling {
				t.Fatalf("closure=%v want sibling=%v", closure, test.wantSibling)
			}
			requirements, err := metadata.BuildRequirements("suite", false, testCompare)
			if err != nil {
				t.Fatal(err)
			}
			if (len(requirements) == 0) != test.wantSibling {
				t.Fatalf("requirements=%v want sibling=%v", requirements, test.wantSibling)
			}
		})
	}
}

func TestVersionedSiblingExpressionsFailClosed(t *testing.T) {
	for _, expression := range []string{"helper>>1", "helper>=", "helper=1=2"} {
		data := "pkgbase = suite\n\tpkgver = 1\n\tpkgrel = 1\n\npkgname = suite\n\tdepends = " + expression + "\n"
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatalf("accepted malformed dependency %q", expression)
		}
	}
	for _, expression := range []string{"helper>=1", "helper=", "helper=1=2"} {
		data := "pkgbase = suite\n\tpkgver = 1\n\tpkgrel = 1\n\nprovides = " + expression + "\n\npkgname = suite\n"
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatalf("accepted malformed provide %q", expression)
		}
	}
}

func TestAmbiguousSiblingProvidersFailClosed(t *testing.T) {
	metadata, err := Parse([]byte("pkgbase = suite\n\tpkgver = 1\n\tpkgrel = 1\n\npkgname = suite\n\tdepends = virtual\n\npkgname = first\n\tprovides = virtual\n\npkgname = second\n\tprovides = virtual\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.OutputClosure("suite", testCompare); err == nil {
		t.Fatal("ambiguous sibling providers were selected by order")
	}
}
