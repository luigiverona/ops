#!/usr/bin/python3
"""Disposable staged executable; no production code imports this fixture."""
import json
import os
import pathlib
import signal
import time

root = pathlib.Path(os.environ['OPS_RELEASE_FIXTURE_DIR'])
bound = float(os.environ.get('OPS_RELEASE_FIXTURE_SECONDS', '20'))
assert 0 < bound <= 20


def arm_deadline():
    signal.signal(signal.SIGALRM, signal.SIG_DFL)
    signal.setitimer(signal.ITIMER_REAL, bound)


arm_deadline()
os.register_at_fork(after_in_child=arm_deadline)
signal.signal(signal.SIGTERM, signal.SIG_IGN)
signal.signal(signal.SIGINT, signal.SIG_IGN)
role = 'parent'
if os.fork() == 0:
    os.setsid()
    role = 'child'
    if os.fork() == 0:
        role = 'grandchild'
# No exec after fork: each process retains the freshly armed hard timer.
record = dict(pid=os.getpid(), ppid=os.getppid(), role=role,
              remaining=signal.getitimer(signal.ITIMER_REAL)[0],
              started=time.monotonic(),
              cgroup=pathlib.Path('/proc/self/cgroup').read_text())
tmp = root / (str(os.getpid()) + '.tmp')
tmp.write_text(json.dumps(record))
tmp.rename(root / (str(os.getpid()) + '.json'))
with (root / (str(os.getpid()) + '.heartbeat')).open('wb', buffering=0) as heartbeat:
    while True:
        heartbeat.write(b'.')
        time.sleep(.01)
