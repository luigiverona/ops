# ARCHIVAL SOURCE ONLY. Historical result data is preserved alongside this file.
# This harness predates independent supervisor cleanup; use the maintained
# ownership_integration suite. The guard also prevents execution after copying.
raise SystemExit("Archived architecture harness: use ownership_integration tests")

import os,sys,pathlib,json,subprocess,time,pty,select
base=pathlib.Path(__file__).parent
cgpath=next(x[3:] for x in pathlib.Path('/proc/self/cgroup').read_text().splitlines() if x.startswith('0::'))
parent=pathlib.Path('/sys/fs/cgroup'+cgpath)
r={};created=[]
def until(fn):
 end=time.monotonic()+4
 while time.monotonic()<end:
  if fn():return
  time.sleep(.005)
 raise TimeoutError('condition')
def kill(c):
 (c/'cgroup.kill').write_text('1')
 until(lambda:'populated 0' in (c/'cgroup.events').read_text())
try:
 c=parent/'frozen-command';c.mkdir();created.append(c)
 nested=c/'nested';nested.mkdir();created.append(nested)
 (nested/'cgroup.freeze').write_text('1')
 until(lambda:'frozen 1' in (nested/'cgroup.events').read_text())
 marker=base/'first-instruction'
 code='import pathlib,os,time,signal;signal.alarm(8);pathlib.Path('+repr(str(marker))+').write_text(pathlib.Path("/proc/self/cgroup").read_text());time.sleep(7)'
 p=subprocess.Popen([str(base/'launch'),str(nested),'capture','/usr/bin/python3','-c',code],stdout=subprocess.PIPE,text=True)
 until(lambda:bool((nested/'cgroup.procs').read_text().strip()))
 pid=int((nested/'cgroup.procs').read_text().strip())
 before=pathlib.Path(f'/proc/{pid}/cgroup').read_text()
 assert not marker.exists()
 assert '0::'+cgpath+'/frozen-command/nested\n' in before
 (nested/'cgroup.freeze').write_text('0')
 started=json.loads(p.stdout.readline());assert started['started']==pid
 until(marker.exists);assert marker.read_text()==before
 # Poll the parent's events for descendant population change.
 events=os.open(c/'cgroup.events',os.O_RDONLY)
 initial=os.read(events,4096).decode();assert 'populated 1' in initial
 poll=select.poll();poll.register(events,select.POLLPRI|select.POLLERR)
 t=time.monotonic();(c/'cgroup.kill').write_text('1')
 notified=poll.poll(4000);assert notified
 os.lseek(events,0,0);after=os.read(events,4096).decode();os.close(events)
 until(lambda:'populated 0' in (c/'cgroup.events').read_text())
 output=p.communicate(timeout=4)[0];assert p.returncode==0
 assert not pathlib.Path(f'/proc/{pid}').exists()
 r['frozen_atomic_nested_kill']={'pid':pid,'before_user_code_cgroup':before,'marker_absent_while_frozen':True,'marker_after_thaw':marker.read_text(),'started':started,'wait':json.loads(output),'initial_events':initial,'poll_events':notified,'events_after_notification':after,'empty_events':(c/'cgroup.events').read_text(),'kill_and_wait_ms':round((time.monotonic()-t)*1000,3)}
 nested.rmdir();created.remove(nested);c.rmdir();created.remove(c)
 c=parent/'service-request';c.mkdir();created.append(c)
 unit='ops-wave-e-architecture-service-'+str(os.getpid())
 command=[str(base/'launch'),str(c),'capture','/usr/bin/systemd-run','--user','--quiet','--no-ask-password','--wait','--pipe','--collect','--unit='+unit,'--property=RuntimeMaxSec=5','--expand-environment=no','--','/usr/bin/cat','/proc/self/cgroup']
 out=subprocess.run(command,capture_output=True,text=True,timeout=8);assert out.returncode==0,out
 data=[json.loads(x) for x in out.stdout.splitlines()]
 assert data[-1]['error']=='' and unit+'.service' in data[-1]['stdout'] and '/service-request' not in data[-1]['stdout']
 r['user_manager_ipc']={'unit':unit+'.service','caller_cgroup':str(c),'output':data}
 kill(c);c.rmdir();created.remove(c)
 # Test scope bootstrap itself inside a fresh controlling PTY.
 unit='ops-wave-e-architecture-bootstrap-'+str(os.getpid())
 childcode='import os,sys,json;print(json.dumps({"ready":True,"pid":os.getpid(),"sid":os.getsid(0),"pgid":os.getpgrp(),"fg":os.tcgetpgrp(0),"cwd":os.getcwd(),"env":os.environ["WAVE_E_SENTINEL"],"arg":sys.argv[1]}),flush=True);assert input()=="scope-input";sys.exit(37)'
 pid,fd=pty.fork()
 if pid==0:
  os.chdir(base);os.environ['WAVE_E_SENTINEL']='literal-value'
  args=['systemd-run','--user','--scope','--quiet','--no-ask-password','--collect','--unit='+unit,'--property=Delegate=yes','--expand-environment=no','--','/usr/bin/python3','-c',childcode,'$WAVE_E_SENTINEL']
  os.execvp(args[0],args)
 output=b'';sent=False;end=time.monotonic()+5
 try:
  while time.monotonic()<end:
   if select.select([fd],[],[],.05)[0]:
    try:b=os.read(fd,65536)
    except OSError:break
    if not b:break
    output+=b
    if b'"ready": true' in output and not sent:os.write(fd,b'scope-input\n');sent=True
  else:raise TimeoutError('PTY bootstrap')
  _,status=os.waitpid(pid,0);assert os.waitstatus_to_exitcode(status)==37,output
  line=next(json.loads(s) for s in output.decode().splitlines() if s.startswith('{'))
  assert line['sid']==line['pgid']==line['fg']==pid
  assert line['cwd']==str(base) and line['env']=='literal-value' and line['arg']=='$WAVE_E_SENTINEL'
  r['scope_bootstrap']={'unit':unit+'.scope','output':line,'exit_code':37,'input_passed':True}
 finally:os.close(fd)
finally:
 for c in reversed(created):
  if c.exists():kill(c);c.rmdir()
 (base/'supplement-results.json').write_text(json.dumps(r,indent=2)+'\n')
print(json.dumps(r,indent=2))
