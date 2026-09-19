# Bounded, disposable lifecycle fixture. No package/configuration operations.
import json, os, pathlib, signal, sys, time
mode, directory = sys.argv[1:3]
root = pathlib.Path(directory)
signal.alarm(20)
signal.signal(signal.SIGTERM, signal.SIG_IGN)
signal.signal(signal.SIGINT, signal.SIG_IGN)
def record(role):
    value = dict(pid=os.getpid(), ppid=os.getppid(), uid=os.getuid(), role=role,
                 sid=os.getsid(0), pgid=os.getpgrp(),
                 cgroup=pathlib.Path('/proc/self/cgroup').read_text())
    if os.isatty(0):
        try: value['foreground'] = os.tcgetpgrp(0)
        except OSError: pass
    tmp = root / (str(os.getpid()) + '.tmp')
    tmp.write_text(json.dumps(value))
    tmp.rename(root / (str(os.getpid()) + '.json'))
def leaf(role, detach=False):
    if detach: os.setsid()
    record(role)
    with (root / (str(os.getpid()) + '.heartbeat')).open('wb', buffering=0) as f:
        while True:
            f.write(b'.')
            time.sleep(.01)
if mode == 'tty':
    record('input')
    print('NATIVE INPUT READY', flush=True)
    assert input() == 'native-input'
    mode = 'tree'
if mode == 'nested':
    current = pathlib.Path('/proc/self/cgroup').read_text().split('0::',1)[1].splitlines()[0]
    child = pathlib.Path('/sys/fs/cgroup'+current) / 'nested'
    child.mkdir()
    (child / 'cgroup.procs').write_text(str(os.getpid()))
record('direct')
for i in range(3):
    if os.fork() == 0:
        if mode == 'stdout': os.close(2)
        if mode == 'stderr': os.close(1)
        leaf('leaf-' + str(i), i == 1)
while len(list(root.glob('*.json'))) < 4: time.sleep(.002)
if mode == 'fork':
    if os.fork() == 0:
        record('forker')
        for i in range(32):
            p = os.fork()
            if p == 0:
                record('fork-' + str(i))
                time.sleep(.01)
                os._exit(0)
            os.waitpid(p, 0)
        os._exit(0)
(root / 'ready').write_text('1')
if mode in ('natural', 'stdout', 'stderr'): sys.exit(0)
leaf('direct')
