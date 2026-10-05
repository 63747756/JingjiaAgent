"""SIGKILL only the verified capacity-a daemon during this test's real Run."""
import http.server
import json
import os
import pathlib
import re
import secrets
import subprocess
import sys
import threading
import time
import urllib.request

root=pathlib.Path(__file__).resolve().parent
state=root/'.state'
name='jingjiaagent-runtime-fault-daemon-1'
revision=json.loads((root/'source.lock.json').read_text())['patch_revision']
images={}
for kind,tag in [('daemon',f'jingjiaagent-daemon:c03302d-p{revision}'),('guest',f'jingjiaagent-guest:c03302d-p{revision}')]:
    item=json.loads(subprocess.check_output(['docker','image','inspect',tag]))[0]
    if item['Config']['Labels'].get('jingjiaagent.image.revision')!=str(revision):raise SystemExit('Unexpected fault fixture image revision')
    images[kind]=item['Id']
envfile=state/'fault.env'
envfile.write_text(f"JINGJIAAGENT_RUNTIME_DAEMON_IMAGE={images['daemon']}\nJINGJIAAGENT_RUNTIME_GUEST_IMAGE={images['guest']}\nJINGJIAAGENT_RUNTIME_PORT=47417\n",encoding='utf-8')
subprocess.run(['docker','compose','-p','jingjiaagent-runtime-fault','--env-file',str(envfile),'-f',str(root/'compose.yaml'),'up','-d','--wait','--wait-timeout','60'],check=True)
(state/'fault-images.json').write_text(json.dumps(images,indent=2),encoding='utf-8')
token=(state/'daemon.token').read_text().strip()
control_token=secrets.token_urlsafe(32)
url='http://127.0.0.1:47417'
def validate():
    item=json.loads(subprocess.check_output(['docker','inspect',name]))[0]
    if item['Image']!=images['daemon'] or item['Config']['Labels'].get('com.docker.compose.project')!='jingjiaagent-runtime-fault':
        raise RuntimeError('Unexpected independent daemon fixture')
    return item
def rpc(method,body):
    req=urllib.request.Request(url+'/agentcompose.v2.RunService/'+method,data=json.dumps(body).encode(),headers={'Content-Type':'application/json','Authorization':'Bearer '+token})
    with urllib.request.urlopen(req,timeout=10) as response:return json.load(response)
validate()
if not os.environ.get('JINGJIAAGENT_RUNTIME_TEST_DATABASE_URL'):raise SystemExit('Set an isolated test database.')
class Control(http.server.BaseHTTPRequestHandler):
    executed=False
    def log_message(self,*args):pass
    def do_POST(self):
        try:
            if self.path!='/restart' or self.headers.get('Authorization')!='Bearer '+control_token:raise RuntimeError('Invalid controller request')
            body=json.loads(self.rfile.read(min(int(self.headers.get('Content-Length','0')),4096)))
            validate()
            runs=rpc('ListRuns',{'status':'RUN_STATUS_RUNNING','limit':500}).get('runs',[])
            if len(runs)!=1 or runs[0]['runId']!=body['run_id'] or runs[0]['sandboxId']!=body['sandbox_id'] or Control.executed:raise RuntimeError('Unexpected active fixture Run')
            Control.executed=True
            subprocess.run(['docker','kill','--signal','KILL',name],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=20)
            subprocess.run(['docker','start',name],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=20)
            deadline=time.monotonic()+45
            while True:
                try:
                    rpc('GetRun',{'runId':body['run_id']});break
                except Exception:
                    if time.monotonic()>deadline:raise RuntimeError('Daemon recovery deadline')
                    time.sleep(.25)
            self.send_response(200);self.end_headers()
        except Exception:
            self.send_response(500);self.end_headers()
server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Control)
threading.Thread(target=server.serve_forever,daemon=True).start()
env=dict(os.environ,JINGJIAAGENT_RUNTIME_AGENT_FAULT_LIVE_TEST='1',JINGJIAAGENT_RUNTIME_DAEMON_RESTART_LIVE_TEST='1',JINGJIAAGENT_RUNTIME_TEST_URL=url,
    JINGJIAAGENT_RUNTIME_TEST_FAULT_CONTROLLER=f'http://127.0.0.1:{server.server_port}',JINGJIAAGENT_RUNTIME_TEST_FAULT_TOKEN=control_token,
    JINGJIAAGENT_RUNTIME_TEST_TOKEN_FILE=str(state/'daemon.token'),JINGJIAAGENT_RUNTIME_MODEL_CONFIG=str(state/'model.json'),
    JINGJIAAGENT_RUNTIME_TEST_GUEST_IMAGE=images['guest'],GOTMPDIR=str(root/'.build/go-tmp'))
try:
    result=subprocess.run(['go','test','./pkg/runtimeadapter','-run','^TestLiveAgentFaultRecovery$','-count=1','-v','-timeout','6m'],cwd=root.parent.parent/'backend',env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
    output=result.stdout.decode('utf-8',errors='replace')
    for secret in [token,control_token,json.loads((state/'model.json').read_text())['api_key']]:output=output.replace(secret,'[redacted]')
    output=re.sub(r'invalid header:[A-Za-z0-9+/=_-]+','invalid header:[redacted upstream headers]',output)
    (state/'daemon-fault-live.log').write_text(output,encoding='utf-8')
    (state/f'daemon-fault-p{revision}-live.log').write_text(output,encoding='utf-8');print(output,end='')
finally:
    server.shutdown()
    if not validate()['State']['Running']:subprocess.run(['docker','start',name],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
sys.exit(result.returncode)
