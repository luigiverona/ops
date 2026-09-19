import os,pathlib,subprocess,json
p=pathlib.Path('/sys/fs/cgroup') / next(x[3:] for x in pathlib.Path('/proc/self/cgroup').read_text().splitlines() if x.startswith('0::')).lstrip('/')
print('self_cgroup',pathlib.Path('/proc/self/cgroup').read_text(),flush=True)
print('entries',sorted(x.name for x in p.iterdir()),flush=True)
c=p/'permission-probe'
try:
 c.mkdir()
 fd=os.open(c,os.O_PATH|os.O_DIRECTORY|os.O_CLOEXEC)
 print('O_PATH',fd,flush=True);os.close(fd)
 for q in [p,c]:
  subprocess.run(['stat','-c','%n uid=%u gid=%g mode=%a',str(q),*[str(q/x) for x in ['cgroup.procs','cgroup.events','cgroup.kill','cgroup.subtree_control']]],check=True)
  subprocess.run(['getfacl','-cp',str(q),*[str(q/x) for x in ['cgroup.procs','cgroup.events','cgroup.kill']]],check=True)
 print('events', (c/'cgroup.events').read_text(),flush=True)
 for f,flags in [('cgroup.procs',os.O_RDWR),('cgroup.events',os.O_RDONLY),('cgroup.kill',os.O_WRONLY)]:
  fd=os.open(c/f,flags|os.O_CLOEXEC);os.close(fd);print('open',f,'PASS',flush=True)
finally:
 if c.exists():c.rmdir();print('rmdir PASS',flush=True)
