"""Original Skill APIs/control plus real Guest version/reload acceptance.

Plugin management is absent from this fork's Web: the explicitly named plugin
is a SQL registration fixture, not evidence for an admin authoring flow. Its ZIP
is uploaded with the original authenticated uploader. No credentials are logged.
Only this script's group/resources/task may be changed or stopped.
"""
import argparse
import base64
import concurrent.futures
import datetime
import hashlib
import hmac
import http.cookiejar
import io
import json
import pathlib
import secrets
import socket
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
import zipfile

from preview_fixture import frame, read_frame

state = pathlib.Path(__file__).resolve().parent / '.state'
origin = 'http://127.0.0.1:47420'
skill_name, plugin_name = 'web-version-acceptance', 'web-version-plugin'
group_name = 'Remote Skill version acceptance'
record_file, report_file = state / 'web-resource-versions.json', state / 'web-resource-versions-report.json'
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('action', choices=['prepare', 'run', 'verify-v1', 'update-v2', 'reload', 'verify-v2', 'body-and-concurrency', 'verify-immutable', 'verify-latest', 'verify-skill-clear', 'clear', 'verify-clear', 'verify-agent-clear', 'finish'])
args = parser.parse_args()


def sql(query):
    return subprocess.check_output(['docker', 'exec', 'jingjia-runtime-tests-20261002', 'psql', '-v', 'ON_ERROR_STOP=1',
        '-U', 'postgres', '-d', 'monkeycode_web_poc', '-tAc', query], encoding='utf-8').strip()


def api(client, route, data=None, method=None, raw=None, headers=None):
    request = urllib.request.Request(origin + route, data=raw if raw is not None else (json.dumps(data).encode() if data is not None else None),
        method=method, headers=headers or {'Content-Type': 'application/json'})
    try:
        with client.open(request, timeout=30) as response:
            result = json.load(response)
            if result.get('code') != 0:
                raise SystemExit('Original resource API rejected ' + route + ' (code ' + str(result.get('code')) + ')')
            return result.get('data')
    except urllib.error.HTTPError as error:
        raise SystemExit('Original resource API failed ' + route + ' (HTTP ' + str(error.code) + ')') from None


def login(file, team=False):
    jar = http.cookiejar.CookieJar()
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))
    user = api(client, '/api/v1/teams/users/login' if team else '/api/v1/users/password-login', json.loads((state / file).read_text()))
    return client, str(uuid.UUID(user['id'])), jar


admin, admin_id, _ = login('web-account.json', True)
owner, owner_id, _ = login('web-account.json')
member, member_id, member_jar = login('web-member-account.json')
outsider, _, _ = login('web-outsider-account.json')
record = json.loads(record_file.read_text()) if record_file.exists() else {}
report = json.loads(report_file.read_text()) if report_file.exists() else {}


def save():
    record_file.write_text(json.dumps(record, ensure_ascii=False, indent=2), encoding='utf-8')
    report_file.write_text(json.dumps(report, indent=2), encoding='utf-8')


def archive(entries):
    output = io.BytesIO()
    with zipfile.ZipFile(output, 'w') as z:
        for name, content in entries.items():
            z.writestr(name, content)
    return output.getvalue()


def multipart(client, route, fields, filename, body):
    boundary = 'fixture-' + uuid.uuid4().hex
    raw = b''
    for key, value in fields.items():
        raw += ('--' + boundary + '\r\nContent-Disposition: form-data; name="' + key + '"\r\n\r\n' + value + '\r\n').encode()
    raw += ('--' + boundary + '\r\nContent-Disposition: form-data; name="file"; filename="' + filename + '"\r\nContent-Type: application/zip\r\n\r\n').encode()
    raw += body + ('\r\n--' + boundary + '--\r\n').encode()
    return api(client, route, raw=raw, headers={'Content-Type': 'multipart/form-data; boundary=' + boundary})


def skill_entries(version):
    body = '---\nname: ' + skill_name + '\ndescription: Return the current Chinese receipt when requested.\n---\nRead references/中文回执.txt in this skill directory and return its content.\n'
    entries = {skill_name + '/SKILL.md': body, skill_name + '/references/中文回执.txt': record['skill_' + version] + '\n'}
    if version == 'v1':
        entries[skill_name + '/references/obsolete.txt'] = 'Must disappear in v2.\n'
    return entries


def upload_skill(version, groups=None):
    fields = {'name': skill_name, 'description': '正文版本及授权验收 ' + version, 'source_type': 'zip', 'source_label': version + '.zip'}
    if groups is not None:
        fields['group_ids'] = json.dumps(groups)
    return multipart(admin, '/api/v1/teams/skills/package', fields, version + '.zip', archive(skill_entries(version)))


def guard():
    group_id, skill_id = str(uuid.UUID(record['group'])), str(uuid.UUID(record['skill']))
    groups = api(admin, '/api/v1/teams/groups')['groups']
    if not any(g['id'] == group_id and g['name'] == group_name for g in groups):
        raise SystemExit('Disposable version group differs; preserve it.')
    membership = set(sql("SELECT user_id FROM team_group_members WHERE group_id='" + group_id + "'").splitlines())
    if membership - {owner_id, member_id}:
        raise SystemExit('Unexpected members in version fixture group.')
    items = api(admin, '/api/v1/teams/skills')['skills']
    matches = [s for s in items if s['id'] == skill_id and s['name'] == skill_name]
    if len(matches) != 1 or [g['id'] for g in matches[0]['groups']] != [group_id]:
        raise SystemExit('Version fixture resource or its authorization differs.')
    return matches[0]


def register_plugin(version):
    # No external dependencies, networking, credentials or host access. A
    # private unpredictable tool receipt and an execution-only audit file
    # distinguish actual plugin execution from reading a prompt/source file.
    receipt = record['plugin_' + version]
    body = 'import fs from "node:fs"; export const VersionProof = async () => ({ tool: { plugin_version_receipt: { description: "Return the current plugin acceptance receipt.", args: {}, execute: async () => { const receipt = ' + json.dumps(receipt) + '; fs.appendFileSync("/workspace/web-version-plugin-audit.txt", receipt + "\\n"); return receipt; } } } });\n'
    entries = {'main.js': body}
    if version == 'v1':
        entries['obsolete.txt'] = 'Must disappear in v2.\n'
    access = multipart(owner, '/api/v1/uploader', {'usage': 'spec'}, 'plugin-' + version + '.zip', archive(entries))
    key = urllib.parse.parse_qs(urllib.parse.urlsplit(access).query)['key'][0]
    if not key.endswith('.zip') or owner_id not in key or "'" in key:
        raise SystemExit('Original uploader returned an unexpected fixture key.')
    team_id = sql("SELECT team_id FROM team_members WHERE user_id='" + member_id + "'")
    team_id = str(uuid.UUID(team_id))
    repo_id = str(uuid.UUID(sql("SELECT id FROM agent_plugin_repos WHERE scope_type='team' AND scope_id='" + team_id + "' AND source_type='bare' AND is_deleted=false")))
    if 'plugin' not in record:
        existing = sql("SELECT id FROM agent_plugins WHERE name='" + plugin_name + "' AND repo_id='" + repo_id + "' AND is_deleted=false")
        if existing:
            raise SystemExit('Unowned same-name plugin fixture exists; do not overwrite.')
        record['plugin'] = str(uuid.uuid4())
        save()
    plugin_id = str(uuid.UUID(record['plugin']))
    known = sql("SELECT count(*) FROM agent_plugins WHERE id='" + plugin_id + "' AND name='" + plugin_name + "' AND repo_id='" + repo_id + "'")
    if known == '0' and version != 'v1':
        raise SystemExit('Plugin registration fixture is missing.')
    ver_id = str(uuid.uuid4())
    create = ("INSERT INTO agent_plugins(id,name,scope_type,scope_id,repo_id,created_by,description) VALUES ('" + plugin_id + "','" + plugin_name + "','team','" + team_id + "','" + repo_id + "','" + admin_id + "','Disposable resource version acceptance');") if known == '0' else ''
    sql("BEGIN;" + create + "INSERT INTO agent_plugin_versions(id,resource_id,version,s3_key,parsed_meta) VALUES ('" + ver_id + "','" + plugin_id + "','" + version + "','" + key + "','{\"entry\":\"main.js\"}'); UPDATE agent_plugins SET active_version_id='" + ver_id + "',enabled=true WHERE id='" + plugin_id + "'; COMMIT;")
    record['plugin_version'] = version


def guest():
    sandbox = sql("SELECT e.sandbox_id FROM runtime_environments e JOIN runtime_task_intents i ON i.environment_id=e.id WHERE i.task_id='" + str(uuid.UUID(record['task'])) + "'")
    if len(sandbox) != 64 or any(c not in '0123456789abcdef' for c in sandbox):
        raise SystemExit('Original mapped Guest not yet available.')
    return 'agent-compose-' + sandbox[:12]


def guest_read(path):
    return subprocess.check_output(['docker', 'exec', guest(), 'cat', path], encoding='utf-8').strip()


def switch(skills, plugins):
    task_id = str(uuid.UUID(record['task']))
    session = sql("SELECT session_id FROM runtime_task_sessions WHERE task_id='" + task_id + "'")
    if not session:
        raise SystemExit('Actual provider session not yet recorded.')
    cookie = '; '.join(c.name + '=' + c.value for c in member_jar)
    connection = socket.create_connection(('127.0.0.1', 47420), timeout=30)
    key = base64.b64encode(secrets.token_bytes(16)).decode()
    connection.sendall(('GET /api/v1/users/tasks/control?id=' + task_id + ' HTTP/1.1\r\nHost: 127.0.0.1:47420\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: ' + key + '\r\nCookie: ' + cookie + '\r\n\r\n').encode())
    incoming = connection.makefile('rb')
    header = incoming.readline()
    if b' 101 ' not in header:
        raise SystemExit('Original control WebSocket refused.')
    for _ in range(100):
        line = incoming.readline()
        if not line:
            raise SystemExit('Original control connection ended before handshake.')
        if line == b'\r\n':
            break
    else:
        raise SystemExit('Original control handshake exceeds fixture limit.')
    request_id = uuid.uuid4().hex
    request = {'request_id': request_id, 'skill_ids': skills, 'plugin_ids': plugins}
    connection.sendall(frame(1, json.dumps({'type': 'call', 'kind': 'switch_agent_resources', 'data': base64.b64encode(json.dumps(request).encode()).decode()}).encode(), secrets.token_bytes(4)))
    deadline = time.monotonic() + 30
    try:
        while time.monotonic() < deadline:
            opcode, raw = read_frame(incoming)
            if opcode == 9:
                connection.sendall(frame(10, raw, secrets.token_bytes(4)))
            if opcode != 1:
                continue
            response = json.loads(raw)
            if response.get('kind') == 'switch_agent_resources':
                result = response['data']
                result = json.loads(base64.b64decode(result)) if isinstance(result, str) else result
                if result.get('request_id') == request_id:
                    if not result.get('success'):
                        raise SystemExit('Original resource reload rejected.')
                    break
        else:
            raise SystemExit('Original resource reload did not acknowledge.')
    finally:
        incoming.close()
        connection.close()
    if sql("SELECT session_id FROM runtime_task_sessions WHERE task_id='" + task_id + "'") != session:
        raise SystemExit('Resource reload changed the existing provider session.')
    return session


def s3_get(key):
    # Private fixture read for immutable version comparison, not a product
    # authorization test. Never log credentials or the Authorization header.
    config = json.loads((state / 'web-storage-config.json').read_text())
    now = datetime.datetime.now(datetime.timezone.utc)
    date, stamp = now.strftime('%Y%m%d'), now.strftime('%Y%m%dT%H%M%SZ')
    host = urllib.parse.urlsplit(config['endpoint']).netloc
    path = '/' + config['bucket'] + '/' + urllib.parse.quote(key, safe='/')
    empty_hash = hashlib.sha256(b'').hexdigest()
    headers = 'host:' + host + '\nx-amz-content-sha256:' + empty_hash + '\nx-amz-date:' + stamp + '\n'
    signed = 'host;x-amz-content-sha256;x-amz-date'
    canonical = 'GET\n' + path + '\n\n' + headers + '\n' + signed + '\n' + empty_hash
    scope = date + '/' + config['region'] + '/s3/aws4_request'
    signing = ('AWS4' + config['access_key_secret']).encode()
    for item in [date, config['region'], 's3', 'aws4_request']:
        signing = hmac.new(signing, item.encode(), hashlib.sha256).digest()
    signature = hmac.new(signing, ('AWS4-HMAC-SHA256\n' + stamp + '\n' + scope + '\n' + hashlib.sha256(canonical.encode()).hexdigest()).encode(), hashlib.sha256).hexdigest()
    request = urllib.request.Request(config['endpoint'] + path, headers={'x-amz-date': stamp, 'x-amz-content-sha256': empty_hash,
        'Authorization': 'AWS4-HMAC-SHA256 Credential=' + config['access_key'] + '/' + scope + ', SignedHeaders=' + signed + ', Signature=' + signature})
    try:
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=15) as response:
            return response.read()
    except urllib.error.HTTPError as error:
        raise SystemExit('Private fixture object comparison failed: HTTP ' + str(error.code)) from None


if args.action == 'prepare':
    if not record:
        matches = [g for g in api(admin, '/api/v1/teams/groups')['groups'] if g['name'] == group_name]
        if matches:
            raise SystemExit('Unowned same-name version group exists; preserve it.')
        group = api(admin, '/api/v1/teams/groups', {'name': group_name})
        record = {'group': str(uuid.UUID(group['id'])), **{name: name.upper() + '_' + secrets.token_hex(12)
            for name in ['skill_v1', 'skill_v2', 'skill_body', 'skill_concurrent_a', 'skill_concurrent_b', 'plugin_v1', 'plugin_v2']}}
        save()
        api(admin, '/api/v1/teams/groups/' + record['group'] + '/users', {'user_ids': [owner_id, member_id]}, 'PUT')
        if any(s['name'] == skill_name for s in api(admin, '/api/v1/teams/skills')['skills']):
            raise SystemExit('Unowned same-name Skill exists; preserve it.')
        resource = upload_skill('v1', [record['group']])
        record['skill'], record['original_version_key'] = str(uuid.UUID(resource['id'])), resource['s3_key']
        save()
    guard()
    if 'plugin_version' not in record:
        register_plugin('v1')
    report['plugin_source'] = 'SQL registration fixture; original authenticated ZIP uploader'
    report['original_skill_package_upload'] = True
elif args.action == 'run':
    guard()
    for endpoint, name, rid in [('/api/v1/skills', skill_name, record['skill']), ('/api/v1/plugins', plugin_name, record['plugin'])]:
        if not any(s['id'] == rid and s['name'] == name for s in api(member, endpoint)) or any(s['id'] == rid for s in api(outsider, endpoint)):
            raise SystemExit('Original resource listing scope differs.')
    prompt = '版本验收 v1：调用 web-version-acceptance 技能和 plugin_version_receipt 工具，返回各自当前回执。不要读取插件源码。'
    existing = sql("SELECT id FROM tasks WHERE user_id='" + member_id + "' AND content='" + prompt + "'").splitlines()
    if len(existing) > 1:
        raise SystemExit('Conflicting version tasks; do not replay.')
    if not existing:
        config = json.loads((state / 'web-fixture.json').read_text())
        task = api(member, '/api/v1/users/tasks', {'content': prompt, 'host_id': config['node_id'], 'image_id': config['image_id'],
            'model_id': config['model_id'], 'cli_name': 'opencode', 'task_type': 'develop', 'resource': {'core': 1, 'memory': 2*1024**3, 'life': 3600},
            'extra': {'skill_ids': [record['skill']], 'plugin_ids': [record['plugin']]}})
        existing = [task['id']]
    record['task'] = str(uuid.UUID(existing[0]))
    report['original_resource_selection_and_team_scope'] = True
elif args.action == 'update-v2':
    before = guard()
    if before['active_version'] != 'v1':
        raise SystemExit('Only the recorded v1 fixture may advance to v2.')
    result = upload_skill('v2') # deliberately omit group_ids
    if result['id'] != record['skill'] or result['active_version'] != 'v2' or [g['id'] for g in result['groups']] != [record['group']] or result['description'] != '正文版本及授权验收 v2':
        raise SystemExit('Re-upload did not atomically update version/metadata and preserve grants.')
    original = zipfile.ZipFile(io.BytesIO(s3_get(record['original_version_key'])))
    if original.read(skill_name + '/references/中文回执.txt') != (record['skill_v1'] + '\n').encode():
        raise SystemExit('Old immutable ZIP was overwritten.')
    register_plugin('v2')
    report['skill_reupload_preserved_grants_and_old_bytes'] = True
elif args.action in ['reload', 'clear']:
    guard()
    clearing = args.action == 'clear'
    if clearing:
        record['plugin_audit_before_clear'] = guest_read('/workspace/web-version-plugin-audit.txt').splitlines()
    record['session'] = switch([] if clearing else [record['skill']], [] if clearing else [record['plugin']])
    task = api(member, '/api/v1/users/tasks/' + str(uuid.UUID(record['task'])))
    expected = {'skill_ids': [] if clearing else [record['skill']], 'plugin_ids': [] if clearing else [record['plugin']]}
    if any((task.get('extra') or {}).get(key, []) != value for key, value in expected.items()):
        raise SystemExit('Original resource selection was not persisted.')
    report['original_control_' + args.action + '_same_provider_session'] = True
elif args.action in ['verify-v1', 'verify-v2']:
    version = args.action.removeprefix('verify-')
    history = api(member, '/api/v1/users/tasks/rounds?id=' + record['task'] + '&limit=10')['chunks']
    texts = []
    for chunk in history:
        body = base64.b64decode(chunk.get('data') or '').decode('utf-8', errors='replace')
        if chunk['event'] == 'task-running':
            texts.append(body)
        if chunk['event'] == 'user-input':
            prompt = base64.b64decode(json.loads(body)['content']).decode('utf-8', errors='replace')
            if any(receipt in prompt for key, receipt in record.items() if isinstance(receipt, str) and key.startswith(('skill_', 'plugin_')) and key not in ['plugin_version']):
                raise SystemExit('A resource receipt was supplied in the prompt.')
    text = '\n'.join(texts)
    if record['skill_' + version] not in text or record['plugin_' + version] not in text or 'plugin_version_receipt' not in text:
        raise SystemExit('Actual Agent has not returned both current resource receipts.')
    base = '/root/.codingmatrix/project-tpl/.ai-ready/'
    if guest_read(base + 'skills/' + skill_name + '/references/中文回执.txt') != record['skill_' + version]:
        raise SystemExit('Guest Skill differs from current version.')
    if record['plugin_' + version] not in guest_read('/workspace/web-version-plugin-audit.txt').splitlines():
        raise SystemExit('Receipt exists in history without actual plugin tool execution.')
    if version == 'v2':
        for path in [base + 'skills/' + skill_name + '/references/obsolete.txt', base + 'plugins/' + plugin_name + '/obsolete.txt']:
            if subprocess.run(['docker', 'exec', guest(), 'test', '-e', path]).returncode != 1:
                raise SystemExit('Resource reload retained obsolete files or Guest unavailable.')
    report['actual_agent_' + version + '_skill_and_plugin_execution'] = True
elif args.action == 'body-and-concurrency':
    before = guard()
    if before['active_version'] != 'v2':
        raise SystemExit('Only the recorded v2 fixture may advance through the concurrency probe; inspect before retrying.')
    def content(receipt):
        return '---\nname: ' + skill_name + '\ndescription: Current version receipt.\n---\nReturn exactly this receipt when explicitly asked: ' + receipt + '.\n'
    result = api(admin, '/api/v1/teams/skills/' + record['skill'], {'content': content(record['skill_body'])}, 'PUT')
    if result['active_version'] != 'v3' or result['description'] != before['description'] or [g['id'] for g in result['groups']] != [record['group']]:
        raise SystemExit('Body-only update widened grants or changed omitted metadata.')
    def publish(which):
        client, _, _ = login('web-account.json', True)
        return api(client, '/api/v1/teams/skills/' + record['skill'], {'content': content(record['skill_concurrent_' + which])}, 'PUT')
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        list(pool.map(publish, ['a', 'b']))
    versions = json.loads(sql("SELECT coalesce(json_agg(json_build_object('version',version,'s3_key',s3_key)),'[]') FROM agent_skill_versions WHERE resource_id='" + str(uuid.UUID(record['skill'])) + "'"))
    if {v['version'] for v in versions} != {'v1', 'v2', 'v3', 'v4', 'v5'} or len({v['s3_key'] for v in versions}) != 5:
        raise SystemExit('Concurrent uploads duplicated versions or object keys.')
    actual = set()
    for v in versions:
        if v['version'] in ['v4', 'v5']:
            z = zipfile.ZipFile(io.BytesIO(s3_get(v['s3_key'])))
            actual.add(z.read('SKILL.md').decode())
    if actual != {content(record['skill_concurrent_a']), content(record['skill_concurrent_b'])} or guard()['active_version'] != 'v5':
        raise SystemExit('Concurrent committed versions lost or mixed package bytes.')
    report['original_content_only_update_preserved_grants'] = True
    report['original_concurrent_uploads_unique_versions_and_exact_bytes'] = True
elif args.action == 'verify-immutable':
    guard()
    z = zipfile.ZipFile(io.BytesIO(s3_get(record['original_version_key'])))
    expected = skill_entries('v1')
    if set(z.namelist()) != set(expected) or any(z.read(name) != value.encode() for name, value in expected.items()):
        raise SystemExit('Committed v1 archive entries/bytes changed after later publications.')
    report['original_v1_archive_all_entries_exact_bytes_preserved'] = True
elif args.action == 'verify-latest':
    latest = guard()
    if latest['active_version'] != 'v5':
        raise SystemExit('Latest accepted original business version differs.')
    z = zipfile.ZipFile(io.BytesIO(s3_get(latest['s3_key'])))
    expected = z.read('SKILL.md').decode().strip()
    installed = guest_read('/root/.codingmatrix/project-tpl/.ai-ready/skills/' + skill_name + '/SKILL.md')
    if expected != installed or not any(record['skill_concurrent_' + name] in installed for name in ['a', 'b']):
        raise SystemExit('Original Web reload did not install exact latest committed bytes.')
    task = api(member, '/api/v1/users/tasks/' + record['task'])
    if task['extra'].get('skill_ids') != [record['skill']] or task['extra'].get('plugin_ids') != [record['plugin']]:
        raise SystemExit('Original Skill dialog lost the existing plugin selection.')
    if sql("SELECT session_id FROM runtime_task_sessions WHERE task_id='" + record['task'] + "'") != record['session']:
        raise SystemExit('Original Web reload changed the provider session.')
    report['original_web_reload_latest_bytes_preserved_plugin_and_session'] = True
elif args.action == 'verify-skill-clear':
    task = api(member, '/api/v1/users/tasks/' + record['task'])
    base = '/root/.codingmatrix/project-tpl/.ai-ready/'
    if task['extra'].get('skill_ids') != [] or task['extra'].get('plugin_ids') != [record['plugin']]:
        raise SystemExit('Original Skill cancellation changed unrelated plugin selection.')
    if subprocess.run(['docker', 'exec', guest(), 'test', '-e', base + 'skills/' + skill_name]).returncode != 1:
        raise SystemExit('Original Web deselected Skill remains installed.')
    if subprocess.run(['docker', 'exec', guest(), 'test', '-e', base + 'plugins/' + plugin_name + '/main.js']).returncode != 0:
        raise SystemExit('Original Skill cancellation removed the retained plugin.')
    report['original_web_skill_deselection_preserved_plugin'] = True
elif args.action == 'verify-clear':
    base = '/root/.codingmatrix/project-tpl/.ai-ready/'
    for path in [base + 'skills/' + skill_name, base + 'plugins/' + plugin_name]:
        if subprocess.run(['docker', 'exec', guest(), 'test', '-e', path]).returncode != 1:
            raise SystemExit('Deselected resource still installed or Guest unavailable.')
    config = json.loads(guest_read('/root/.config/opencode/opencode.json'))
    if config.get('plugin') != []:
        raise SystemExit('Deselected plugin still configured.')
    report['explicit_deselection_removed_directories_and_plugin_config'] = True
elif args.action == 'verify-agent-clear':
    history = api(member, '/api/v1/users/tasks/rounds?id=' + record['task'] + '&limit=10')['chunks']
    turns = []
    for chunk in history:
        if chunk['event'] == 'user-input':
            incoming = json.loads(base64.b64decode(chunk['data']))
            prompt = base64.b64decode(incoming['content']).decode('utf-8')
            if prompt.startswith('取消选择验收：'):
                turns.append(chunk['turn_seq'])
    if len(set(turns)) != 1:
        raise SystemExit('Submit one original Web deselection acceptance prompt; do not replay it.')
    text = '\n'.join(base64.b64decode(chunk['data']).decode('utf-8') for chunk in history
        if chunk['event'] == 'task-running' and chunk['turn_seq'] == turns[0])
    if 'RESOURCE_SELECTION_CLEARED' not in text or guest_read('/workspace/web-version-plugin-audit.txt').splitlines() != record['plugin_audit_before_clear']:
        raise SystemExit('Deselected plugin absence is not yet confirmed by actual Agent and unchanged execution audit.')
    report['actual_agent_after_clear_no_plugin_execution'] = True
elif args.action == 'finish':
    if not all(report.get(key) for key in ['actual_agent_v2_skill_and_plugin_execution', 'explicit_deselection_removed_directories_and_plugin_config',
        'original_web_reload_latest_bytes_preserved_plugin_and_session', 'actual_agent_after_clear_no_plugin_execution']):
        raise SystemExit('Real acceptance unfinished; do not stop its evidence environment.')
    guard()
    task = api(member, '/api/v1/users/tasks/' + record['task'])
    if not task['content'].startswith('版本验收 v1：'):
        raise SystemExit('Only this disposable task may be finished.')
    if task['status'] == 'processing':
        api(member, '/api/v1/users/tasks/stop', {'id': record['task']}, 'PUT')
    api(admin, '/api/v1/teams/groups/' + record['group'] + '/users', {'user_ids': [owner_id]}, 'PUT')
    sql("UPDATE agent_plugins SET enabled=false WHERE id='" + str(uuid.UUID(record['plugin'])) + "' AND name='" + plugin_name + "'")
    report['finished_disposable_task_revoked_member_and_disabled_fixture_plugin'] = True
save()
print(json.dumps({'action': args.action, 'task': record.get('task'), 'passed': True}, ensure_ascii=False))
