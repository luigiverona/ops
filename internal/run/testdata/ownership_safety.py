import json, os, pathlib, signal, subprocess, tempfile, time
import sys
sys.dont_write_bytecode = True
from ownership_cleanup import subreaper
subreaper()
for mode in ('tree', 'fork'):
    with tempfile.TemporaryDirectory(prefix='ops-fixture-expiration-') as directory:
        root = pathlib.Path(directory)
        p = subprocess.Popen(['python3', str(pathlib.Path(__file__).with_name('ownership.py')), mode, directory], env=dict(os.environ, OPS_FIXTURE_SECONDS='1'), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        records = []
        deadline = time.monotonic()+5
        try:
            while not (root/'ready').exists():
                if time.monotonic() >= deadline: raise RuntimeError('fixture not ready')
                time.sleep(.005)
            if mode == 'fork':
                while not any(json.loads(f.read_text())['role'].startswith('fork-') for f in root.glob('*.json')):
                    if time.monotonic() >= deadline: raise RuntimeError('grandchild not ready')
                    time.sleep(.005)
            records = [json.loads(f.read_text()) for f in root.glob('*.json')]
            children = [r for r in records if r['role'].startswith('leaf-')]
            assert len(children) == 3
            assert all(0 < r['deadline_remaining'] <= 1 for r in records), records
            p.kill()
            p.wait(timeout=2)
            for r in children:
                # No signal is sent to children: only their own timer can expire them.
                while True:
                    pid, status = os.waitpid(r['pid'], os.WNOHANG)
                    if pid:
                        assert os.WIFSIGNALED(status) and os.WTERMSIG(status)==signal.SIGALRM, status
                        break
                    if time.monotonic()>=deadline: raise RuntimeError('child failed self-expiration')
                    time.sleep(.005)
            records = [json.loads(f.read_text()) for f in root.glob('*.json')]
            assert all(0 < r['deadline_remaining'] <= 1 for r in records)
            for r in records:
                try: os.waitpid(r['pid'], os.WNOHANG)
                except ChildProcessError: pass
            assert all(not pathlib.Path('/proc/%d'%r['pid']).exists() for r in records)
            print('independent deadlines, unexpected parent death, SIGALRM expiration, all recorded PIDs reaped: PASS')
        finally:
            if p.poll() is None: p.kill(); p.wait(timeout=2)
            # Exact children recorded by this disposable fixture only; failure fallback.
            for f in root.glob('*.json'):
                r=json.loads(f.read_text())
                try:
                    pid,status=os.waitpid(r['pid'],os.WNOHANG)
                    if pid==0:
                        os.kill(r['pid'],signal.SIGKILL)
                        cleanup_deadline=time.monotonic()+2
                        while os.waitpid(r['pid'],os.WNOHANG)[0]==0:
                            if time.monotonic()>=cleanup_deadline: raise RuntimeError('fixture reap timeout')
                            time.sleep(.005)
                except ChildProcessError: pass
