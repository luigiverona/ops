import os,sys,pathlib,json,subprocess,time,ctypes,select,pty,signal
base=pathlib.Path(__file__).parent
assert os.getuid()==1000
assert ctypes.CDLL(None).prctl(36,1,0,0,0)==0 # subreaper: reclaim owned orphans
cgpath=next(x[3:] for x in pathlib.Path('/proc/self/cgroup').read_text().splitlines() if x.startswith('0::'))
parent=pathlib.Path('/sys/fs/cgroup'+cgpath)
results={'uid':os.getuid(),'scope':cgpath,'cases':[]}
allpids=set();created=[]
def records(d):
 a=[]
 for f in d.glob('*.json'):
  try:a.append(json.loads(f.read_text()))
  except json.JSONDecodeError:pass
 return a
def until(fn,seconds=4):
 end=time.monotonic()+seconds
 while time.monotonic()<end:
  if fn():return
  time.sleep(.005)
 raise TimeoutError('bounded condition failed')
def reap():
 while True:
  try:
   p,s=os.waitpid(-1,os.WNOHANG)
   if not p:return
  except ChildProcessError:return
def kill(c):
 (c/'cgroup.kill').write_text('1')
 until(lambda:'populated 0' in (c/'cgroup.events').read_text())
def newcase(name):
 c=parent/name;c.mkdir();created.append(c)
 d=base/name;d.mkdir()
 return c,d
def run(mode,name):
 c,d=newcase(name)
 parent_pids_before=set((parent/'cgroup.procs').read_text().split())
 p=subprocess.Popen([str(base/'launch'),str(c),'capture','/usr/bin/python3',str(base/'workload.py'),mode,str(d)],stdout=subprocess.PIPE,text=True)
 try:
  started=json.loads(p.stdout.readline());assert 'started' in started,started
  if mode in ('stdout','stderr'):
   until(lambda:len(records(d))>=2)
   until(lambda:not pathlib.Path('/proc/'+str(started['started'])).exists())
   time.sleep(.08)
   assert p.poll() is None,'Wait unexpectedly returned despite holder'
  else:
   until(lambda:len(records(d)) >= (7 if mode=='stress' else 5))
  if mode=='stress':time.sleep(.004 + int(name[-2:]) % 11 * .001)
  rr=records(d);ids={r['pid'] for r in rr};allpids.update(ids)
  for r in rr:assert f'0::{cgpath}/{name}\n' in r['cgroup'],r
  if mode not in ('stdout','stderr'):
   direct=next(r for r in rr if r['role']=='parent-wait')
   detached=next(r for r in rr if r['role']=='grandchild1')
   assert detached['sid']!=direct['sid'] and detached['pgid']!=direct['pgid']
  before=(c/'cgroup.events').read_text();assert 'populated 1' in before
  t=time.monotonic();kill(c);empty_ms=(time.monotonic()-t)*1000
  remaining=p.communicate(timeout=4)[0];assert p.returncode==0,remaining
  ended=json.loads(remaining.strip());assert ended['waited']
  after=(c/'cgroup.events').read_text()
  # Capture any fork records written between the readiness check and kill.
  rr=records(d);ids.update(r['pid'] for r in rr);allpids.update(ids)
  for r in rr:assert f'0::{cgpath}/{name}\n' in r['cgroup'],r
  reap();until(lambda:all(not pathlib.Path('/proc/'+str(x)).exists() for x in ids))
  heart={f.name:f.stat().st_size for f in d.glob('*.heartbeat')}
  time.sleep(.05);assert heart=={f.name:f.stat().st_size for f in d.glob('*.heartbeat')}
  assert all(str(x) not in (parent/'cgroup.procs').read_text().split() for x in ids)
  assert set((parent/'cgroup.procs').read_text().split())==parent_pids_before
  c.rmdir();created.remove(c)
  return {'case':name,'started':started,'records':rr,'events_before':before,'events_after':after,'empty_ms':round(empty_ms,3),'wait':ended,'heartbeats_stopped':True,'all_recorded_pids_gone':True,'no_parent_escape':True,'removed':True}
 finally:
  if c.exists():kill(c)
  if p.poll() is None:p.wait(timeout=4)
  reap()
def ttycase(atomic):
 name='tty-atomic' if atomic else 'tty-baseline';c,d=newcase(name)
 pid,fd=pty.fork()
 if pid==0:
  os.execv(str(base/'launch'),[str(base/'launch'),str(c) if atomic else 'baseline','tty','/usr/bin/python3',str(base/'workload.py'),'tty',str(d)])
 output=b'';sent=False;end=time.monotonic()+5
 try:
  while time.monotonic()<end:
   if select.select([fd],[],[],.05)[0]:
    try:b=os.read(fd,65536)
    except OSError:break
    if not b:break
    output+=b
    if b'"tty_ready": true' in output and not sent:os.write(fd,b'wave-e-input\n');sent=True
  else:raise TimeoutError('PTY timeout')
  _,status=os.waitpid(pid,0);assert status==0,output
  lines=output.decode().splitlines()
  start=next(json.loads(s) for s in lines if s.startswith('{') and '"started"' in s)
  ready=next(json.loads(s) for s in lines if s.startswith('{') and 'tty_ready' in s)
  assert ready['pgid']==ready['fg']==start['parent_pgid']==pid
  assert ready['sid']==pid and all(ready['tty'])
  assert 'TTY_INPUT_PASS' in lines and 'TTY_STDERR_PASS' in lines
  rr=records(d);allpids.update(r['pid'] for r in rr)
  expected=cgpath+'/'+name if atomic else cgpath
  assert '0::'+expected+'\n' in rr[0]['cgroup']
  until(lambda:'populated 0' in (c/'cgroup.events').read_text());c.rmdir();created.remove(c)
  return {'case':name,'output':output.decode(),'records':rr,'pass':True}
 finally:os.close(fd)
try:
 # Invalid FD must fail; no fallback to uncontained fork/exec.
 c,d=newcase('badfd')
 bad=subprocess.run([str(base/'launch'),str(c),'badfd','/usr/bin/true'],capture_output=True,text=True,timeout=4)
 assert bad.returncode==2 and 'bad file descriptor' in bad.stdout,bad
 results['badfd']=bad.stdout;c.rmdir();created.remove(c)
 for mode,name in [('tree','normal-tree'),('stdout','stdout-holder'),('stderr','stderr-holder')]+[('stress',f'fork-{i:02}') for i in range(20)]:
  r=run(mode,name);results['cases'].append(r);print(name,'PASS',r['empty_ms'],flush=True)
 for atomic in [False,True]:results['cases'].append(ttycase(atomic));print('tty',atomic,'PASS',flush=True)
 # Controlled migration stays within disposable delegated scope; no host targets.
 c,d=newcase('escape-source');target=parent/'escape-target';target.mkdir();created.append(target)
 migrated=subprocess.run([str(base/'launch'),str(c),'capture','/usr/bin/python3',str(base/'workload.py'),'escape',str(d),str(target)],capture_output=True,text=True,timeout=4)
 assert migrated.returncode==0,migrated
 rr=records(d);assert len(rr)==1 and '0::'+cgpath+'/escape-target\n' in rr[0]['cgroup']
 results['deliberate_user_escape']={'record':rr[0],'output':migrated.stdout};allpids.add(rr[0]['pid'])
 for x in [c,target]:x.rmdir();created.remove(x)
 results['all_passed']=True
finally:
 for c in reversed(created):
  if c.exists():kill(c);c.rmdir()
 reap()
 results['cleanup']={'created_cgroups_remaining':[str(c) for c in created if c.exists()],'recorded_pids_remaining':[p for p in allpids if pathlib.Path('/proc/'+str(p)).exists()]}
 (base/'results-architecture.json').write_text(json.dumps(results,indent=2)+'\n')
print('COMPLETE',results['cleanup'],flush=True)
