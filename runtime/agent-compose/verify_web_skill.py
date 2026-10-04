"""Read-only evidence for the original browser Skill upload/selection flow."""
import argparse
import base64
import http.cookiejar
import json
import pathlib
import re
import subprocess
import urllib.request
import uuid

state = pathlib.Path(__file__).resolve().parent / '.state'
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('task_id', type=uuid.UUID)
parser.add_argument('--expect-cleared', action='store_true')
args = parser.parse_args()
task_id = str(args.task_id)
origin = 'http://127.0.0.1:47420'


def login(name):
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    account = json.loads((state / name).read_text())
    req = urllib.request.Request(origin+'/api/v1/users/password-login',data=json.dumps(account).encode(),headers={'Content-Type':'application/json'})
    with client.open(req,timeout=10) as response:
        if json.load(response).get('code') != 0:
            raise SystemExit('Original fixture login failed.')
    return client


def api(client, route):
    with client.open(origin+route,timeout=20) as response:
        result = json.load(response)
        if result.get('code') != 0:
            raise SystemExit('Original API verification failed: '+str(result.get('code')))
        return result['data']


owner, outsider = login('web-account.json'), login('web-outsider-account.json')
task = api(owner,'/api/v1/users/tasks/'+task_id)
if task['content'] != '附件验收初始化。仅回复 WEB_ATTACHMENT_READY，不要使用工具。':
    raise SystemExit('Only the dedicated browser acceptance fixture may be inspected.')
fixture = json.loads((state/'web-skill-fixture.json').read_text())
skills = api(owner,'/api/v1/skills')
matching = [item for item in skills if item['name'] == fixture['name']]
if len(matching) != 1 or matching[0].get('enabled') is not True or matching[0].get('scope',{}).get('type') != 'team':
    raise SystemExit('Uploaded team Skill is not available through the original picker API.')
skill_id = matching[0]['id']
if any(item['id'] == skill_id for item in api(outsider,'/api/v1/skills')):
    raise SystemExit('Another team can see the uploaded Skill.')
history = api(owner,'/api/v1/users/tasks/rounds?id='+task_id+'&limit=10')
chunks = [(item,base64.b64decode(item.get('data') or '').decode('utf-8')) for item in history['chunks']]
if not any(item['event']=='task-running' and fixture['receipt'] in text for item,text in chunks):
    raise SystemExit('Real Agent Skill receipt is absent from original history.')
for item,text in chunks:
    if item['event']=='user-input' and fixture['receipt'] in base64.b64decode(json.loads(text)['content']).decode('utf-8'):
        raise SystemExit('Skill receipt was supplied in a prompt.')
sql = "SELECT e.sandbox_id FROM runtime_environments e JOIN runtime_task_intents i ON i.environment_id=e.id WHERE i.task_id='"+task_id+"'"
sandbox = subprocess.check_output(['docker','exec','jingjia-runtime-tests-20261002','psql','-U','postgres','-d','monkeycode_web_poc','-tAc',sql],text=True).strip()
if not re.fullmatch('[a-f0-9]{64}',sandbox):
    raise SystemExit('Invalid original runtime mapping.')
directory = '/root/.codingmatrix/project-tpl/.ai-ready/skills/'+fixture['name']
probe = subprocess.run(['docker','exec','agent-compose-'+sandbox[:12],'test','-e',directory],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
if args.expect_cleared and probe.returncode != 1:
    raise SystemExit('Cleared Skill directory still exists or Guest could not be inspected.')
if not args.expect_cleared:
    if probe.returncode != 0:
        raise SystemExit('Selected Skill directory is absent.')
    data = subprocess.check_output(['docker','exec','agent-compose-'+sandbox[:12],'cat',directory+'/references/中文回执.txt'])
    if data != (fixture['receipt']+'\n').encode():
        raise SystemExit('Skill reference bytes changed.')
report = {'task':task_id,'skill':fixture['name'],'skill_id':skill_id,'owner_picker':True,'other_team_hidden':True,
    'model_receipt_observed':True,'receipt_not_supplied_in_prompt':True,'selection_cleared':args.expect_cleared,
    'directory_absent':args.expect_cleared}
(state/'web-skill-report.json').write_text(json.dumps(report,indent=2),encoding='utf-8')
print(json.dumps(report,ensure_ascii=False,indent=2))
