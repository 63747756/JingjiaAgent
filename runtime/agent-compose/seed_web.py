"""Configure the isolated Web PoC through its existing authenticated APIs.

The existing team login supplies the owner/team for administrator node
configuration. The background registry, rather than this script, registers
the host and maintains its metadata. This is an isolated setup helper.
No credentials or credential-bearing API responses are printed.
"""
import http.cookiejar
import json
import os
import pathlib
import sys
import urllib.error
import urllib.request
import uuid

root = pathlib.Path(__file__).resolve().parent
state = pathlib.Path(os.environ.get('RUNTIME_WEB_STATE_DIRECTORY',str(root / '.state'))).resolve()
if not state.is_relative_to((root / '.state').resolve()):
    raise SystemExit('Web fixture state must remain in the ignored local state directory.')
base_url = os.environ.get('RUNTIME_WEB_BASE_URL','http://127.0.0.1:47420').rstrip('/')
account = json.loads((state / 'web-account.json').read_text())
model = json.loads((state / 'model.json').read_text())
lock = json.loads((root / 'source.lock.json').read_text())
cookies = http.cookiejar.CookieJar()
client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(cookies))

def api(path, data=None, method=None, internal=False):
    headers = {'Content-Type':'application/json'}
    if internal:
        headers['Authorization'] = 'Bearer ' + (state / 'daemon.token').read_text().strip()
    req = urllib.request.Request(base_url + path,
        data=json.dumps(data).encode() if data is not None else None,
        method=method or ('POST' if data is not None else 'GET'), headers=headers)
    try:
        with client.open(req, timeout=60) as response:
            result = json.load(response)
    except urllib.error.HTTPError as error:
        raise SystemExit(f'{path}: HTTP {error.code}') from None
    if result.get('code') != 0:
        raise SystemExit(f'{path}: business error {result.get("code")}')
    return result.get('data')

admin = api('/api/v1/teams/users/login', account)
node_file = state / 'web-node.json'
if not node_file.exists():
    node_file.write_text(json.dumps({'id':str(uuid.uuid4())},indent=2),encoding='utf-8')
node = json.loads(node_file.read_text())
if not admin.get('team', {}).get('id'):
    raise SystemExit('Original team login did not return the owner/team required for node enrollment.')
for key, value in [('owner_id',admin['id']),('team_id',admin['team']['id'])]:
    if node.get(key) and node[key] != value:
        raise SystemExit('Existing isolated node ownership does not match the current team login.')
    node[key] = value
node_file.write_text(json.dumps(node,indent=2),encoding='utf-8')
node_id = node['id']
models = api('/api/v1/teams/models')['models'] or []
matches = [m for m in models if m['model']==model['model'] and m['base_url']==model['base_url'] and m['interface_type']=='openai_chat']
if matches:
    model_id = matches[0]['id']
else:
    saved = api('/api/v1/teams/models', {'provider':'DeepSeek','api_key':model['api_key'],
        'base_url':model['base_url'],'model':model['model'],'interface_type':'openai_chat',
        'temperature':0.2,'remark':'Isolated remote runtime acceptance model','support_image':False})
    model_id = saved['id']
fixture = {'node_id':node_id,'model_id':model_id}
images = json.loads((state / 'images.json').read_text())
available_images = api('/api/v1/teams/images')['images'] or []
matching_images = [entry for entry in available_images if entry['name'] == images['guest']]
if matching_images:
    fixture['image_id'] = matching_images[0]['id']
    image_remark = 'Remote runtime compatibility p' + str(lock.get('guest_patch_revision',lock['patch_revision']))
    if matching_images[0].get('remark') != image_remark:
        api('/api/v1/teams/images/' + fixture['image_id'], {'remark':image_remark}, method='PUT')
else:
    saved_image = api('/api/v1/teams/images', {'name':images['guest'],
        'remark':'Remote runtime compatibility p' + str(lock.get('guest_patch_revision',lock['patch_revision'])), 'group_ids':[]})
    fixture['image_id'] = saved_image['id']
if '--switch-fixture' in sys.argv:
    remark = 'Isolated model-switch acceptance target'
    targets = [entry for entry in models if entry.get('remark') == remark]
    if targets:
        target_id = targets[0]['id']
    else:
        saved = api('/api/v1/teams/models', {'provider':'DeepSeek','api_key':model['api_key'],
            'base_url':model['base_url'],'model':model['model'],'interface_type':'openai_chat',
            'temperature':0.3,'remark':remark,'support_image':False})
        target_id = saved['id']
    fixture['switch_model_id'] = target_id
(state / 'web-fixture.json').write_text(json.dumps(fixture,indent=2))
print('Prepared administrator node enrollment and configured the model/image through existing APIs.')
if os.environ.get('RUNTIME_WEB_PROJECT'):
    print('Node ownership saved; continue with this deployment entry point to reload backend configuration.')
elif state == (root / '.state/linux-web').resolve():
    print('Re-run prepare_linux_web.py and recreate the isolated backend and web services; the background heartbeat performs registration.')
else:
    print('Re-run prepare_web.py and restart only the PoC backend; the background heartbeat performs registration.')
