#!/usr/bin/env python3
"""Harmless I-02 baseline and process-group counterexamples; temporary files only.
Every helper self-expires after 15 seconds. The harness owns pidfds, adopts and
reaps orphans, and checks /proc disappearance. No sudo or unrelated signals.
"""
import ctypes, json, os, pathlib, signal, subprocess, sys, tempfile, time
ROOT = pathlib.Path(__file__).resolve().parent
PROBE = os.environ['OPS_WAVE_E_PROBE'] if 'OPS_WAVE_E_PROBE' in os.environ else str(ROOT / 'probe')

def await_condition(predicate, seconds=5):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        result = predicate()
        if result:
            return result
        time.sleep(.005)
    raise AssertionError('synchronization deadline expired')

def state(pid):
    try:
        return pathlib.Path(f'/proc/{pid}/stat').read_text().rsplit(')', 1)[1].split()[0]
    except FileNotFoundError:
        return None

def live(pid):
    return state(pid) not in (None, 'Z', 'X')

def helper(role, directory, mode, index='0'):
    directory = pathlib.Path(directory)
    # Each helper is exec'd separately; install its own hard deadline as well
    # as the cooperative loop bound, independently of the launching parent.
    signal.signal(signal.SIGALRM, signal.SIG_DFL)
    signal.alarm(15)
    lifetime_end = time.monotonic() + 15
    if role == 'leaf':
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        if mode == 'escape':
            os.setsid()
        (directory / f'leaf-{index}').write_text(str(os.getpid()))
        while time.monotonic() < lifetime_end:
            if (directory / 'request').exists():
                (directory / f'ack-{index}').write_text('mutation after cancellation')
            time.sleep(.01)
        os._exit(0)
    (directory / 'leader').write_text(str(os.getpid()))
    count = 3 if mode == 'multiple' else 1
    for i in range(count):
        subprocess.Popen([sys.executable, __file__, 'leaf', str(directory), mode, str(i)],
            stdin=subprocess.DEVNULL,
            stdout=None if mode in ('stdout', 'natural') else subprocess.DEVNULL,
            stderr=None if mode == 'stderr' else subprocess.DEVNULL)
    await_condition(lambda: all((directory / f'leaf-{i}').exists() for i in range(count)))
    (directory / 'ready').write_text('ready')
    if mode == 'natural':
        return
    time.sleep(max(0, lifetime_end - time.monotonic()))

if len(sys.argv) > 1:
    helper(*sys.argv[1:])
    sys.exit(0)

# Isolate orphan adoption to this disposable proof harness, not the Go tests.
assert ctypes.CDLL(None, use_errno=True).prctl(36, 1, 0, 0, 0) == 0
results = []
for boundary, mode in [('capture','survival'), ('capture','stdout'), ('capture','stderr'),
                       ('capture','natural'), ('capture','multiple'),
                       ('stream','survival'), ('group','escape')]:
    handles = {}
    bridge = None
    with tempfile.TemporaryDirectory(prefix='ops-wave-e-proof-') as temp:
        directory = pathlib.Path(temp)
        resultfile = directory / 'result.json'
        try:
            bridge = subprocess.Popen([PROBE, boundary, str(resultfile),
                sys.executable, __file__, 'leader', temp, mode], stdin=subprocess.PIPE,
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            await_condition(lambda: (directory / 'ready').exists())
            leader = int((directory / 'leader').read_text())
            leaves = [int(p.read_text()) for p in sorted(directory.glob('leaf-*'))]
            for pid in [leader] + leaves:
                if live(pid):
                    handles[pid] = os.pidfd_open(pid)
            if mode == 'natural':
                await_condition(lambda: not live(leader))
            bridge.stdin.write(b'cancel\n'); bridge.stdin.flush()
            await_condition(lambda: not live(leader))
            held = mode in ('stdout', 'stderr', 'natural')
            if held:
                # A negative observation has a deadline, after process-state synchronization.
                try:
                    bridge.wait(timeout=.3)
                    raise AssertionError('expected inherited descriptor to retain Run')
                except subprocess.TimeoutExpired:
                    pass
            else:
                bridge.wait(timeout=3)
                assert bridge.returncode == 0
            assert all(live(pid) for pid in leaves), 'a descendant unexpectedly died'
            for pid in leaves:
                signal.pidfd_send_signal(handles[pid], signal.SIGTERM)
                status = pathlib.Path(f'/proc/{pid}/status').read_text()
                ignored = int(next(line.split()[1] for line in status.splitlines() if line.startswith('SigIgn:')), 16)
                assert ignored & (1 << (signal.SIGTERM - 1)), 'TERM handler not synchronized'
            (directory / 'request').write_text('prove work is still possible')
            await_condition(lambda: all((directory / f'ack-{i}').exists() for i in range(len(leaves))))
            for pid in leaves:
                signal.pidfd_send_signal(handles[pid], signal.SIGKILL)
            bridge.wait(timeout=3)
            result = json.loads(resultfile.read_text())
            result.update(boundary=boundary, mode=mode, descendants=len(leaves),
                descendants_alive_after_cancel=True, post_cancel_mutation=True,
                pipe_retained_until_external_cleanup=held)
            results.append(result)
        finally:
            # Also recover helpers if a readiness assertion failed before registration.
            for record in [directory / 'leader', *directory.glob('leaf-*')]:
                if not record.exists():
                    continue
                try:
                    pid = int(record.read_text())
                    if pid in handles:
                        continue
                    fd = os.pidfd_open(pid)
                    try:
                        args = pathlib.Path(f'/proc/{pid}/cmdline').read_bytes().split(b'\0')
                        if os.fsencode(temp) in args and os.fsencode(__file__) in args:
                            handles[pid] = fd
                            fd = None
                    finally:
                        if fd is not None:
                            os.close(fd)
                except (ProcessLookupError, FileNotFoundError, ValueError):
                    pass
            # Signal only open pidfds belonging to these uniquely identified helpers.
            for fd in handles.values():
                try:
                    signal.pidfd_send_signal(fd, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                os.close(fd)
            if bridge is not None:
                if bridge.poll() is None:
                    bridge.kill()
                bridge.wait(timeout=3)
                bridge.stdin.close()
            # On setup failure, even an unregistered orphan self-expires in 15 s.
            end = time.monotonic() + 20
            while time.monotonic() < end:
                try:
                    pid, _ = os.waitpid(-1, os.WNOHANG)
                    if pid == 0:
                        time.sleep(.005)
                except ChildProcessError:
                    break
            else:
                raise AssertionError("helper reaping deadline expired")
            assert all(state(pid) is None for pid in handles), 'test helper not reaped'
    print(json.dumps(results[-1]), flush=True)
if 'OPS_WAVE_E_RESULTS' in os.environ:
    pathlib.Path(os.environ['OPS_WAVE_E_RESULTS']).write_text(json.dumps(results, indent=2) + '\n')
print('All helper processes terminated and reaped; all temporary mutation files removed.')
