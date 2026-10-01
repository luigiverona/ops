"""Bounded cleanup for an exact, independently recorded disposable owner scope."""
import ctypes, os, pathlib, re, time

def subreaper():
    if ctypes.CDLL(None, use_errno=True).prctl(36, 1, 0, 0, 0):
        raise OSError(ctypes.get_errno(), 'subreaper')

def cleanup_scope(record, owner_pid, known=None):
    record = pathlib.Path(record)
    if not record.exists():
        return
    scope = pathlib.Path(record.read_text().strip())
    # The helper writes its verified production scope before starting workloads.
    # Never infer authority from a broad ops-* scan or a descendant's PID alone.
    assert str(scope).startswith('/sys/fs/cgroup/user.slice/user-%d.slice/' % os.getuid())
    assert re.fullmatch(r'ops-%d-[0-9a-f]{32}\.scope' % owner_pid, scope.name)
    deadline = time.monotonic() + 8
    known = set(known or ())
    while scope.exists():
        if time.monotonic() >= deadline:
            raise RuntimeError('fixture scope cleanup timeout: ' + str(scope))
        try:
            for procs in scope.rglob('cgroup.procs'):
                known.update(int(p) for p in procs.read_text().split())
            (scope / 'cgroup.kill').write_text('1')
            events = dict(line.split() for line in (scope / 'cgroup.events').read_text().splitlines())
            if events['populated'] == '0':
                for child in sorted((p for p in scope.rglob('*') if p.is_dir()), key=lambda p:len(p.parts), reverse=True):
                    try: child.rmdir()
                    except FileNotFoundError: pass
        except FileNotFoundError:
            pass # systemd collects the empty scope concurrently
        for pid in known:
            try: os.waitpid(pid, os.WNOHANG)
            except ChildProcessError: pass
        time.sleep(.01)
    # Disappearance proves no population remains in the fixture's scope.
    while known:
        for pid in list(known):
            try: os.waitpid(pid, os.WNOHANG)
            except ChildProcessError: pass
            if not pathlib.Path('/proc/%d' % pid).exists(): known.remove(pid)
        if time.monotonic() >= deadline: raise RuntimeError('fixture PIDs remain: '+repr(known))
        if known: time.sleep(.01)
