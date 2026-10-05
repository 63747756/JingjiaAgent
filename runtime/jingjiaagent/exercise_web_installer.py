"""Original authenticated Web API -> Linux Docker installer -> real TLS heartbeat.

No Agent Run or model call is made. Only the named PoC installation slot and
its labeled Docker resources are used. Credentials and tickets stay private.
"""
import http.cookiejar
import json
import pathlib
import re
import ssl
import subprocess
import time
import urllib.error
import urllib.request
import uuid

root = pathlib.Path(__file__).resolve().parent
state = root / '.state'
private = state / 'installer'
node = json.loads((state / 'web-installer-config.json').read_text())['nodes'][0]
project = 'jingjiaagent-node-' + uuid.UUID(node['id']).hex
platform = 'http://127.0.0.1:47424'
images = json.loads((state / 'images.json').read_text())
tag = 'jingjiaagent-runtime-installer-client:p7'

def client():
    return urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

def query(opener, path, body=None):
    request = urllib.request.Request(platform+path, data=json.dumps(body).encode() if body is not None else None, headers={'Content-Type':'application/json'})
    try:
        with opener.open(request,timeout=60) as response:
            data=response.read(); content=response.headers.get('Content-Type','')
            return response.status, json.loads(data) if 'json' in content else data
    except urllib.error.HTTPError as response:
        return response.code, {}

def api(opener,path,body=None):
    status,result=query(opener,path,body)
    if status != 200 or not isinstance(result,dict) or result.get('code') != 0:
        raise SystemExit('Original API check failed: '+path.split('?')[0])
    return result['data']

def get_snapshot():
    context=ssl.create_default_context(cafile=node['ca_file'])
    request=urllib.request.Request(node['url']+'/internal/jingjiaagent/node',headers={'Authorization':'Bearer '+pathlib.Path(node['token_file']).read_text().strip()})
    with urllib.request.urlopen(request,context=context,timeout=5) as response:
        return json.load(response)

opener=client()
api(opener,'/api/v1/teams/users/login',json.loads((state/'web-account.json').read_text()))
anonymous=client()
if query(anonymous,'/api/v1/teams/hosts/install-command')[0] != 401:
    raise SystemExit('Anonymous installation command was not rejected.')
for path in ('install','install-bundle'):
    if query(anonymous,'/api/v1/users/hosts/'+path+'?token='+str(uuid.uuid4()))[0] != 403:
        raise SystemExit('Unknown installation ticket was not rejected.')
member=client()
api(member,'/api/v1/users/password-login',json.loads((state/'web-member-account.json').read_text()))
status,result=query(member,'/api/v1/teams/hosts/install-command')
if status == 200 and result.get('code') == 0:
    raise SystemExit('Ordinary member obtained installer credentials.')

# BuildKit FROM resolves registry names rather than local image IDs. Bind
# private local tags to the already verified IDs and disable pulling.
subprocess.run(['docker','tag',images['daemon'],'jingjiaagent-runtime-installer-daemon:verified-local'],check=True)
subprocess.run(['docker','tag','sha256:862099ada15c669000bef53aa4cb9d821262829f45b0dda2159ccb276443043b','jingjiaagent-runtime-installer-cli:verified-local'],check=True)
with (private/'client-build.log').open('wb') as log:
    subprocess.run(['docker','build','--pull=false','-f',str(root/'Dockerfile.installer-client'),'-t',tag,str(root)],check=True,stdout=log,stderr=log)
client_image=json.loads(subprocess.check_output(['docker','image','inspect',tag]))[0]['Id']

def install(iteration):
    command=api(opener,'/api/v1/teams/hosts/install-command')['command']
    match=re.search(r'token=([0-9a-f-]{36})',command)
    if not match or 'taskflow' in command.lower(): raise SystemExit('Unexpected original installation command.')
    ticket=match.group(1)
    status,script=query(anonymous,'/api/v1/users/hosts/install?token='+ticket)
    if status != 200 or not isinstance(script,bytes): raise SystemExit('Authorized install script unavailable.')
    file=private/'install.sh'; file.write_bytes(script); file.chmod(0o600)
    # This fixture runs against the real local Docker socket. The original
    # administrator command remains usable on a configured Linux host.
    with (private/('install-'+str(iteration)+'.log')).open('wb') as log:
        result=subprocess.run(['docker','run','--rm','--name','jingjiaagent-runtime-install-client-20261003','--label','jingjiaagent.runtime.poc=1','-v','/var/run/docker.sock:/var/run/docker.sock','-v',str(file)+':/install.sh:ro',client_image,'/install.sh'],stdout=log,stderr=log)
    if result.returncode: raise SystemExit('Linux node installer failed; private log saved.')
    snapshot=get_snapshot()
    ready=api(anonymous,'/api/v1/users/hosts/install-status',dict(token=ticket,instance_id=snapshot['instance_id'],fingerprint=snapshot['fingerprint']))
    if not ready.get('ready'): raise SystemExit('Installed node heartbeat was not confirmed.')
    for path in ('install','install-bundle'):
        if query(anonymous,'/api/v1/users/hosts/'+path+'?token='+ticket)[0] != 403:
            raise SystemExit('Completed ticket was reusable for a credential download.')
    wrong=dict(token=ticket,instance_id=str(uuid.uuid4()),fingerprint=snapshot['fingerprint'])
    if query(anonymous,'/api/v1/users/hosts/install-status',wrong)[0] != 409:
        raise SystemExit('Wrong-machine confirmation was accepted.')
    return snapshot

before=install(1)
marker='installer-retains-existing-node-data'
subprocess.run(['docker','exec',project+'-daemon','python3','-c',"from pathlib import Path; Path('/data/installer-acceptance-marker').write_text('"+marker+"')"],check=True,stdout=subprocess.DEVNULL)
after=install(2)
if (before['instance_id'],before['fingerprint']) != (after['instance_id'],after['fingerprint']):
    raise SystemExit('Reinstall changed the persisted node identity.')
actual_marker=subprocess.check_output(['docker','exec',project+'-daemon','cat','/data/installer-acceptance-marker']).decode()
if actual_marker != marker: raise SystemExit('Reinstall lost existing persistent node data.')
hosts=api(opener,'/api/v1/teams/hosts')
# Original API shape includes the team host records; do not invent enrollment
# by writing host rows or synthesizing a heartbeat in the fixture.
if node['id'] not in json.dumps(hosts): raise SystemExit('Installed node did not appear in the original team host API.')
report={'original_admin_install_command':True,'anonymous_and_member_denied':True,'unknown_tickets_denied':True,
        'actual_linux_docker_install':True,'verified_https_node_identity':True,'heartbeat_registered_original_host':True,
        'completed_ticket_downloads_denied':True,'wrong_machine_confirmation_denied':True,
        'repeat_install_identity_retained':True,'repeat_install_data_retained':True,
        'actual_agent_or_model_called':False,'independent_linux_server_tested':False}
(state/'installer-acceptance.json').write_text(json.dumps(report,indent=2))
print(json.dumps(report,indent=2))
