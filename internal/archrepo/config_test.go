package archrepo

import (
	"strings"
	"testing"
)

func TestOfficialConfigPreservesPolicyAndExcludesCustomRepositories(t *testing.T) {
	input := "[options]\nArchitecture = x86_64\nSigLevel = PackageRequired\nIgnorePkg = held\n[custom]\nServer = https://custom.example/\n[core]\nServer = https://mirror/core/\n[extra]\nSigLevel = PackageRequired\nServer = https://mirror/extra/\n[multilib]\nServer = https://mirror/multilib/\n"
	got, custom, err := OfficialConfig(input)
	if err != nil || !custom || strings.Contains(got, "custom") || !strings.Contains(got, "IgnorePkg = held") || !strings.Contains(got, "[multilib]\nServer = https://mirror/multilib/") || strings.Count(got, "SigLevel = PackageRequired") != 2 {
		t.Fatalf("%q custom=%v err=%v", got, custom, err)
	}
	unchanged, custom, err := OfficialConfig(got)
	if err != nil || custom || unchanged != got {
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
