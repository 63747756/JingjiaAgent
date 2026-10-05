"""Original local APIs + actual Guest resource acceptance, with reusable fixtures.

Skill ZIP and rule packages are imported through the original Web, not SQL.
Only the named disposable group is changed. Credentials stay in .state.
Existing tasks are inspected rather than resubmitted on reruns.
"""
import argparse
import base64
import hashlib
import http.cookiejar
import json
import pathlib
import secrets
import socket
import subprocess
import time
import urllib.error
import urllib.request
import uuid
import zipfile

from preview_fixture import frame, read_frame

from linux_web_common import state
origin = 'http://127.0.0.1:47424'
group_name = 'Remote Skill authorization acceptance'
skill_name = 'web-group-acceptance'
record_path = state / 'web-resource-fixture.json'
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('action', choices=['prepare', 'denied', 'grant', 'revoke', 'run-v1', 'run-v2', 'run-empty', 'verify-v1', 'verify-v2', 'verify-empty', 'finish-v1', 'finish-v2', 'finish-empty', 'switch-revoked', 'check-web-save'])
args = parser.parse_args()


def sql(query):
    return subprocess.check_output(['docker', 'exec', 'jingjiaagent-postgres-1', 'psql', '-v', 'ON_ERROR_STOP=1',
        '-U', 'postgres', '-d', 'jingjiaagent', '-tAc', query], text=True, encoding='utf-8').strip()


def api(client, route, data=None, method=None):
    req = urllib.request.Request(origin + route, data=json.dumps(data).encode() if data is not None else None,
        headers={'Content-Type': 'application/json'}, method=method)
    try:
        with client.open(req, timeout=30) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as response:
        try:
            value = json.load(response)
        except (ValueError, UnicodeError):
            value = None
        return response.code, value


def success(client, route, data=None, method=None):
    status, result = api(client, route, data, method)
    if status != 200 or result.get('code') != 0:
        raise SystemExit('Original resource API failed: ' + route + ' (HTTP ' + str(status) + ', code ' + str((result or {}).get('code')) + ')')
    return result.get('data')


def login(name, team=False):
    jar = http.cookiejar.CookieJar()
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))
    user = success(client, '/api/v1/teams/users/login' if team else '/api/v1/users/password-login', json.loads((state / name).read_text()))
    return client, str(uuid.UUID(user['id'])), jar


admin, admin_id, _ = login('web-account.json', True)
owner, owner_id, _ = login('web-account.json')
member, member_id, member_jar = login('web-member-account.json')
outsider, _, _ = login('web-outsider-account.json')
record = json.loads(record_path.read_text(encoding='utf-8')) if record_path.exists() else {}
report_path = state / 'web-resource-report.json'
report = json.loads(report_path.read_text()) if report_path.exists() else {}


def save():
    record_path.write_text(json.dumps(record, ensure_ascii=False, indent=2), encoding='utf-8')
    report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf-8')


def check_group():
    groups = success(admin, '/api/v1/teams/groups')['groups']
    matches = [g for g in groups if g['id'] == record['group'] and g['name'] == group_name]
    if len(matches) != 1:
        raise SystemExit('Only the named resource acceptance group may be changed.')
    existing = sql("SELECT user_id FROM team_group_members WHERE group_id='" + str(uuid.UUID(record['group'])) + "'").splitlines()
    if set(existing) - {owner_id, member_id}:
        raise SystemExit('Unexpected members in disposable resource group; preserve it.')


def skill():
    matches = [s for s in success(admin, '/api/v1/teams/skills')['skills'] if s['name'] == skill_name]
    if len(matches) != 1 or [g['id'] for g in matches[0]['groups']] != [record['group']]:
        raise SystemExit('Upload the dedicated Skill ZIP and bind only its acceptance group in the original Web.')
    return str(uuid.UUID(matches[0]['id']))


def listings(expected_member):
    resource_id = skill()
    for client, expected in [(owner, True), (member, expected_member), (outsider, False)]:
        items = success(client, '/api/v1/skills')
        if any(s['id'] == resource_id for s in items) != expected:
            raise SystemExit('Skill group visibility differs from original grant.')
    return resource_id


def container_for(task_id):
    sandbox = sql("SELECT e.sandbox_id FROM runtime_environments e JOIN runtime_task_intents i ON i.environment_id=e.id WHERE i.task_id='" + str(uuid.UUID(task_id)) + "'")
    if len(sandbox) != 64 or any(c not in '0123456789abcdef' for c in sandbox):
        raise SystemExit('Task does not yet have its original mapped sandbox.')
    return 'agent-compose-' + sandbox[:12]


def run_in_guest(container, *command):
    return subprocess.check_output(['docker', 'exec', container, *command], encoding='utf-8').strip()


if args.action == 'prepare':
    if not record:
        matches = [g for g in success(admin, '/api/v1/teams/groups')['groups'] if g['name'] == group_name]
        if len(matches) > 1:
            raise SystemExit('Conflicting resource groups; do not duplicate.')
        group = matches[0] if matches else success(admin, '/api/v1/teams/groups', {'name': group_name})
        record = {'group': str(uuid.UUID(group['id'])), 'skill_name': skill_name, 'rule_name': 'web-resource-rule',
            'skill_receipt': '中文技能回执_' + secrets.token_hex(12),
            'rule_v1': 'RULE_V1_' + secrets.token_hex(12), 'rule_v2': 'RULE_V2_' + secrets.token_hex(12)}
        save()
    check_group()
    success(admin, '/api/v1/teams/groups/' + record['group'] + '/users', {'user_ids': [owner_id]}, 'PUT')
    directory = state / 'web-resource'
    directory.mkdir(exist_ok=True)
    markdown = '---\nname: ' + skill_name + '\ndescription: Read the dedicated Chinese acceptance receipt when explicitly requested.\n---\nRead references/中文回执.txt in this skill directory and return its exact content.\n'
    with zipfile.ZipFile(directory / 'group-skill.zip', 'w') as archive:
        archive.writestr(skill_name + '/SKILL.md', markdown)
        archive.writestr(skill_name + '/references/中文回执.txt', record['skill_receipt'] + '\n')
    for version in ['v1', 'v2', 'empty']:
        rules = [] if version == 'empty' else [{'rule_id': 'proof', 'name': record['rule_name'], 'path': 'rules/proof.md'}]
        with zipfile.ZipFile(directory / ('rules-' + version + '.zip'), 'w') as archive:
            archive.writestr('manifest.json', json.dumps({'package_id': 'web-resource-rule-acceptance', 'version': version, 'rules': rules}))
            if rules:
                archive.writestr('rules/proof.md', 'When the user requests JINGJIAAGENT_WEB_RULE_CHECK, include exactly this rule receipt in the answer: ' + record['rule_' + version] + '.\n')
    report['original_api_disposable_group_created'] = True
elif args.action in ['grant', 'revoke']:
    check_group()
    enabled = args.action == 'grant'
    success(admin, '/api/v1/teams/groups/' + record['group'] + '/users', {'user_ids': [owner_id, member_id] if enabled else [owner_id]}, 'PUT')
    listings(enabled)
    report['original_group_' + args.action + '_visibility'] = True
elif args.action == 'denied':
    listings(False)
    foreign = json.loads((state / 'web-member-scope.json').read_text())
    resource_id = skill()
    before = success(admin, '/api/v1/teams/skills')
    old_version = sql("SELECT active_version_id FROM agent_skills WHERE id='" + resource_id + "'")
    old_count = sql("SELECT count(*) FROM agent_skill_versions WHERE resource_id='" + resource_id + "'")
    valid_content = '---\nname: ' + skill_name + '\ndescription: Valid content for rejected grant probe.\n---\nReturn a test receipt.\n'
    for body in [{'description': 'INVALID MUST NOT BE SAVED', 'group_ids': [foreign['group']]},
                 {'description': 'INVALID MUST NOT BE SAVED', 'content': valid_content, 'group_ids': [foreign['group']]}]:
        status, result = api(admin, '/api/v1/teams/skills/' + resource_id, body, 'PUT')
        if not (status == 400 or (status == 200 and result and result.get('code') != 0)):
            raise SystemExit('Foreign Skill group mutation was accepted.')
        if (success(admin, '/api/v1/teams/skills') != before or
            sql("SELECT active_version_id FROM agent_skills WHERE id='" + resource_id + "'") != old_version or
            sql("SELECT count(*) FROM agent_skill_versions WHERE resource_id='" + resource_id + "'") != old_count):
            raise SystemExit('Rejected foreign grant changed Skill metadata or version.')
    for actor in [member, outsider]:
        status, _ = api(actor, '/api/v1/teams/skills/' + resource_id, {'description': 'denied'}, 'PUT')
        if status not in [401, 403]:
            raise SystemExit('Ordinary user modified administrator Skill API.')
    report.update({'ungrouped_member_hidden': True, 'outside_team_hidden': True,
        'foreign_group_update_rejected_without_metadata_or_version_changes': True, 'ordinary_user_skill_mutation_denied': True})
elif args.action.startswith('run-'):
    phase = args.action[4:]
    client, uid = member, member_id
    resource_id = listings(True) if phase == 'v1' else skill()
    content = ('资源验收 v1：先调用 web-group-acceptance 技能并返回其中文回执，再处理 JINGJIAAGENT_WEB_RULE_CHECK。' if phase == 'v1' else
               '资源验收 v2：请处理 JINGJIAAGENT_WEB_RULE_CHECK，不要使用工具。' if phase == 'v2' else '资源验收 empty：只回复 RESOURCE_EMPTY_READY，不要使用工具。')
    existing = sql("SELECT id FROM tasks WHERE user_id='" + uid + "' AND content='" + content + "'").splitlines()
    if len(existing) > 1:
        raise SystemExit('Conflicting acceptance tasks; do not replay.')
    if existing:
        task_id = str(uuid.UUID(existing[0]))
    else:
        config = json.loads((state / 'web-fixture.json').read_text())
        task = success(client, '/api/v1/users/tasks', {'content': content, 'host_id': config['node_id'], 'image_id': config['image_id'],
            'model_id': config['model_id'], 'cli_name': 'opencode', 'task_type': 'develop', 'resource': {'core': 1, 'memory': 2*1024**3, 'life': 3600},
            'extra': {'skill_ids': [resource_id] if phase == 'v1' else [], 'plugin_ids': []}})
        task_id = str(uuid.UUID(task['id']))
    record['task_' + phase] = task_id
    report['task_' + phase] = task_id
elif args.action.startswith('verify-'):
    phase = args.action[7:]
    task_id = str(uuid.UUID(record['task_' + phase]))
    client = member
    chunks = success(client, '/api/v1/users/tasks/rounds?id=' + task_id + '&limit=10')['chunks']
    assistant = []
    for chunk in chunks:
        if chunk.get('event') == 'task-running':
            assistant.append(base64.b64decode(chunk.get('data', '')).decode('utf-8', errors='replace'))
        elif chunk.get('event') == 'user-input':
            incoming = json.loads(base64.b64decode(chunk.get('data', '')))
            prompt = base64.b64decode(incoming.get('content', '')).decode('utf-8', errors='replace')
            if any(record[name] in prompt for name in ['rule_v1', 'rule_v2', 'skill_receipt']):
                raise SystemExit('A resource receipt was supplied in user input.')
    text = '\n'.join(assistant)
    if phase != 'empty' and record['rule_' + phase] not in text:
        raise SystemExit('Actual Agent has not returned the rule receipt.')
    guest = container_for(task_id)
    path = '/root/.codingmatrix/project-tpl/.ai-ready/rules/' + record['rule_name'] + '.md'
    if phase == 'empty':
        if subprocess.run(['docker', 'exec', guest, 'test', '-e', path]).returncode != 1 or 'RESOURCE_EMPTY_READY' not in text:
            raise SystemExit('Cleared package still injected a rule into a new task, or Agent has not completed.')
    elif record['rule_' + phase] not in run_in_guest(guest, 'cat', path):
        raise SystemExit('Rule file differs from active original business version.')
    if phase == 'v1':
        if record['skill_receipt'] not in text or record['skill_receipt'] != run_in_guest(guest, 'cat', '/root/.codingmatrix/project-tpl/.ai-ready/skills/' + skill_name + '/references/中文回执.txt'):
            raise SystemExit('Actual selected Skill receipt is absent or differs.')
    if phase == 'v2' and record['rule_v1'] in text:
        raise SystemExit('New task used stale rule version.')
    report['actual_agent_' + phase + '_receipt_and_files'] = True
elif args.action == 'check-web-save':
    resource_id = listings(False)
    matches = [s for s in success(admin, '/api/v1/teams/skills')['skills'] if s['id'] == resource_id]
    if matches[0]['description'] != '读取指定中文回执，验证原 Skill 分组授权与远程执行。' or sorted(matches[0]['tags']) != sorted(['验收', '中文']):
        raise SystemExit('Original Web Skill metadata changes were not saved.')
    task = success(member, '/api/v1/users/tasks/' + str(uuid.UUID(record['task_v1'])))
    if task.get('extra', {}).get('skill_ids'):
        raise SystemExit('Original Web resource picker did not persist its empty filtered selection.')
    report['original_web_metadata_and_revoked_selection_saved'] = True
elif args.action.startswith('finish-'):
    phase = args.action[7:]
    task_id = str(uuid.UUID(record['task_' + phase]))
    if not report.get('actual_agent_' + phase + '_receipt_and_files'):
        raise SystemExit('Verify the named acceptance task before finishing it.')
    expected_prefix = '资源验收 ' + phase + '：'
    saved = success(member, '/api/v1/users/tasks/' + task_id)
    if saved['user_id'] != member_id or not saved['content'].startswith(expected_prefix):
        raise SystemExit('Only this script original API acceptance task may be stopped.')
    if saved['status'] == 'processing':
        success(member, '/api/v1/users/tasks/stop', {'id': task_id}, 'PUT')
    report['original_stop_' + phase + '_fixture_only'] = True
elif args.action == 'switch-revoked':
    resource_id = listings(False)
    task_id = str(uuid.UUID(record['task_v1']))
    session_before = sql("SELECT session_id FROM runtime_task_sessions WHERE task_id='" + task_id + "'")
    if not session_before:
        raise SystemExit('Actual native Agent session has not yet been persisted.')
    connection = socket.create_connection(('127.0.0.1', 47424), timeout=30)
    stream = connection.makefile('rb')
    key = base64.b64encode(secrets.token_bytes(16)).decode()
    cookie = '; '.join(c.name + '=' + c.value for c in member_jar)
    connection.sendall(('GET /api/v1/users/tasks/control?id=' + task_id + ' HTTP/1.1\r\nHost: 127.0.0.1:47424\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: ' + key + '\r\nCookie: ' + cookie + '\r\n\r\n').encode())
    if b' 101 ' not in stream.readline():
        raise SystemExit('Original resource control connection rejected.')
    while stream.readline() != b'\r\n':
        pass
    request_id = str(uuid.uuid4())
    data = {'request_id': request_id, 'skill_ids': [resource_id], 'plugin_ids': []}
    message = {'type': 'call', 'kind': 'switch_agent_resources', 'data': base64.b64encode(json.dumps(data).encode()).decode()}
    try:
        connection.sendall(frame(1, json.dumps(message).encode(), secrets.token_bytes(4)))
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            opcode, raw = read_frame(stream)
            if opcode == 9:
                connection.sendall(frame(10, raw, secrets.token_bytes(4)))
                continue
            if opcode != 1:
                continue
            response = json.loads(raw)
            if response.get('kind') != 'switch_agent_resources':
                continue
            value = response.get('data')
            if isinstance(value, str):
                value = json.loads(base64.b64decode(value))
            if value.get('request_id') == request_id:
                if not value.get('success'):
                    raise SystemExit('Original resource restart did not succeed.')
                break
        else:
            raise SystemExit('Original resource restart response timed out; inspect before retrying.')
    finally:
        stream.close()
        connection.close()
    guest = container_for(task_id)
    if subprocess.run(['docker', 'exec', guest, 'test', '-e', '/root/.codingmatrix/project-tpl/.ai-ready/skills/' + skill_name]).returncode != 1:
        raise SystemExit('Revoked explicitly requested Skill remains installed after resource switch.')
    if sql("SELECT session_id FROM runtime_task_sessions WHERE task_id='" + task_id + "'") != session_before:
        raise SystemExit('Skill change discarded the native Agent session.')
    report['revoked_id_cannot_reinstall_after_original_control_switch'] = True
    report['resource_switch_preserved_native_session'] = True
save()
print(json.dumps({'action': args.action, 'group': record['group'], 'tasks': {k:v for k,v in record.items() if k.startswith('task_')}, 'passed': True}, indent=2))
