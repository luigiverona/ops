package archrepo

import (
	"strings"
	"testing"
)

func TestOfficialConfigPreservesPolicyAndExcludesCustomRepositories(t *testing.T) {
	input := "[options]\nArchitecture = x86_64\nSigLevel = PackageRequired\nIgnorePkg = held\n[custom]\nServer = https://custom.example/\n[core]\nServer = https://mirror/core/\n[extra]\nSigLevel = PackageRequired\nServer = https://mirror/extra/\n[multilib]\nServer = https://mirror/multilib/\n"
	got, custom, err := OfficialConfig(input)
	if err != nil || !custom || strings.Contains(got, "custom") || !strings.Contains(got, "IgnorePkg = held") || !strings.Contains(got, "[multilib]\nServer = https://geo.mirror.pkgbuild.com/multilib/os/x86_64") || strings.Count(got, "SigLevel = PackageRequired") != 1 || strings.Contains(got, "https://mirror/") {
		t.Fatalf("%q custom=%v err=%v", got, custom, err)
	}
	unchanged, custom, err := OfficialConfig(got)
	if err != nil || !custom || unchanged != got {
		t.Fatalf("official config changed: %q %v %v", unchanged, custom, err)
	}
}

func TestOfficialConfigRejectsAmbiguousOrUnexpandedInput(t *testing.T) {
	for _, input := range []string{"", "[options]\n[core]\n", "[core]\n[extra]\n", "[options]\n[core]\n[extra]\n[core]\n", "[options]\n[core]\nInclude = /etc/mirror\n[extra]\n", "[options]\n[core]\n[extra]\n\n", "[options]\n[core]\n[extra]\n[bad][extra]\n"} {
		if _, _, err := OfficialConfig(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestSpoofedOfficialSectionsCannotSelectContent(t *testing.T) {
	for _, repo := range Repositories() {
		t.Run(repo, func(t *testing.T) {
			input := "[options]\nArchitecture = x86_64\nSigLevel = Required TrustedOnly\n[core]\nServer = https://custom.example/core\n[extra]\nServer = https://custom.example/extra\n[multilib]\nServer = https://custom.example/multilib\n"
			input = strings.Replace(input, "["+repo+"]\n", "["+repo+"]\nSigLevel = Never\n", 1)
			got, staged, err := OfficialConfig(input)
			if err != nil || !staged || strings.Contains(got, "custom.example") || strings.Contains(got, "Never") || !strings.Contains(got, "Server = https://geo.mirror.pkgbuild.com/"+repo+"/os/x86_64") {
				t.Fatalf("spoofed source retained: %s %v", got, err)
			}
		})
	}
}

func TestOfficialGlobalPolicyBoundary(t *testing.T) {
	for _, policy := range []string{"RootDir = /other", "DBPath = /other", "CacheDir = /other", "Architecture = aarch64", "NoUpgrade = usr/bin/git", "NoExtract = usr/bin/git", "XferCommand = custom", "DisableSandbox", "SigLevel = Optional", "SigLevel = PackageNever"} {
		if _, _, err := OfficialConfig("[options]\n" + policy + "\n[core]\n[extra]\n"); err == nil {
			t.Fatal("unsupported policy accepted", policy)
		}
	}
}
