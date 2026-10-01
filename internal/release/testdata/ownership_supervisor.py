"""Survives fixture/Go-owner death; cleans only its own children and workspace."""
import json
import os
import pathlib
import select
import signal
import subprocess
import sys
import tempfile
import time

sys.dont_write_bytecode = True
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2] / 'run/testdata'))
from ownership_cleanup import subreaper, cleanup_scope

subreaper()
mode = sys.argv[1]
workspace = tempfile.TemporaryDirectory(prefix='ops-release-ownership-')
root = pathlib.Path(workspace.name)
env = dict(os.environ, OPS_RELEASE_FIXTURE_DIR=str(root))
p = None
records = []


def until(predicate, seconds=5):
    end = time.monotonic() + seconds
    while not predicate():
        if time.monotonic() >= end:
            raise RuntimeError('release supervisor deadline')
        time.sleep(.005)


def children():
    # Only unreaped children of this single-threaded, surviving supervisor.
    return [int(pid) for pid in pathlib.Path(
        '/proc/self/task/%d/children' % os.getpid()).read_text().split()]


def reap_children():
    end = time.monotonic() + 8
    while children():
        for pid in children():
            try:
                if os.waitpid(pid, os.WNOHANG)[0] == 0:
                    # An unreaped child cannot have its PID recycled; no other
                    # thread or signal handler reaps in this supervisor.
                    os.kill(pid, signal.SIGKILL)
            except ChildProcessError:
                pass
        if time.monotonic() >= end:
            raise RuntimeError('release child cleanup timeout')
        time.sleep(.005)


try:
    if mode == 'expiration':
        env['OPS_RELEASE_FIXTURE_SECONDS'] = '1'
        p = subprocess.Popen([sys.executable, str(pathlib.Path(__file__).with_name('ownership.py'))],
                             env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        until(lambda: len(list(root.glob('*.json'))) == 3)
        records = [json.loads(f.read_text()) for f in root.glob('*.json')]
        assert {r['role'] for r in records} == {'parent', 'child', 'grandchild'}
        assert all(0 < r['remaining'] <= 1 for r in records), records
        assert all(pathlib.Path('/proc/%d' % r['pid']).exists() for r in records)
        p.kill()
        p.wait(timeout=2)
        statuses = {}
        def expired():
            while True:
                try:
                    pid, status = os.waitpid(-1, os.WNOHANG)
                except ChildProcessError:
                    break
                if pid == 0:
                    break
                statuses[pid] = (status, time.monotonic())
            return len(statuses) == 2
        until(expired, 3)
        for r in records:
            if r['role'] != 'parent':
                status, observed = statuses[r['pid']]
                assert os.WIFSIGNALED(status) and os.WTERMSIG(status) == signal.SIGALRM
                assert observed - r['started'] < 2, (r, observed)
                print(r['role'], 'independent SIGALRM expiration PASS', flush=True)
    else:
        env['OPS_RELEASE_PARENT_DEATH'] = '1' if mode == 'parent-death' else '0'
        p = subprocess.Popen([sys.argv[2], '-test.run=^TestNativeUpdaterOwnershipHelper$', '-test.v'],
                             env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        if mode == 'parent-death':
            output = b''
            end = time.monotonic() + 15
            while b'RELEASE TREE READY' not in output:
                if time.monotonic() >= end:
                    raise RuntimeError('release owner not ready: ' + repr(output))
                if select.select([p.stdout], [], [], .05)[0]:
                    block = os.read(p.stdout.fileno(), 65536)
                    if not block:
                        raise RuntimeError('release owner exited: ' + repr(output))
                    output += block
            records = [json.loads(f.read_text()) for f in root.glob('*.json')]
            assert len(records) == 3
            p.kill()
            p.wait(timeout=3)
            scope = pathlib.Path((root / 'scope').read_text())
            assert 'populated 1' in (scope / 'cgroup.events').read_text()
            print('release owner death left live descendants: reproduced', flush=True)
        else:
            output, _ = p.communicate(timeout=35)
            assert p.returncode == 0, output
            print(output.decode(), end='')
finally:
    try:
        if p is not None:
            try:
                if p.poll() is None:
                    p.kill()
                p.wait(timeout=3)
            finally:
                try:
                    cleanup_scope(root / 'scope', p.pid,
                                  [r['pid'] for r in records])
                finally:
                    reap_children()
        for f in root.glob('*.json'):
            r = json.loads(f.read_text())
            assert not pathlib.Path('/proc/%d' % r['pid']).exists(), r
        assert not children()
    finally:
        workspace.cleanup()
        assert not root.exists()
print('RELEASE CLEANUP PASS: processes, scope, cgroups and workspace absent', root)
