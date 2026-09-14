package arch

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/luigiverona/ops/internal/run"
	"github.com/luigiverona/ops/internal/testpkg"
)

type configRunner struct {
	configuration                     string
	staged                            string
	calls                             []run.Spec
	failCopy, unsafeStage, failPacman bool
}

func (r *configRunner) Run(_ context.Context, s run.Spec) (run.Result, error) {
	r.calls = append(r.calls, s)
	if s.Name == "pacman-conf" {
		return run.Result{Stdout: r.configuration}, nil
	}
	if s.Name != "sudo" {
		return run.Result{}, errors.New("unexpected query")
	}
	switch s.Args[1] {
	case "stat":
		if s.Args[len(s.Args)-1] == artifactStageParent {
			return run.Result{Stdout: "0\t43ff\t2\n"}, nil
		}
		if r.unsafeStage {
			return run.Result{Stdout: "1000\t41ff\t2\n"}, nil
		}
		if strings.HasSuffix(s.Args[len(s.Args)-1], "/pacman.conf") {
			return run.Result{Stdout: "0\t8180\t1\n"}, nil
		}
		return run.Result{Stdout: "0\t41c0\t2\n"}, nil
	case "mktemp":
		return run.Result{Stdout: "/var/tmp/ops-paru-CONFIG123456\n"}, nil
	case "install":
		if s.Stdin == nil || s.Args[len(s.Args)-2] != "/dev/stdin" {
			return run.Result{}, errors.New("configuration must be streamed")
		}
		data, err := io.ReadAll(s.Stdin)
		r.staged = string(data)
		if r.failCopy {
			err = errors.New("copy failed")
		}
		return run.Result{}, err
	case "pacman":
		if r.failPacman {
			return run.Result{}, errors.New("transaction failed")
		}
	case "rm", "rmdir":
	default:
		return run.Result{}, errors.New("unexpected mutation")
	}
	return run.Result{}, nil
}

func TestEveryPackageMutationExcludesCustomRepositoriesInProtectedConfig(t *testing.T) {
	for _, operation := range []string{"-S", "-U"} {
		for _, failure := range []string{"", "copy", "stage", "pacman"} {
			r := &configRunner{configuration: "[options]\nSigLevel = Required\n[custom]\nServer = https://custom/\n[core]\nServer = https://core/\n[extra]\nServer = https://extra/\n[multilib]\nServer = https://multilib/\n", failCopy: failure == "copy", unsafeStage: failure == "stage", failPacman: failure == "pacman"}
			spec := run.Spec{Name: "sudo", Args: []string{"-n", "pacman", operation, "--", "extra/git"}, Interactive: operation == "-Syu", Interaction: "pacman transaction decisions"}
			err := (Manager{Runner: r}).runOfficial(context.Background(), spec)
			if (err != nil) != (failure != "") {
				t.Fatalf("%s %s: %v", operation, failure, err)
			}
			mutated, cleaned := false, false
			for _, call := range r.calls {
				if call.Name != "sudo" {
					continue
				}
				if call.Args[1] == "rmdir" {
					cleaned = true
				}
				if call.Args[1] != "pacman" {
					continue
				}
				mutated = true
				if call.Interactive != spec.Interactive || call.Stdin != nil || !strings.Contains(strings.Join(call.Args, " "), "--config /var/tmp/ops-paru-CONFIG123456/pacman.conf -- extra/git") || strings.Contains(r.staged, "custom") || !strings.Contains(r.staged, "[multilib]") || !strings.Contains(r.staged, "SigLevel = Required") {
					t.Fatalf("unsafe command/config: %+v %q", call, r.staged)
				}
			}
			if !cleaned || mutated != (failure == "" || failure == "pacman") {
				t.Fatalf("%s: mutated=%v cleaned=%v", failure, mutated, cleaned)
			}
		}
	}
}

func TestInvalidRepositoryConfigurationStopsBeforeMutation(t *testing.T) {
	r := &configRunner{configuration: "[options]\n[core]\n[extra]\n[extra]\n"}
	if (Manager{Runner: r}).FullUpgrade(context.Background()) == nil || len(r.calls) != 1 {
		t.Fatalf("invalid configuration reached sudo: %+v", r.calls)
	}
}

type provenanceRunner struct {
	output string
	calls  []run.Spec
}

func (r *provenanceRunner) Run(_ context.Context, s run.Spec) (run.Result, error) {
	r.calls = append(r.calls, s)
	if s.Name == "pacman" && s.Args[0] == "-Sp" {
		return run.Result{Stdout: r.output}, nil
	}
	if result, ok := testpkg.Query(s); ok {
		return result, nil
	}
	return run.Result{}, nil
}
func TestInstallQualifiesTargetsWithoutPromotingImplicitDependencies(t *testing.T) {
	r := &provenanceRunner{output: "extra/firefox\ncore/glibc\n"}
	if err := (Manager{Runner: r}).Install(context.Background(), []string{"extra/firefox"}, false); err != nil {
		t.Fatal(err)
	}
	for _, s := range r.calls {
		if s.Name == "sudo" {
			if strings.Join(s.Args, " ") != "-n pacman -S --noconfirm -- extra/firefox" {
				t.Fatalf("unqualified or needed mutation: %v", s.Args)
			}
		}
	}
}
func TestInstallRejectsCustomAndUnplannedMembersBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		output string
		deps   bool
	}{
		{"custom/git\n", false}, {"extra/git\ncustom/dependency\n", false}, {"extra/git\nextra/changed\n", true}, {"extra/other\n", false},
	} {
		r := &provenanceRunner{output: tc.output}
		if err := (Manager{Runner: r}).Install(context.Background(), []string{"extra/git"}, tc.deps); err == nil {
			t.Fatalf("accepted %s", tc.output)
		}
		for _, call := range r.calls {
			if call.Name == "sudo" {
				t.Fatalf("mutation: %v", call)
			}
		}
	}
}
func TestFullUpgradeProtectsManagedOfficialTargets(t *testing.T) {
	r := &provenanceRunner{}
	if err := (Manager{Runner: r}).FullUpgrade(context.Background(), "extra/git", "core/openssh"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.calls[1].Args, " "); got != "-n pacman -Syu -- extra/git core/openssh" {
		t.Fatal(got)
	}
	r.calls = nil
	if (Manager{Runner: r}).FullUpgrade(context.Background(), "custom/git") == nil || len(r.calls) != 0 {
		t.Fatal("invalid upgrade identity reached pacman")
	}
}
