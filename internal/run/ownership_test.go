package run

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeGroup struct {
	empty                                 bool
	readErr, killErr, removeErr, closeErr error
	killed, removed, closed               int
	observe                               func() (bool, error)
}

func (*fakeGroup) FD() int { return -1 }
func (g *fakeGroup) Empty() (bool, error) {
	if g.observe != nil {
		return g.observe()
	}
	return g.empty, g.readErr
}
func (g *fakeGroup) Kill() error {
	g.killed++
	if g.killErr == nil {
		g.empty = true
	}
	return g.killErr
}
func (g *fakeGroup) Remove() error {
	empty, err := g.Empty()
	if err != nil {
		return err
	}
	if !empty {
		return errors.New("populated")
	}
	g.removed++
	return g.removeErr
}
func (g *fakeGroup) Close() error { g.closed++; return g.closeErr }

type fakeScope struct {
	group         *fakeGroup
	err, checkErr error
	names         []string
}

func (s *fakeScope) Check() error { return s.checkErr }
func (s *fakeScope) New(name string) (commandGroup, error) {
	s.names = append(s.names, name)
	return s.group, s.err
}
func fakeOwner(s *fakeScope) *Owner {
	return &Owner{activate: func(context.Context) (ownedScope, error) { return s, nil }, probe: func(context.Context, commandGroup) error { return nil }}
}

func TestOwnershipActivationAndCopies(t *testing.T) {
	s := &fakeScope{group: &fakeGroup{empty: true}}
	o := fakeOwner(s)
	if !OwnershipFailed(o.Check()) {
		t.Fatal("inactive mutation admitted")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := o.Activate(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(s.names) != 2 || s.names[0] == s.names[1] {
		t.Fatalf("activation repeated or name reused: %v", s.names)
	}
	e := Exec{Owner: o}.WithIO(strings.NewReader(""), io.Discard, io.Discard)
	if e.Owner != o {
		t.Fatal("IO rebinding lost owner")
	}
	sentinel := errors.New("cleanup failed")
	o.fail(sentinel)
	copy := e
	if !errors.Is(copy.CheckMutation(), sentinel) {
		t.Fatal("copy lost poison")
	}
	_, err := copy.Run(context.Background(), Spec{Name: "/usr/bin/true"})
	if !OwnershipFailed(err) {
		t.Fatal("poisoned command admitted")
	}
}

func TestOwnershipActivationFaults(t *testing.T) {
	for _, fault := range []string{"unavailable", "mkdir", "open", "events", "kill", "remove", "clone", "scope"} {
		t.Run(fault, func(t *testing.T) {
			failure := errors.New(fault)
			s := &fakeScope{group: &fakeGroup{empty: true}}
			o := fakeOwner(s)
			switch fault {
			case "unavailable":
				o.activate = func(context.Context) (ownedScope, error) { return nil, failure }
			case "mkdir", "open":
				s.err = failure
			case "events":
				s.group.readErr = failure
			case "kill":
				s.group.killErr = failure
			case "remove":
				s.group.removeErr = failure
			case "clone":
				o.probe = func(context.Context, commandGroup) error { return failure }
			case "scope":
				s.checkErr = failure
			}
			if err := o.Activate(context.Background()); !OwnershipFailed(err) || !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if !errors.Is(o.Check(), failure) {
				t.Fatal("fault did not poison")
			}
			if !errors.Is(o.Activate(context.Background()), failure) {
				t.Fatal("poisoned activation retried")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := NewOwner()
	if err := o.Activate(ctx); !errors.Is(err, context.Canceled) || o.poison != nil {
		t.Fatal(err)
	}
}

func TestOwnershipStartFailureAndPrecancel(t *testing.T) {
	for _, cleanupFailure := range []bool{false, true} {
		g := &fakeGroup{empty: true}
		if cleanupFailure {
			g.removeErr = errors.New("remove")
		}
		o := fakeOwner(&fakeScope{group: g})
		o.scope = &fakeScope{group: g}
		cmd := exec.CommandContext(context.Background(), "/does-not-exist-ops-wave-e")
		pidfd := -1
		_, err := o.start(context.Background(), cmd, &pidfd)
		var startErr *os.PathError
		if !errors.As(err, &startErr) || OwnershipFailed(err) != cleanupFailure || g.removed != 1 || g.closed != 1 {
			t.Fatalf("%v: %v %#v", cleanupFailure, err, g)
		}
		if cmd.Process != nil || !cmd.SysProcAttr.UseCgroupFD {
			t.Fatal("failed start escaped placement")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := fakeOwner(&fakeScope{group: &fakeGroup{empty: true}})
	_, err := (Exec{Owner: o}).Run(ctx, Spec{Name: "/usr/bin/true"})
	if !errors.Is(err, context.Canceled) || o.next != 0 {
		t.Fatal(err)
	}
}

func TestOwnershipCleanupFaults(t *testing.T) {
	for _, fault := range []string{"none", "kill", "events", "timeout", "remove", "close", "empty-race", "changing", "grace-exit", "already-empty"} {
		t.Run(fault, func(t *testing.T) {
			g := &fakeGroup{}
			bad := errors.New(fault)
			switch fault {
			case "kill":
				g.killErr = bad
			case "events":
				g.readErr = bad
			case "timeout":
				g.observe = func() (bool, error) { return false, nil }
			case "remove":
				g.removeErr = bad
			case "close":
				g.closeErr = bad
			case "empty-race":
				g.killErr = bad
				g.observe = func() (bool, error) { return g.killed > 0, nil }
			case "already-empty":
				g.empty = true
			case "changing", "grace-exit":
				n := 0
				g.observe = func() (bool, error) { n++; return n > 3, nil }
			}
			o := NewOwner()
			err := o.clean(g, fault == "grace-exit")
			wantErr := fault != "none" && fault != "empty-race" && fault != "changing" && fault != "grace-exit" && fault != "already-empty"
			if (fault == "grace-exit" || fault == "already-empty") && g.killed != 0 {
				t.Fatal("unnecessary force cleanup")
			}
			if OwnershipFailed(err) != wantErr {
				t.Fatalf("err=%v", err)
			}
			if g.closed != 1 {
				t.Fatal("handles leaked")
			}
			if (fault == "kill" || fault == "events" || fault == "timeout") && g.removed != 0 {
				t.Fatal("unproven empty group removed")
			}
			if wantErr && !OwnershipFailed(o.Check()) {
				t.Fatal("cleanup failed without poison")
			}
		})
	}
}

func TestPopulationParsing(t *testing.T) {
	for _, s := range []string{"", "frozen 0\n", "populated 2\n", "populated 0\npopulated 1\n", "populated\n"} {
		if _, err := parsePopulation(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	for _, s := range []string{"populated 0\nfrozen 0\n", "frozen 0\npopulated 1\n"} {
		empty, err := parsePopulation(s)
		if err != nil || empty != strings.Contains(s, "populated 0") {
			t.Fatal(s, err)
		}
	}
}

func TestCommandDirectoryOpenFailureRestoresInvariant(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	s := &cgroupScope{root: root}
	if _, err := s.New("command-1"); err == nil {
		t.Fatal("missing cgroup interfaces accepted")
	}
	if _, err := root.Stat("command-1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("allocation not removed", err)
	}
}

func TestCgroupRemovalIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Mkdir(filepath.Join(dir, "command"), 0700); err != nil {
		t.Fatal(err)
	}
	events, err := os.CreateTemp(t.TempDir(), "events")
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	if _, err := events.WriteString("populated 0\n"); err != nil {
		t.Fatal(err)
	}
	g := &cgroupCommand{scope: &cgroupScope{root: root}, name: "command", events: events}
	if err := g.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := g.Remove(); err != nil {
		t.Fatal(err)
	}
	o := NewOwner()
	for range 2 {
		if err := o.finishGroup(g); err != nil {
			t.Fatal("repeated cleanup", err)
		}
	}
}

func TestCommandCancellationArbitration(t *testing.T) {
	failure := errors.New("independent failure")
	for _, naturalErr := range []error{nil, failure} {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		done <- naturalErr
		cancel()
		got, cause, natural, received, err := awaitCommand(ctx, done, nil, func() (bool, error) { panic("unexpected observation") })
		if got != naturalErr || cause != nil || !natural || !received || err != nil {
			t.Fatal("completed command lost to later cancellation")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	_, cause, natural, received, err := awaitCommand(ctx, done, nil, nil)
	done <- nil // exit zero AFTER cancellation won cannot change the latched cause.
	if !errors.Is(cause, context.Canceled) || natural || received || err != nil {
		t.Fatal("cancellation lost", cause)
	}
	deadlineCtx, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	_, cause, _, _, _ = awaitCommand(deadlineCtx, make(chan error), nil, nil)
	if !errors.Is(cause, context.DeadlineExceeded) {
		t.Fatal(cause)
	}
	pulse := make(chan time.Time, 1)
	pulse <- time.Now()
	_, _, natural, received, err = awaitCommand(context.Background(), make(chan error), pulse, func() (bool, error) { return true, nil })
	if !natural || received || err != nil {
		t.Fatal("direct exit with pending pipes lost")
	}
	pulse <- time.Now()
	_, _, _, _, err = awaitCommand(context.Background(), make(chan error), pulse, func() (bool, error) { return false, failure })
	if err != failure {
		t.Fatal("observation failure lost")
	}
}

func TestMutationAndPoisonShareAdmissionLock(t *testing.T) {
	o := fakeOwner(&fakeScope{group: &fakeGroup{empty: true}})
	if err := o.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	e := Exec{Owner: o}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() { finished <- e.Mutate(func() error { close(entered); <-release; return nil }) }()
	<-entered
	poisonStarted, poisoned := make(chan struct{}), make(chan error, 1)
	go func() { close(poisonStarted); poisoned <- o.fail(errors.New("cleanup incomplete")) }()
	<-poisonStarted
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if !OwnershipFailed(<-poisoned) {
		t.Fatal("missing poison")
	}
	called := false
	if err := e.Mutate(func() error { called = true; return nil }); !OwnershipFailed(err) || called {
		t.Fatal("persistent write admitted after poison", err)
	}
}

func TestTerminalRecoveryFailureClosesMutationGate(t *testing.T) {
	o := fakeOwner(&fakeScope{group: &fakeGroup{empty: true}})
	if err := o.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	original := context.Canceled
	err := (Exec{Owner: o}).recoverTerminal(func() error { return errors.New("terminal disappeared") }, original)
	if !errors.Is(err, original) || !OwnershipFailed(err) || !OwnershipFailed(o.Check()) {
		t.Fatal(err)
	}
}
