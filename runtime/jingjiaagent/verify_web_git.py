"""Read-only proof of real authenticated remote Git checkout and model output."""
import base64
import http.cookiejar
import hashlib
import json
import pathlib
import re
import subprocess
import urllib.request
import uuid

from linux_web_common import state
git = json.loads((state/'web-git.json').read_text(encoding='utf-8'))
saved = json.loads((state/'web-git-task.json').read_text(encoding='utf-8'))
task_id = str(uuid.UUID(saved['task']))
project_id = str(uuid.UUID(saved['project']))
client = urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))


def api(route,data=None,internal=False):
    headers = {'Content-Type':'application/json'}
    if internal:
        headers['Authorization'] = 'Bearer '+(state/'daemon.token').read_text().strip()
    req = urllib.request.Request('http://127.0.0.1:47424'+route,
                                 data=json.dumps(data).encode() if data is not None else None,
                                 headers=headers)
    with client.open(req,timeout=20) as response:
        result = json.load(response)
    if result.get('code') != 0:
        raise SystemExit('Original authenticated Git evidence API failed.')
    return result['data']


api('/api/v1/users/password-login',json.loads((state/'web-member-account.json').read_text()))
task = api('/api/v1/users/tasks/'+task_id)
if task['content'] != saved['content']:
    raise SystemExit('Only the dedicated Git acceptance task may be inspected.')
history = api('/api/v1/users/tasks/rounds?id='+task_id+'&limit=10')
observed = False
for item in history['chunks']:
    text = base64.b64decode(item.get('data') or '').decode('utf-8')
    if item['event'] == 'user-input':
        content = base64.b64decode(json.loads(text)['content']).decode('utf-8')
        if git['receipt'] in content:
            raise SystemExit('Git receipt was provided in a prompt.')
    if item['event'] == 'task-running' and git['receipt'] in text:
        observed = True
if not observed:
    raise SystemExit('Real Agent Git receipt is absent from original history.')
mapping = subprocess.check_output(['docker','exec','jingjiaagent-postgres-1','psql','-U','postgres',
    '-d','jingjiaagent','-tAc',"SELECT e.sandbox_id FROM runtime_environments e JOIN runtime_task_intents i ON i.environment_id=e.id WHERE i.task_id='"+task_id+"'"],text=True).strip()
if not re.fullmatch('[a-f0-9]{64}',mapping):
    raise SystemExit('Invalid Git runtime sandbox mapping.')
container = 'agent-compose-'+mapping[:12]


def guest(*args):
    return subprocess.check_output(['docker','exec',container,*args],timeout=20)


expected = subprocess.check_output(['git','--git-dir',str(state/'git-http'/'repos'/'fixture.git'),
                                    'show',git['commit']+':中文验收.txt'],timeout=20)
if expected.decode('utf-8').splitlines() != [git['receipt']]:
    raise SystemExit('Committed Git fixture content does not match the private receipt.')
if guest('cat','/workspace/中文验收.txt') != expected:
    raise SystemExit('Actual Git checkout Chinese file bytes changed.')
if guest('git','-C','/workspace','rev-parse','HEAD').decode().strip() != git['commit']:
    raise SystemExit('Actual Git checkout resolved another commit.')
if guest('git','-C','/workspace','status','--porcelain') != b'':
    raise SystemExit('Read-only Git acceptance unexpectedly modified its repository.')
config = guest('cat','/workspace/.git/config').decode()
if git['url'] not in config or any(git[field] in config for field in ('owner_token','member_token')):
    raise SystemExit('Git origin was lost or a fixture token persisted in .git/config.')
query = "SELECT count(*) FROM project_tasks WHERE task_id='"+task_id+"' AND project_id='"+project_id+"' AND git_identity_id='"+git['owner_identity']+"'"
if subprocess.check_output(['docker','exec','jingjiaagent-postgres-1','psql','-U','postgres',
                           '-d','jingjiaagent','-tAc',query],text=True).strip() != '1':
    raise SystemExit('Original business task lost its authorized project identity mapping.')
credential = api('/internal/git-credential',{'task_id':task_id,'vm_id':task['virtualmachine']['id'],
                                            'protocol':'http','host':'host.docker.internal:47593','path':'fixture.git'},internal=True)
if credential.get('error') or credential.get('password') != git['owner_token'] or credential.get('username') != git['username']:
    raise SystemExit('Original internal credential callback lost task/project authorization or identity metadata.')
audit = [json.loads(line) for line in (state/'web-git-audit.jsonl').read_text().splitlines()]
if not any(row['method']=='GET' and row['status']==200 for row in audit) or not any(row['method']=='POST' and row['status']==200 for row in audit):
    raise SystemExit('Authenticated smart HTTP discovery/fetch evidence is absent.')
report = {'task':task_id,'project':project_id,'real_smart_http_authenticated_checkout':True,
          'actual_commit_matches':True,'chinese_file_bytes_match':True,'real_model_receipt_in_history':True,
          'chinese_file_size':len(expected),'chinese_file_sha256':hashlib.sha256(expected).hexdigest(),
          'receipt_not_in_prompt':True,'repository_clean':True,'origin_preserved_no_token_in_git_config':True,
          'collaborator_uses_bound_project_identity':True,'original_credential_callback_scoped':True}
(state/'web-git-report.json').write_text(json.dumps(report,indent=2),encoding='utf-8')
print(json.dumps(report,ensure_ascii=False,indent=2))
