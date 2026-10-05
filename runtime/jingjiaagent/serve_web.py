"""Run the original backend with ignored PoC config; secrets stay off stdout."""
import os
import pathlib
import subprocess
import sys

root = pathlib.Path(__file__).resolve().parent
env = {k:v for k,v in os.environ.items() if not k.startswith(('JINGJIAAGENT_','TASKFLOW_'))}
name = 'jingjiaagent-server.exe' if os.name=='nt' else 'jingjiaagent-server'
with (root / '.state' / 'web' / 'backend.log').open('ab',buffering=0) as output:
    print('Starting isolated backend at http://127.0.0.1:47424',flush=True)
    sys.exit(subprocess.call([str(root / '.build' / name)],cwd=root / '.state' / 'web',env=env,stdout=output,stderr=output))
