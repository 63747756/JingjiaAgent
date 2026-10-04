"""Original authenticated URL MCP APIs/gateway and real Web Agent acceptance.

Only named transport fixtures may be created, disabled or stopped. No local
command input is added to the product. Credentials are never printed.
"""
import argparse
import base64
import concurrent.futures
import http.cookiejar
import json
import pathlib
import subprocess
import time
import urllib.error
import urllib.request
import uuid

state = pathlib.Path(__file__).resolve().parent / '.state'
origin = 'http://127.0.0.1:47420'
fixture = json.loads((state / 'mcp-transports.json').read_text())
record_path, report_path = state / 'web-mcp-transports.json', state / 'web-mcp-transports-report.json'
record = json.loads(record_path.read_text()) if record_path.exists() else {}
report = json.loads(report_path.read_text()) if report_path.exists() else {}
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('action', choices=['prepare', 'concurrency', 'run', 'verify', 'revoke', 'verify-revoked', 'finish'])
args = parser.parse_args()


def save():
    record_path.write_text(json.dumps(record, indent=2))
    report_path.write_text(json.dumps(report, indent=2))


def sql(query):
    return subprocess.check_output(['docker', 'exec', 'jingjia-runtime-tests-20261002', 'psql', '-v', 'ON_ERROR_STOP=1',
        '-U', 'postgres', '-d', 'monkeycode_web_poc', '-tAc', query], text=True).strip()


def api(client, route, data=None, method=None):
    req = urllib.request.Request(origin + route, data=json.dumps(data).encode() if data is not None else None,
        method=method, headers={'Content-Type': 'application/json'})
    try:
        with client.open(req, timeout=30) as response:
            body = json.load(response)
    except urllib.error.HTTPError as error:
        raise SystemExit('Original MCP API failed (HTTP ' + str(error.code) + ').') from None
    if body.get('code') != 0:
        raise SystemExit('Original MCP API refused operation (code ' + str(body.get('code')) + ').')
    return body.get('data')


def login(name, team=False):
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    user = api(client, '/api/v1/teams/users/login' if team else '/api/v1/users/password-login', json.loads((state / name).read_text()))
    return client, str(uuid.UUID(user['id']))


admin, _ = login('web-account.json', True)
member, member_id = login('web-member-account.json')
owner, owner_id = login('web-account.json')


def upstream(which):
    client, route = (member, '/api/v1/users/mcp/upstreams') if which == 'stream' else (admin, '/api/v1/teams/mcp/upstreams')
    items = api(client, route)['items']
    name = 'MCP transport acceptance ' + which
    matches = [item for item in items if item['id'] == record[which] and item['name'] == name and item['url'] == fixture[which + '_url']]
    if len(matches) != 1:
        raise SystemExit('Named upstream fixture changed; preserve it.')
    return matches[0]


def guard():
    groups = api(admin, '/api/v1/teams/groups')['groups']
    if not any(g['id'] == record['group'] and g['name'] == 'MCP transport acceptance' for g in groups):
        raise SystemExit('Named MCP group changed.')
    memberships = set(sql("SELECT user_id FROM team_group_members WHERE group_id='" + str(uuid.UUID(record['group'])) + "'").splitlines())
    if memberships - {owner_id, member_id}:
        raise SystemExit('Unexpected members in disposable MCP group.')
    for which in ['stream', 'legacy']:
        item = upstream(which)
        if which == 'legacy' and [g['id'] for g in item['groups']] != [record['group']]:
            raise SystemExit('Team upstream binding changed.')
    return memberships


def token(client, task_id):
    task = api(client, '/api/v1/users/tasks/' + str(uuid.UUID(task_id)))
    environment = task['virtualmachine']['id']
    uuid.UUID(environment.removeprefix('agent_'))
    key = sql("SELECT api_key FROM model_api_keys WHERE virtualmachine_id='" + environment + "' AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 1")
    if not key:
        raise SystemExit('Dedicated task credential missing.')
    return key


def gateway(key, method, params=None):
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    req = urllib.request.Request(origin + '/mcp', data=json.dumps({'jsonrpc': '2.0', 'id': uuid.uuid4().hex,
        'method': method, 'params': params or {}}).encode(), headers={'Content-Type': 'application/json',
        'Accept': 'application/json, text/event-stream', 'Authorization': 'Bearer ' + key})
    with client.open(req, timeout=20) as response:
        return json.load(response)


def audit():
    path = state / 'mcp-transports-audit.jsonl'
    return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []


if args.action == 'prepare':
    groups = api(admin, '/api/v1/teams/groups')['groups']
    if 'group' not in record:
        if any(g['name'] == 'MCP transport acceptance' for g in groups):
            raise SystemExit('Unowned MCP group already exists.')
        record['group'] = api(admin, '/api/v1/teams/groups', {'name': 'MCP transport acceptance'})['id']
        save()
    api(admin, '/api/v1/teams/groups/' + record['group'] + '/users', {'user_ids': [owner_id, member_id]}, 'PUT')
    for which in ['stream', 'legacy']:
        client, route = (member, '/api/v1/users/mcp/upstreams') if which == 'stream' else (admin, '/api/v1/teams/mcp/upstreams')
        name = 'MCP transport acceptance ' + which
        if which not in record:
            if any(item['name'] == name for item in api(client, route)['items']):
                raise SystemExit('Unowned same-name MCP upstream exists.')
            data = {'name': name, 'slug': 'transport-' + which + '-' + uuid.uuid4().hex[:8], 'url': fixture[which + '_url'],
                'headers': [{'name': 'Authorization', 'value': 'Bearer ' + fixture['token']}], 'enabled': True}
            if which == 'legacy':
                data['group_ids'] = [record['group']]
            record[which] = api(client, route, data)['id']
            save()
        started = time.monotonic()
        api(client, route + '/' + record[which] + '/sync', {})
        item = upstream(which)
        if item['sync_status'] != 'success' or len(item['tools']) != 1 or time.monotonic() - started >= 10:
            raise SystemExit('Persistent SSE enumeration did not return before stream EOF.')
        record[which + '_tool'] = item['tools'][0]
    guard()
    report['original_url_management_synced_both_sse_transports_before_eof'] = True
elif args.action == 'concurrency':
    if 'concurrent_values' in record:
        raise SystemExit('Concurrent tools already submitted; inspect evidence instead of replaying.')
    if guard() != {owner_id, member_id}:
        raise SystemExit('Concurrent scope fixtures differ.')
    keys = [token(owner, '96594b1d-4859-4e60-b902-1e4df75a1659'), token(member, '948b2f6d-707e-44fb-9670-da688240cf5b')]
    if keys[0] == keys[1]:
        raise SystemExit('Two task users share credentials.')
    names = [set(t['name'] for t in gateway(key, 'tools/list')['result']['tools']) for key in keys]
    if record['stream_tool']['namespaced_name'] in names[0] or record['stream_tool']['namespaced_name'] not in names[1] or any(record['legacy_tool']['namespaced_name'] not in listing for listing in names):
        raise SystemExit('Positive/negative original personal/team scopes differ.')
    before = len(audit())
    denied = gateway(keys[0], 'tools/call', {'name': record['stream_tool']['namespaced_name'], 'arguments': {'value': 7}})
    if 'error' not in denied or len(audit()) != before:
        raise SystemExit('Personal upstream crossed users.')
    start_value = 100000 + uuid.uuid4().int % 100000
    probes = [('stream', 1, start_value + i) for i in range(8)] + [('legacy', i % 2, start_value + 8 + i) for i in range(8)]
    record['concurrent_values'] = [p[2] for p in probes]
    save() # Persist intent; do not replay an uncertain tool execution.
    def invoke(probe):
        which, user, value = probe
        response = gateway(keys[user], 'tools/call', {'name': record[which + '_tool']['namespaced_name'], 'arguments': {'value': value}})
        if 'result' not in response or response['result'].get('isError') or not any(c.get('text') == str(value * 2) + ' ' + fixture[which + '_receipt'] for c in response['result'].get('content', [])):
            raise SystemExit('Concurrent real upstream result/session mismatched.')
    with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:
        list(pool.map(invoke, probes))
    actual = [item for item in audit() if item['value'] in record['concurrent_values']]
    if len(actual) != 16 or len({item['session'] for item in actual}) != 16 or len({item['request_id'] for item in actual}) != 16:
        raise SystemExit('Concurrent calls repeated execution or shared upstream sessions.')
    report.update({'parallel_gateway_calls_verified': 16, 'two_task_credentials_and_personal_team_scope_verified': True,
        'independent_upstream_sessions_and_rpc_ids': True})
elif args.action == 'run':
    guard()
    prompt = 'MCP SSE 验收初始化。仅回复 MCP_SSE_WEB_READY，不要使用工具。'
    existing = sql("SELECT id FROM tasks WHERE user_id='" + member_id + "' AND content='" + prompt + "'").splitlines()
    if len(existing) > 1:
        raise SystemExit('Conflicting MCP fixture tasks; do not replay.')
    if not existing:
        cfg = json.loads((state / 'web-fixture.json').read_text())
        task = api(member, '/api/v1/users/tasks', {'content': prompt, 'host_id': cfg['node_id'], 'image_id': cfg['image_id'],
            'model_id': cfg['model_id'], 'cli_name': 'opencode', 'task_type': 'develop', 'resource': {'core': 1, 'memory': 2*1024**3, 'life': 3600}})
        existing = [task['id']]
    record['task'] = str(uuid.UUID(existing[0]))
    report['original_task_created_with_unchanged_builtin_mcp_gateway'] = True
elif args.action == 'verify':
    guard()
    history = api(member, '/api/v1/users/tasks/rounds?id=' + record['task'] + '&limit=10')['chunks']
    texts = []
    for chunk in history:
        data = base64.b64decode(chunk.get('data') or '').decode('utf-8')
        if chunk['event'] == 'task-running':
            texts.append(data)
        if chunk['event'] == 'user-input':
            prompt = base64.b64decode(json.loads(data)['content']).decode('utf-8')
            if fixture['stream_receipt'] in prompt or fixture['legacy_receipt'] in prompt:
                raise SystemExit('Receipt was supplied to the model.')
    if any(str(value * 2) + ' ' + fixture[which + '_receipt'] not in '\n'.join(texts) for which, value in [('stream', 31), ('legacy', 32)]):
        raise SystemExit('Original real Agent/history has not returned both SSE tool receipts.')
    for which, value in [('stream', 31), ('legacy', 32)]:
        tool_id = str(uuid.UUID(record[which + '_tool']['id']))
        if not any(item['transport'] == which and item['value'] == value for item in audit()):
            raise SystemExit('Agent prose has no actual upstream execution audit.')
        count = sql("SELECT count(*) FROM mcp_tool_calls WHERE task_id='" + record['task'] + "' AND tool_id='" + tool_id + "' AND status='success'")
        if count != '1':
            raise SystemExit('Actual task did not execute each SSE tool exactly once.')
    report['original_web_followup_real_agent_both_sse_tools_and_history'] = True
elif args.action in ['revoke', 'verify-revoked']:
    guard()
    if args.action == 'revoke':
        api(admin, '/api/v1/teams/groups/' + record['group'] + '/users', {'user_ids': [owner_id]}, 'PUT')
        api(member, '/api/v1/users/mcp/tools/' + record['stream_tool']['id'], {'enabled': False}, 'PUT')
    key = token(member, record['task'])
    listing = {item['name'] for item in gateway(key, 'tools/list')['result']['tools']}
    before = len(audit())
    for which in ['stream', 'legacy']:
        tool_name = record[which + '_tool']['namespaced_name']
        if tool_name in listing or 'error' not in gateway(key, 'tools/call', {'name': tool_name, 'arguments': {'value': 39}}) or len(audit()) != before:
            raise SystemExit('Revoked tool still enumerated/executed.')
    report['group_revocation_and_personal_disable_deny_listing_and_call'] = True
    if args.action == 'verify-revoked':
        history = api(member, '/api/v1/users/tasks/rounds?id=' + record['task'] + '&limit=10')['chunks']
        turns = []
        for chunk in history:
            if chunk['event'] == 'user-input':
                body = json.loads(base64.b64decode(chunk['data']))
                if base64.b64decode(body['content']).decode('utf-8').startswith('MCP SSE 撤销验收：'):
                    turns.append(chunk['turn_seq'])
        if len(set(turns)) != 1:
            raise SystemExit('Submit the original Web revocation prompt once; do not replay it.')
        texts = '\n'.join(base64.b64decode(chunk['data']).decode('utf-8') for chunk in history
            if chunk['event'] == 'task-running' and chunk['turn_seq'] == turns[0])
        if 'MCP_SSE_ACCESS_REVOKED' not in texts or any(item['value'] == 39 for item in audit()):
            raise SystemExit('Real Agent revocation not completed or forbidden tool executed.')
        for which in ['stream', 'legacy']:
            tool_id = str(uuid.UUID(record[which + '_tool']['id']))
            if sql("SELECT count(*) FROM mcp_tool_calls WHERE task_id='" + record['task'] + "' AND tool_id='" + tool_id + "' AND status='success'") != '1':
                raise SystemExit('Revoked real task executed a tool again.')
        report['real_agent_after_revocation_no_new_successful_execution'] = True
elif args.action == 'finish':
    guard()
    if not report.get('original_web_followup_real_agent_both_sse_tools_and_history') or not report.get('real_agent_after_revocation_no_new_successful_execution'):
        raise SystemExit('Do not finish incomplete Agent/scope acceptance.')
    task = api(member, '/api/v1/users/tasks/' + record['task'])
    if task['content'] != 'MCP SSE 验收初始化。仅回复 MCP_SSE_WEB_READY，不要使用工具。':
        raise SystemExit('Only this dedicated task may be stopped.')
    if task['status'] == 'processing':
        api(member, '/api/v1/users/tasks/stop', {'id': record['task']}, 'PUT')
    for which, client, route in [('stream', member, '/api/v1/users/mcp/upstreams/'), ('legacy', admin, '/api/v1/teams/mcp/upstreams/')]:
        api(client, route + record[which], {'enabled': False}, 'PUT')
    report['only_new_task_stopped_and_named_upstreams_disabled'] = True
save()
print(json.dumps({'action': args.action, 'task': record.get('task'), 'passed': True}))
