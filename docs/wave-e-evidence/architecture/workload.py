import os,sys,time,json,signal,pathlib
mode,root=sys.argv[1:3];root=pathlib.Path(root)
signal.alarm(12)
signal.signal(signal.SIGTERM,signal.SIG_IGN)
def record(role):
 d={'role':role,'pid':os.getpid(),'ppid':os.getppid(),'uid':os.getuid(),'gid':os.getgid(),'sid':os.getsid(0),'pgid':os.getpgrp(),'cgroup':pathlib.Path('/proc/self/cgroup').read_text()}
 (root/f'{os.getpid()}.json').write_text(json.dumps(d));return d
def leaf(role,newsession=False):
 if newsession:os.setsid()
 record(role)
 with (root/f'{os.getpid()}.heartbeat').open('wb',buffering=0) as f:
  while True:f.write(b'.');time.sleep(.01)
record('direct')
if mode=='tty':
 print(json.dumps({'tty_ready':True,'pid':os.getpid(),'sid':os.getsid(0),'pgid':os.getpgrp(),'fg':os.tcgetpgrp(0),'tty':[os.isatty(i) for i in range(3)]}),flush=True)
 data=input();assert data=='wave-e-input'
 print('TTY_INPUT_PASS',flush=True);print('TTY_STDERR_PASS',file=sys.stderr,flush=True);sys.exit(0)
if mode=='escape':
 target=sys.argv[3]
 with open(target+'/cgroup.procs','w') as f:f.write(str(os.getpid()))
 record('deliberate-sibling-migration');sys.exit(0)
if mode in ('stdout','stderr'):
 child=os.fork()
 if child==0:
  if mode=='stdout':os.close(2)
  else:os.close(1)
  leaf(mode+'-holder',True)
 while not (root/f'{child}.json').exists():time.sleep(.001)
 print('direct stdout',flush=True);print('direct stderr',file=sys.stderr,flush=True)
 sys.exit(0)
child=os.fork()
if child==0:
 record('child')
 for i in range(3):
  if os.fork()==0:leaf('grandchild'+str(i),i==1)
 leaf('child-wait')
if mode=='stress':
 # One bounded serial forker, at most one short-lived extra child at a time.
 if os.fork()==0:
  record('forker')
  for i in range(200):
   p=os.fork()
   if p==0:record('racing-child');time.sleep(.0005);os._exit(0)
   os.waitpid(p,0)
  os._exit(0)
leaf('parent-wait')
