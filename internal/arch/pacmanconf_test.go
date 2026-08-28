package arch

import (
	"bytes"
	"testing"
)

func TestEnableCommentedMultilibPreservesContent(t *testing.T) {
	input := []byte("# header\n[core]\nInclude = /mirror\n\n#[multilib]\n#Include = /etc/pacman.d/mirrorlist\n\n[custom]\nServer = x\n")
	got, err := EnableMultilib(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("[multilib]\nInclude = /etc/pacman.d/mirrorlist")) || !bytes.Contains(got, []byte("[custom]\nServer = x")) {
		t.Fatalf("unexpected output:\n%s", got)
	}
	enabled, err := MultilibEnabled(got)
	if err != nil || !enabled {
		t.Fatalf("enabled = %v, %v", enabled, err)
	}
}

func TestEnableMultilibIdempotent(t *testing.T) {
	input := []byte("[multilib]\nInclude = /etc/pacman.d/mirrorlist\n")
	got, err := EnableMultilib(input)
	if err != nil || !bytes.Equal(got, input) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestDuplicateMultilibRejected(t *testing.T) {
	input := []byte("#[multilib]\n#Include = x\n[multilib]\nInclude = x\n")
	if _, err := EnableMultilib(input); err == nil {
		t.Fatal("expected duplicate error")
	}
}
