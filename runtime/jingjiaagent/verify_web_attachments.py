"""Read-only verification of browser-created attachments in the local Web PoC.

Uses original authenticated history, asset and file APIs. It never submits a
task, changes application state or prints credentials/presigned URLs.
"""
import argparse
import base64
import hashlib
import http.cookiejar
import json
import pathlib
import re
import subprocess
import urllib.error
import urllib.parse
import urllib.request
import uuid

from linux_web_common import state
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('task_id', type=uuid.UUID)
parser.add_argument('--expect-cleared', action='store_true')
args = parser.parse_args()
task_id = str(args.task_id)
origin = 'http://127.0.0.1:47424'


def client_for(account_name):
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    account = json.loads((state / account_name).read_text())
    request = urllib.request.Request(origin + '/api/v1/users/password-login', data=json.dumps(account).encode(), headers={'Content-Type': 'application/json'})
    with client.open(request, timeout=10) as response:
        if json.load(response).get('code') != 0:
            raise SystemExit('Original fixture login failed')
    return client


def api(client, route):
    with client.open(origin + route, timeout=20) as response:
        result = json.load(response)
        if result.get('code') != 0:
            raise SystemExit('Original API verification failed: ' + str(result.get('code')))
        return result['data']


owner = client_for('web-account.json')
outsider = client_for('web-outsider-account.json')
anonymous = urllib.request.build_opener(urllib.request.ProxyHandler({}))
task = api(owner, '/api/v1/users/tasks/' + task_id)
if task['content'] != '附件验收初始化。仅回复 JINGJIAAGENT_WEB_ATTACHMENT_READY，不要使用工具。':
    raise SystemExit('Only the dedicated browser attachment fixture may be inspected.')
environment = task['virtualmachine']['id']
fixture = json.loads((state / 'web-attachment-fixture.json').read_text())
history = api(owner, '/api/v1/users/tasks/rounds?' + urllib.parse.urlencode({'id':task_id,'limit':10}))
decoded = [(chunk, base64.b64decode(chunk.get('data') or '').decode('utf-8')) for chunk in history['chunks']]
if any('X-Amz-Signature' in content for _, content in decoded):
    raise SystemExit('Internal signed download credentials leaked into browser history.')
if not any(chunk['event'] == 'task-running' and fixture['receipt'] in content for chunk, content in decoded):
    raise SystemExit('Real Agent attachment receipt was not found in the original task history.')
inputs = [json.loads(content) for chunk, content in decoded if chunk['event'] == 'user-input']
if any(fixture['receipt'] in base64.b64decode(item['content']).decode('utf-8') for item in inputs):
    raise SystemExit('Receipt was supplied in the prompt, so Agent file reading is not proved.')
selections = [(chunk['turn_seq'],json.loads(content)['attachments']) for chunk,content in decoded
              if chunk['event']=='user-input' and json.loads(content).get('attachments')]
if not any(len(selection)==2 for _,selection in selections):
    raise SystemExit('Expected the two nonempty attachments accepted by the original Web.')
index = api(owner, '/api/v1/users/tasks/user-inputs?' + urllib.parse.urlencode({'id':task_id,'limit':20}))
if len(index['items']) != len(inputs) or len({item['id'] for item in index['items']}) != len(inputs):
    raise SystemExit('User-input index lost or duplicated a round.')

sql = "SELECT e.sandbox_id FROM runtime_environments e JOIN runtime_task_intents i ON i.environment_id=e.id WHERE i.task_id='" + task_id + "'"
row = subprocess.check_output(['docker','exec','jingjiaagent-postgres-1','psql','-U','postgres','-d','jingjiaagent','-tAc',sql], text=True).strip()
sandbox = row
if not re.fullmatch('[a-f0-9]{64}', sandbox):
    raise SystemExit('Invalid original runtime mapping.')
container = 'agent-compose-' + sandbox[:12]
pointer = json.loads(subprocess.check_output(['docker','exec',container,'cat','/data/state/jingjiaagent-attachments/'+task_id+'.json']))
if args.expect_cleared and pointer['files']:
    raise SystemExit('Attachment-free round retained its old selection.')
report = {'task':task_id,'environment':environment,'model_receipt_observed':True,'unique_user_inputs':len(inputs),
          'internal_signatures_absent':True,'current_selection_cleared':not bool(pointer['files']),
          'empty_attachment_ui':'rejected_by_existing_behavior','files':[]}
expected_names = ['中文附件验收.txt','二进制附件验收.zip']
fresh_file = state/'web-attachment-fresh-fixture.json'
if fresh_file.exists():
    fresh = json.loads(fresh_file.read_text())
    expected_names.append(fresh['file'])
    if not any(chunk['event']=='task-running' and fresh['receipt'] in content for chunk,content in decoded):
        raise SystemExit('Fresh attachment receipt was not returned by the actual Agent.')
for turn,selection in selections:
  sql = "SELECT id FROM runtime_commands WHERE task_id='"+task_id+"' AND operation='task' AND turn="+str(int(turn))
  command = subprocess.check_output(['docker','exec','jingjiaagent-postgres-1','psql','-U','postgres','-d','jingjiaagent','-tAc',sql],text=True).strip()
  if str(uuid.UUID(command)) != command:
    raise SystemExit('Invalid original command mapping.')
  root = '/workspace/.jingjiaagent/attachments/' + task_id + '/' + command + '/'
  manifest = json.loads(subprocess.check_output(['docker','exec',container,'cat',root+'.manifest.json']))
  for item in selection:
    route = item['url']
    url = urllib.parse.urlsplit(route)
    if url.scheme or url.netloc or url.path != '/api/v1/assets':
        raise SystemExit('History attachment does not use the original authenticated asset route.')
    original_name = re.sub(r'-\d+$', '', pathlib.Path(item['filename']).stem) + pathlib.Path(item['filename']).suffix
    if original_name not in expected_names:
        raise SystemExit('Unexpected browser attachment filename.')
    expected = (state / 'web-attachments' / original_name).read_bytes()
    with owner.open(origin + route, timeout=20) as response:
        if response.read() != expected:
            raise SystemExit('Original asset bytes differ from the uploaded fixture.')
    for client in (outsider, anonymous):
        try:
            with client.open(origin + route, timeout=10) as response:
                raise SystemExit('Unauthorized asset access was accepted.')
        except urllib.error.HTTPError as error:
            if error.code != 401:
                raise SystemExit('Asset denial returned an unrelated error: ' + str(error.code)) from None
    installed = [file for file in manifest['files'] if pathlib.Path(file['path']).name.endswith('-' + item['filename'])]
    if len(installed) != 1 or not installed[0]['path'].startswith(root):
        raise SystemExit('Attachment is absent from its isolated command directory.')
    query = urllib.parse.urlencode({'id':environment,'path':installed[0]['path']})
    with owner.open(origin + '/api/v1/users/files/download?' + query, timeout=20) as response:
        body = response.read()
        if body != expected or response.headers.get('Content-Length') != str(len(expected)):
            raise SystemExit('Guest attachment bytes or length changed.')
    digest = hashlib.sha256(expected).hexdigest()
    if installed[0]['sha256'] != digest or installed[0]['size'] != len(expected):
        raise SystemExit('Installed attachment manifest disagrees with original bytes.')
    # The private temporary prefix must also reject unsigned direct S3 reads.
    key = urllib.parse.parse_qs(url.query)['key'][0]
    direct = 'http://127.0.0.1:47590/jingjiaagent/' + urllib.parse.quote(key)
    try:
        with anonymous.open(direct, timeout=10):
            raise SystemExit('Temporary object is anonymously readable through S3.')
    except urllib.error.HTTPError as error:
        if error.code != 403:
            raise SystemExit('Unsigned S3 access returned an unrelated error.') from None
    report['files'].append({'file':item['filename'],'bytes':len(expected),'sha256':digest,
        'asset_download':True,'guest_download':True,'outsider_denied':True,'anonymous_denied':True,'unsigned_s3_denied':True})
    if fresh_file.exists() and original_name==fresh['file']:
        sql = "SELECT EXISTS(SELECT 1 FROM runtime_events WHERE task_id='"+task_id+"' AND turn="+str(int(turn))+" AND chunk->>'event'='user-input' AND position('X-Amz-Signature' in convert_from(decode(chunk->>'data','base64'),'UTF8'))>0)"
        leaked = subprocess.check_output(['docker','exec','jingjiaagent-postgres-1','psql','-U','postgres','-d','jingjiaagent','-tAc',sql],text=True).strip()
        if leaked != 'f':
            raise SystemExit('A newly persisted user-input contains an internal signature.')
        report['new_persisted_input_is_public'] = True
(state / 'web-attachment-report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8')
print(json.dumps(report,ensure_ascii=False,indent=2))
