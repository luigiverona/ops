package run

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/godbus/dbus/v5"
)

const systemdDestination = "org.freedesktop.systemd1"
const systemdManager = systemdDestination + ".Manager"

type unitProperty struct {
	Name  string
	Value dbus.Variant
}
type auxiliaryUnit struct {
	Name       string
	Properties []unitProperty
}

// Register the CURRENT process; no re-exec, approval replay, CLI helper, system
// manager, persistent unit, lingering change, or environment-based bus autolaunch.
func activateScope(ctx context.Context) (ownedScope, error) {
	socket := fmt.Sprintf("/run/user/%d/bus", os.Geteuid())
	transport, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("connect to systemd user manager: %w", err)
	}
	defer transport.Close()
	deadline, _ := ctx.Deadline()
	if err := transport.SetDeadline(deadline); err != nil {
		return nil, err
	}
	conn, err := dbus.NewConn(transport, dbus.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.Auth([]dbus.Auth{dbus.AuthExternal(strconv.Itoa(os.Geteuid()))}); err != nil {
		return nil, err
	}
	if err := conn.Hello(); err != nil {
		return nil, err
	}
	manager := conn.Object(systemdDestination, "/org/freedesktop/systemd1")
	signals := make(chan *dbus.Signal, 32)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	if err := conn.AddMatchSignalContext(ctx, dbus.WithMatchObjectPath("/org/freedesktop/systemd1"), dbus.WithMatchInterface(systemdManager), dbus.WithMatchMember("JobRemoved")); err != nil {
		return nil, err
	}
	if err := manager.CallWithContext(ctx, systemdManager+".Subscribe", 0).Err; err != nil {
		return nil, err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	unit := fmt.Sprintf("ops-%d-%x.scope", os.Getpid(), nonce)
	props := []unitProperty{
		{"Description", dbus.MakeVariant("ops subprocess ownership")},
		{"PIDs", dbus.MakeVariant([]uint32{uint32(os.Getpid())})},
		{"Delegate", dbus.MakeVariant(true)},
		{"CollectMode", dbus.MakeVariant("inactive-or-failed")},
	}
	var job dbus.ObjectPath
	if err := manager.CallWithContext(ctx, systemdManager+".StartTransientUnit", 0, unit, "fail", props, []auxiliaryUnit{}).Store(&job); err != nil {
		return nil, fmt.Errorf("register delegated scope: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case sig, ok := <-signals:
			if !ok {
				return nil, errors.New("user manager disconnected during scope activation")
			}
			if sig.Name != systemdManager+".JobRemoved" || len(sig.Body) != 4 || sig.Body[1] != job {
				continue
			}
			if sig.Body[2] != unit || sig.Body[3] != "done" {
				return nil, errors.New("delegated scope activation job failed")
			}
			var object dbus.ObjectPath
			if err := manager.CallWithContext(ctx, systemdManager+".GetUnit", 0, unit).Store(&object); err != nil {
				return nil, err
			}
			get := func(iface, name string, target any) error {
				var v dbus.Variant
				err := conn.Object(systemdDestination, object).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, systemdDestination+"."+iface, name).Store(&v)
				if err != nil {
					return err
				}
				return v.Store(target)
			}
			var delegated, transient bool
			var path string
			if err := get("Scope", "Delegate", &delegated); err != nil {
				return nil, err
			}
			if err := get("Unit", "Transient", &transient); err != nil {
				return nil, err
			}
			if err := get("Scope", "ControlGroup", &path); err != nil {
				return nil, err
			}
			if !delegated || !transient || !filepath.IsAbs(path) || path == "/" || filepath.Clean(path) != path {
				return nil, errors.New("user manager did not provide a transient delegated scope")
			}
			root, err := os.OpenRoot("/sys/fs/cgroup" + path)
			if err != nil {
				return nil, err
			}
			scope := &cgroupScope{root: root, path: path}
			if err := scope.Check(); err != nil {
				root.Close()
				return nil, err
			}
			return scope, nil
		}
	}
}

func selfCgroup() (string, error) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "0::") {
			return strings.TrimPrefix(line, "0::"), nil
		}
	}
	return "", errors.New("unified cgroup v2 membership unavailable")
}

type cgroupScope struct {
	root *os.Root
	path string
}

func (s *cgroupScope) Check() error {
	current, err := selfCgroup()
	if err != nil {
		return err
	}
	if current != s.path {
		return errors.New("ops process left its delegated scope")
	}
	f, err := s.root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	var st syscall.Statfs_t
	if err := syscall.Fstatfs(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Type != 0x63677270 {
		return errors.New("ownership filesystem is not cgroup v2")
	}
	return nil
}
func (s *cgroupScope) New(name string) (commandGroup, error) {
	if err := s.root.Mkdir(name, 0700); err != nil {
		return nil, err
	}
	g := &cgroupCommand{scope: s, name: name}
	fail := func(err error) (commandGroup, error) {
		return nil, errors.Join(err, g.Close(), s.root.Remove(name))
	}
	var err error
	g.dir, err = s.root.OpenFile(name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fail(err)
	}
	// All interfaces are opened relative to the retained command directory, so
	// cleanup never follows a replacement path or an unrelated command's files.
	open := func(name string, flags int) (*os.File, error) {
		fd, err := syscall.Openat(int(g.dir.Fd()), name, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), name), nil
	}
	g.events, err = open("cgroup.events", os.O_RDONLY)
	if err != nil {
		return fail(err)
	}
	g.kill, err = open("cgroup.kill", os.O_WRONLY)
	if err != nil {
		return fail(err)
	}
	return g, nil
}

type cgroupCommand struct {
	scope             *cgroupScope
	name              string
	dir, events, kill *os.File
	removed           bool
}

func (g *cgroupCommand) FD() int { return int(g.dir.Fd()) }
func (g *cgroupCommand) Empty() (bool, error) {
	b := make([]byte, 4096)
	n, err := g.events.ReadAt(b, 0)
	if err != nil && err != io.EOF {
		return false, err
	}
	return parsePopulation(string(b[:n]))
}
func parsePopulation(value string) (bool, error) {
	found, empty := false, false
	for _, line := range strings.Split(value, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "populated" {
			continue
		}
		if found || len(f) != 2 || (f[1] != "0" && f[1] != "1") {
			return false, errors.New("malformed cgroup.events population")
		}
		found, empty = true, f[1] == "0"
	}
	if !found {
		return false, errors.New("cgroup.events population missing")
	}
	return empty, nil
}
func (g *cgroupCommand) Kill() error { _, err := g.kill.WriteString("1"); return err }
func (g *cgroupCommand) Remove() error {
	if g.removed {
		return nil
	}
	empty, err := g.Empty()
	if err != nil {
		return err
	}
	if !empty {
		return errors.New("refusing removal of populated command cgroup")
	}
	if err := g.scope.root.Remove(g.name); err != nil {
		if !errors.Is(err, syscall.EBUSY) && !errors.Is(err, syscall.ENOTEMPTY) {
			return fmt.Errorf("remove empty command cgroup %s: %w", g.name, err)
		}
		// Ordinary commands cost one rmdir. Walk ONLY an empty command's nested
		// cgroups when necessary, never /proc or the surrounding hierarchy.
		root, openErr := g.scope.root.OpenRoot(g.name)
		if openErr != nil {
			return openErr
		}
		budget := 128
		nestedErr := removeEmptyCgroups(root, &budget)
		nestedErr = compose(nestedErr, root.Close())
		if nestedErr != nil {
			return nestedErr
		}
		if err := g.scope.root.Remove(g.name); err != nil {
			return fmt.Errorf("remove empty command cgroup %s: %w", g.name, err)
		}
	}
	g.removed = true
	return nil
}
func (g *cgroupCommand) Close() error {
	var errs []error
	for _, f := range []**os.File{&g.kill, &g.events, &g.dir} {
		if *f != nil {
			errs = append(errs, (*f).Close())
			*f = nil
		}
	}
	return errors.Join(errs...)
}

func removeEmptyCgroups(root *os.Root, budget *int) error {
	if *budget <= 0 {
		return errors.New("command nested-cgroup cleanup limit exceeded")
	}
	*budget--
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	err = compose(err, f.Close())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child, err := root.OpenRoot(entry.Name())
		if err != nil {
			return err
		}
		err = removeEmptyCgroups(child, budget)
		err = compose(err, child.Close())
		if err != nil {
			return err
		}
		if err := root.Remove(entry.Name()); err != nil {
			return err
		}
	}
	return nil
}
