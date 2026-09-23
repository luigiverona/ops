import fcntl, os, pathlib, pty, re, select, signal, subprocess, sys, tempfile, termios, time
sys.dont_write_bytecode = True
from ownership_cleanup import subreaper, cleanup_scope
subreaper()
workspace = tempfile.TemporaryDirectory(prefix="ops-pty-cleanup-")
record = pathlib.Path(workspace.name) / "scope"
env = dict(os.environ, OPS_OWNERSHIP_SCOPE_RECORD=str(record))
master, slave = pty.openpty()
before = termios.tcgetattr(slave)
def session():
    os.setsid()
    fcntl.ioctl(0, termios.TIOCSCTTY, 0)
p = subprocess.Popen([sys.argv[1], '-test.run=^TestOwnershipPTYHelper$', '-test.v'], stdin=slave, stdout=slave, stderr=slave, preexec_fn=session, env=env)
output = b''
def until(marker):
    global output
    end = time.monotonic()+12
    while marker not in output:
        if time.monotonic()>end: raise Exception('PTY timeout: '+repr(output))
        if select.select([master],[],[],.05)[0]: output += os.read(master,65536)
try:
    until(b'NATIVE INPUT READY')
    os.write(master,b'native-input\n')
    until(b'NATIVE TREE READY')
    if os.environ.get('OPS_OWNERSHIP_PTY_FAIL') == '1':
        raise RuntimeError('injected PTY failure')
    os.write(master,b'\x03')
    # sudo may forward keyboard interrupts into its own PTY. Explicitly
    # deliver cancellation to the owner, without changing session/TTY state.
    os.kill(p.pid, signal.SIGTERM)
    until(b'NATIVE CLEAN PASS')
    until(b'NATIVE SECOND INPUT')
    os.write(master,b'terminal-usable\n')
    p.wait(timeout=8)
    while select.select([master],[],[],.05)[0]: output += os.read(master,65536)
    assert p.returncode==0, output
    assert before==termios.tcgetattr(slave), 'terminal modes changed'
    sys.stdout.buffer.write(output)
finally:
    try:
        if p.poll() is None: p.kill()
        p.wait(timeout=8)
        if os.environ.get('OPS_OWNERSHIP_PTY_FAIL') == '1':
            scope = pathlib.Path(record.read_text().strip())
            assert 'populated 1' in (scope/'cgroup.events').read_text()
            print('OWNER-ONLY CLEANUP LEFT LIVE DESCENDANTS', flush=True)
    finally:
        try:
            cleanup_scope(record, p.pid, [int(pid) for pid in re.findall(rb"OWNED PID (\d+)", output)])
            print("PTY FIXTURE CLEANUP PASS", flush=True)
        finally:
            os.close(master);os.close(slave)
            workspace.cleanup()
