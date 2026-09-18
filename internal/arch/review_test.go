package arch

import (
	"context"
	"strings"
	"testing"
)

func TestReviewFullUpgradeIncludesConfiguredCustomRepositories(t *testing.T) {
	r := &configRunner{configuration: "[options]\nSigLevel = Required\n[custom]\nServer = https://custom/\n[core]\nServer = https://core/\n[extra]\nServer = https://extra/\n"}
	if err := (Manager{Runner: r}).FullUpgrade(context.Background(), "extra/git"); err != nil {
		t.Fatal(err)
	}
	mutations := 0
	for _, s := range r.calls {
		if s.Name != "sudo" {
			continue
		}
		mutations++
		if strings.Join(s.Args, " ") != "-n pacman -Syu -- extra/git" || !s.Interactive {
			t.Fatalf("D-R3: full upgrade excluded configured repositories: %v", s.Args)
		}
	}
	if mutations != 1 || r.staged != "" {
		t.Fatalf("unexpected staging or transaction count: %d %q", mutations, r.staged)
	}
}
