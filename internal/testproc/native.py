"""Surviving subreaper for one native test; no global resource cleanup."""
import os
import pathlib
import signal
import subprocess
import sys
import tempfile
import time

sys.dont_write_bytecode = True
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1] / 'run/testdata'))
from ownership_cleanup import cleanup_scope, subreaper

subreaper()
with tempfile.TemporaryDirectory(prefix='ops-n-') as directory:
    root = pathlib.Path(directory)
    env = dict(os.environ, OPS_NATIVE_TEST_CHILD=sys.argv[2],
               OPS_OWNERSHIP_SCOPE_RECORD=str(root / 'scope'), OPS_NATIVE_READY=str(root / 'ready'), TMPDIR=directory)
    # File output avoids an inherited pipe delaying supervisor failure cleanup.
    with (root / 'output').open('w+b') as output:
        p = subprocess.Popen([sys.argv[1], '-test.run=' + sys.argv[3],
                              '-test.v', '-test.timeout=100s'], env=env,
                             stdout=output, stderr=subprocess.STDOUT)
        def reap_adopted():
            for child in pathlib.Path('/proc/self/task/%d/children' % os.getpid()).read_text().split():
                if int(child) != p.pid:
                    os.waitpid(int(child), os.WNOHANG)
        try:
            if sys.argv[4] == 'owner-death':
                end = time.monotonic() + 10
                while not (root / 'ready').exists():
                    if p.poll() is not None or time.monotonic() >= end:
                        raise RuntimeError('native GPG owner did not become ready')
                    reap_adopted()
                    time.sleep(.01)
                scope = pathlib.Path((root / 'scope').read_text())
                pids = [int(pid) for procs in scope.rglob('cgroup.procs') for pid in procs.read_text().split()]
                assert any(pathlib.Path('/proc/%d/comm' % pid).read_text().strip() == 'keyboxd' for pid in pids)
                p.kill()
                p.wait(timeout=3)
                assert 'populated 1' in (scope / 'cgroup.events').read_text()
                print('GPG owner death left live keyboxd: reproduced')
            else:
                end = time.monotonic() + 110
                while p.poll() is None:
                    reap_adopted()
                    if time.monotonic() >= end:
                        raise RuntimeError('native test deadline')
                    time.sleep(.01)
        finally:
            try:
                if p.poll() is None:
                    p.kill()
                p.wait(timeout=3)
                cleanup_scope(root / 'scope', p.pid)
            finally:
                # Adopted, unreaped children belong to this single-threaded
                # supervisor. Their PIDs cannot be recycled before waitpid.
                end = time.monotonic() + 25
                while True:
                    children = pathlib.Path('/proc/self/task/%d/children' % os.getpid()).read_text().split()
                    if not children:
                        break
                    for child in children:
                        pid = int(child)
                        if os.waitpid(pid, os.WNOHANG)[0] == 0:
                            os.kill(pid, signal.SIGKILL)
                    if time.monotonic() >= end:
                        raise RuntimeError('native fixture reap timeout')
                    time.sleep(.01)
                output.seek(0)
                data = output.read().decode(errors='replace')
                print(data, end='')
        if sys.argv[4] == 'owner-death':
            assert p.returncode == -signal.SIGKILL, p.returncode
            assert all(not pathlib.Path('/proc/%d' % pid).exists() for pid in pids)
        else:
            assert p.returncode == 0, p.returncode
        assert '--- SKIP:' not in data, 'native fixture silently skipped'
assert not root.exists()
print('NATIVE SUPERVISOR CLEANUP PASS', root)
