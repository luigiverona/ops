"""Exercise retained architecture workloads without relying on their parent."""
import json
import os
import pathlib
import signal
import subprocess
import sys
import tempfile
import time

sys.dont_write_bytecode = True
from ownership_cleanup import subreaper

subreaper()
fixture = pathlib.Path(__file__).resolve().parents[3] / 'docs/wave-e-evidence/architecture/workload.py'
for mode in ('tree', 'stress', 'stdout', 'stderr'):
    with tempfile.TemporaryDirectory(prefix='ops-architecture-expiration-') as directory:
        root = pathlib.Path(directory)
        p = subprocess.Popen([sys.executable, str(fixture), mode, directory],
                             env=dict(os.environ, OPS_ARCH_FIXTURE_SECONDS='1'),
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        records = []
        try:
            end = time.monotonic() + 4
            while True:
                try:
                    records = [json.loads(f.read_text()) for f in root.glob('*.json')]
                except json.JSONDecodeError:
                    if time.monotonic() >= end:
                        raise RuntimeError('incomplete architecture record')
                    time.sleep(.005)
                    continue
                roles = {r['role'] for r in records}
                if (mode in ('stdout', 'stderr') and mode+'-holder' in roles or
                    mode == 'tree' and {'child-wait', 'grandchild0', 'grandchild1', 'grandchild2'} <= roles or
                    mode == 'stress' and {'forker', 'racing-child', 'grandchild2'} <= roles):
                    break
                if time.monotonic() >= end:
                    raise RuntimeError('architecture fixture not ready')
                time.sleep(.005)
            assert all(0 < r['deadline_remaining'] <= 1 for r in records), records
            if mode in ('stdout', 'stderr'):
                assert p.wait(timeout=2) == 0  # natural parent exit retains a writer
            else:
                p.kill()
                p.wait(timeout=2)
            statuses = {}
            # No descendant signal in the measured interval: require SIGALRM.
            while True:
                try:
                    pid, status = os.waitpid(-1, os.WNOHANG)
                except ChildProcessError:
                    break
                if pid:
                    statuses[pid] = status
                else:
                    if time.monotonic() >= end:
                        raise RuntimeError('architecture descendant failed expiration')
                    time.sleep(.005)
            looping = [r for r in records if r['role'] in
                       ('child-wait', 'grandchild0', 'grandchild1', 'grandchild2',
                        'stdout-holder', 'stderr-holder')]
            assert looping
            for r in looping:
                status = statuses[r['pid']]
                assert os.WIFSIGNALED(status) and os.WTERMSIG(status) == signal.SIGALRM, r
            assert all(not pathlib.Path('/proc/%d' % r['pid']).exists() for r in records)
            sizes = {f.name: f.stat().st_size for f in root.glob('*.heartbeat')}
            time.sleep(.03)
            assert sizes == {f.name: f.stat().st_size for f in root.glob('*.heartbeat')}
            print(mode, 'parent gone, independent SIGALRM, no surviving writer: PASS')
        finally:
            if p.poll() is None:
                p.kill()
            p.wait(timeout=2)
            end = time.monotonic() + 3
            while True:
                children = pathlib.Path('/proc/self/task/%d/children' % os.getpid()).read_text().split()
                if not children:
                    break
                for child in children:
                    pid = int(child)
                    if os.waitpid(pid, os.WNOHANG)[0] == 0:
                        os.kill(pid, signal.SIGKILL)
                if time.monotonic() >= end:
                    raise RuntimeError('architecture fallback reap timeout')
                time.sleep(.005)
    assert not root.exists()
