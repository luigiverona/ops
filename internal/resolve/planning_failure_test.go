package resolve

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/luigiverona/ops/internal/aurmeta"
	"github.com/luigiverona/ops/internal/config"
	"github.com/luigiverona/ops/internal/plan"
	"github.com/luigiverona/ops/internal/run"
)

// Count every resolver admission, including attempts a poisoned owner would reject.
type planningFailureResolver struct {
	fakeResolver
	at              string
	cause           error
	calls, failedAt int
}

func (r *planningFailureResolver) call(at string) error {
	r.calls++
	if at == r.at {
		r.failedAt = r.calls
		return r.cause
	}
	return nil
}
func (r *planningFailureResolver) Pacman(ctx context.Context, name string) (plan.Package, bool, error) {
	if err := r.call("pacman"); err != nil {
		return plan.Package{}, false, err
	}
	return plan.Package{Name: name, Repository: "extra"}, true, nil
}
func (r *planningFailureResolver) AUR(ctx context.Context, name string) (plan.Package, bool, error) {
	if err := r.call("aur"); err != nil {
		return plan.Package{}, false, err
	}
	return plan.Package{Name: name, PackageBase: "paru"}, true, nil
}
func (r *planningFailureResolver) Flatpak(context.Context, string) (bool, error) {
	return true, r.call("flatpak")
}
func (r *planningFailureResolver) AURSource(ctx context.Context, name string) (plan.AURSource, bool, error) {
	if err := r.call("source"); err != nil {
		return plan.AURSource{}, false, err
	}
	return r.fakeResolver.AURSource(ctx, name)
}
func (r *planningFailureResolver) OfficialDependency(ctx context.Context, name string) (plan.OfficialDependency, error) {
	if err := r.call("dependency"); err != nil {
		return plan.OfficialDependency{}, err
	}
	return r.fakeResolver.OfficialDependency(ctx, name)
}
func (r *planningFailureResolver) CompareVersions(context.Context, string, string) (int, error) {
	return 0, r.call("version")
}
func (r *planningFailureResolver) UserPGPKey(context.Context, string) (bool, error) {
	return true, r.call("key")
}

func TestApplicationResolutionStopsOnFatalCause(t *testing.T) {
	owner := &run.OwnershipError{Err: errors.New("original cleanup failure")}
	for _, cause := range []error{owner, context.Canceled, context.DeadlineExceeded} {
		for _, at := range []string{"pacman", "aur", "flatpak", "source", "dependency", "version", "key"} {
			t.Run(fmt.Sprintf("%s/%s", cause, at), func(t *testing.T) {
				source := config.AUR
				if at == "pacman" {
					source = config.Pacman
				}
				if at == "flatpak" {
					source = config.Flatpak
				}
				pinned := testParuSource()
				pinned.Metadata.ValidPGPKeys = []string{"first", "second"}
				// An internal versioned edge forces the metadata closure's comparator.
				if at == "version" {
					pinned.Metadata.Packages[0].Depends = []string{"paru>=1"}
				}
				r := &planningFailureResolver{fakeResolver: fakeResolver{source: &pinned}, at: at, cause: fmt.Errorf("query: %w", cause)}
				cfg := config.Config{Applications: []config.Application{{Source: source, Identifier: "paru"}, {Source: source, Identifier: "later"}, {Source: source, Identifier: "last"}}}
				facts, err := Applications(context.Background(), cfg, plan.State{}, r)
				if !errors.Is(err, cause) || facts != nil || r.failedAt == 0 || r.calls != r.failedAt {
					t.Fatalf("facts=%v err=%v calls=%d failure=%d", facts, err, r.calls, r.failedAt)
				}
				if cause == owner && !run.OwnershipFailed(err) {
					t.Fatal("ownership classification lost")
				}
				if at == "pacman" || at == "aur" || at == "flatpak" {
					r.calls, r.failedAt = 0, 0
					facts, err = ApplicationAvailability(context.Background(), cfg, plan.State{}, r)
					if !errors.Is(err, cause) || facts != nil || r.calls != 1 {
						t.Fatalf("availability: facts=%v err=%v calls=%d", facts, err, r.calls)
					}
				}
			})
		}
	}
}

func TestOrdinaryApplicationProblemsContinue(t *testing.T) {
	for _, at := range []string{"pacman", "aur", "flatpak", "source", "dependency", "key"} {
		t.Run(at, func(t *testing.T) {
			source := config.AUR
			if at == "pacman" {
				source = config.Pacman
			}
			if at == "flatpak" {
				source = config.Flatpak
			}
			pinned := testParuSource()
			pinned.Metadata.ValidPGPKeys = []string{"key"}
			pinned.Metadata.Packages = append(pinned.Metadata.Packages, aurmeta.Package{Name: "later"})
			cause := errors.New("ordinary unresolved source or dependency")
			r := &planningFailureResolver{fakeResolver: fakeResolver{source: &pinned}, at: at, cause: cause}
			cfg := config.Config{Applications: []config.Application{{Source: source, Identifier: "paru"}, {Source: source, Identifier: "later"}}}
			facts, err := Applications(context.Background(), cfg, plan.State{}, r)
			if err != nil || len(facts) != 2 {
				t.Fatalf("facts=%v err=%v", facts, err)
			}
			for _, fact := range facts {
				if !fact.State.Problem() || !errors.Is(fact.Err, cause) {
					t.Fatalf("ordinary problem changed: %+v", fact)
				}
			}
		})
	}
}
