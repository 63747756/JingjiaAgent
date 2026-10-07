"""Real isolated Web/AD API acceptance; never targets the existing deployment.

The directory is a TLS test fixture. Optional task execution uses the privately
configured real model. Outputs contain only results and fixture identifiers.
"""
import argparse
import base64
import concurrent.futures
import hashlib
import http.client
import http.cookiejar
from http.cookies import SimpleCookie
import json
import pathlib
import secrets
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
STATE = ROOT / '.state/ad-acceptance'
ORIGIN = 'http://127.0.0.1:47426'
DATABASE = 'jingjiaagent-ad-acceptance-postgres-1'
REPORT = {}


def load(name):
    return json.loads((STATE / name).read_text(encoding='utf-8'))


def save(name, value):
    path = STATE / name
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2), encoding='utf-8')
    path.chmod(0o600)


def check(name, condition):
    REPORT[name] = bool(condition)
    save('ad-api-report.json', REPORT)
    if not condition:
        raise RuntimeError('AD acceptance failed: ' + name)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *_):
        return None


class Client:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.http = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(),
                                                urllib.request.HTTPCookieProcessor(self.jar))

    def raw(self, route, data=None, method=None, headers=None):
        request = urllib.request.Request(ORIGIN + route, data=data,
                                         headers=headers or {'Content-Type': 'application/json'}, method=method)
        try:
            response = self.http.open(request, timeout=60)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.status, response.read(), dict(response.headers)

    def response(self, route, data=None, method=None):
        status, raw, headers = self.raw(route, json.dumps(data).encode() if data is not None else None, method)
        try:
            body = json.loads(raw)
        except ValueError:
            body = {}
        return status, body, headers

    def api(self, route, data=None, method=None):
        status, body, _ = self.response(route, data, method)
        if status != 200 or body.get('code') != 0:
            save('ad-api-last-failure.json', {'route': route, 'status': status, 'body': body})
            raise RuntimeError('Isolated API failed: ' + route + ' HTTP ' + str(status)
                               + ' code ' + str(body.get('code')))
        return body.get('data')


def sql(query):
    return subprocess.check_output(['docker', 'exec', '-i', DATABASE, 'psql', '-v', 'ON_ERROR_STOP=1',
                                    '-U', 'postgres', '-d', 'jingjiaagent', '-At'],
                                   input=query, text=True, encoding='utf-8').strip()


def memberships(user):
    user = str(uuid.UUID(user))
    rows = sql("SELECT json_build_object('group',group_id,'source',source) FROM team_group_members WHERE user_id='" + user + "' ORDER BY group_id;")
    return {item['group']: item['source'] for item in map(json.loads, rows.splitlines())}


def login_ad(username, users, password=None):
    client = Client()
    actor = client.api('/api/v1/users/ad-login', {'account': username,
                       'password': users[username]['password'] if password is None else password})
    return client, actor


def wait(predicate, label, seconds=240):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(.5)
    raise RuntimeError(label + ' timed out')


def authentication_and_groups():
    admin = Client()
    actor = admin.api('/api/v1/teams/users/login', load('web-account.json'))
    check('local_administrator_works', bool(actor.get('team', {}).get('id')))
    # Repeat an interrupted acceptance run without deleting project data. The
    # immutable fixture baseline restores directory names, never production AD.
    for name in ('directory', 'users'):
        baseline = STATE / 'fixture' / (name + '.initial.json')
        if not baseline.exists():
            save('fixture/' + name + '.initial.json', load('fixture/' + name + '.json'))
        save('fixture/' + name + '.json', load('fixture/' + name + '.initial.json'))
    prior = sql("SELECT id FROM users WHERE id IN (SELECT user_id FROM user_identities WHERE platform='ad') AND is_blocked=true;")
    for user_id in prior.splitlines():
        admin.api('/api/v1/teams/users/' + str(uuid.UUID(user_id)), {'is_blocked': False}, 'PUT')
    public = Client()
    check('fresh_default_disabled', load('environment-ready.json')['fresh_ad_disabled'])
    candidate = load('fixture/config.json')
    candidate['revision'] = admin.api('/api/v1/teams/ad')['config']['revision']
    config = admin.api('/api/v1/teams/ad', candidate, 'PUT')['config']
    check('draft_keeps_local_mode', public.api('/api/v1/users/auth-config')['mode'] == 'local')
    check('draft_saved_password_redacted', not config['enabled'] and config['has_bind_password']
          and 'bind_password' not in config and 'bind_password_ciphertext' not in config)
    candidate.update(revision=config['revision'], bind_password='')
    check('connection_test', admin.api('/api/v1/teams/ad/test', candidate)['success'])
    candidate['enabled'] = True
    config = admin.api('/api/v1/teams/ad', candidate, 'PUT')['config']
    check('enabled_public_config', public.api('/api/v1/users/auth-config')['mode'] == 'ad')
    check('query_password_encrypted', sql("SELECT bool_and(bind_password_ciphertext LIKE 'v1:%') FROM team_ad_configs;") == 't')

    users = load('fixture/users.json')
    identities, clients = {}, {}
    for name in ('alice', 'bob', 'charlie', 'escaped', 'noou', 'noemail'):
        clients[name], identities[name] = login_ad(name, users)
        check(name + '_login', identities[name]['auth_source'] == 'ad' and not identities[name]['has_password'])
    check('no_email_is_empty', identities['noemail']['email'] == '')
    groups = admin.api('/api/v1/teams/groups')['groups']
    department = {user: next(g for g in groups if g['source'] == 'ad_ou'
                  and any(u['id'] == identities[user]['id'] for u in g.get('users', []))) for user in identities}
    default_id = next(key for key, source in memberships(identities['alice']['id']).items() if source == 'ad_default')
    check('same_ou_same_group', department['alice']['id'] == department['bob']['id'])
    check('same_name_different_ou_distinct_group', department['alice']['id'] != department['charlie']['id'])
    check('escaped_department_path', department['escaped']['ou_path'] == '公司／研发,工具组')
    check('no_ou_unassigned', department['noou']['ou_path'] == '未分配部门')
    check('all_default_and_department_bindings', all(sorted(source for source in memberships(x['id']).values()
          if source.startswith('ad_')) == ['ad_default', 'ad_ou'] for x in identities.values()))
    repeated_client, repeated_actor = login_ad('bob', users)
    check('repeat_login_keeps_identity', repeated_actor['id'] == identities['bob']['id'])
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        repeated = list(pool.map(lambda _: login_ad('noemail', users)[1]['id'], range(2)))
    check('concurrent_login_no_duplicates', set(repeated) == {identities['noemail']['id']}
          and sql("SELECT count(*) FROM user_identities WHERE platform='ad';") == '6')

    for name in ('disabled', 'outsider', 'locked'):
        status, body, _ = Client().response('/api/v1/users/ad-login', {'account': name, 'password': users[name]['password']})
        check(name + '_denied', status == 401 and body.get('code') == 10642)
    status, body, _ = Client().response('/api/v1/users/ad-login', {'account': 'alice', 'password': 'incorrect-password'})
    check('wrong_password_denied', status == 401 and body.get('code') == 10642)
    for path, data in [('/api/v1/users/password-login', load('web-member-account.json')),
                       ('/api/v1/users/oidc/login?team_id=' + actor['team']['id'], None),
                       ('/api/v1/users/oidc/callback?code=test&state=test', None),
                       ('/api/v1/users/oauth/github/login', None),
                       ('/api/v1/users/oauth/github/callback?code=test&state=test', None)]:
        status, body, _ = Client().response(path, data)
        check('blocked_' + path.split('?')[0].rsplit('/', 2)[-2] + '_' + path.split('?')[0].rsplit('/', 1)[-1],
              status == 403 and body.get('code') == 10643)
    for route, data, method, client in [('/api/v1/users/passwords/change', {'new_password': 'DoNotSet_AD_123!'}, 'PUT', clients['alice']),
          ('/api/v1/users/passwords/reset-request', {'emails': [identities['alice']['email']]}, 'PUT', Client()),
          ('/api/v1/teams/users/' + identities['alice']['id'] + '/passwords/reset', {}, 'PUT', admin)]:
        status, body, _ = client.response(route, data, method)
        check('ad_password_denied_' + route.rsplit('/', 1)[-1], status == 403 and body.get('code') == 10648)

    manual = next((group for group in groups if group['name'] == 'AD acceptance manual group'), None)
    if manual is None:
        manual = admin.api('/api/v1/teams/groups', {'name': 'AD acceptance manual group'})
    admin.api('/api/v1/teams/groups/' + manual['id'] + '/users', {'user_ids': [identities['alice']['id']]}, 'PUT')
    local_members = admin.api('/api/v1/teams/users?role=user')['members']
    local_ids = [item['user']['id'] for item in local_members if item['user']['auth_source'] != 'ad']
    admin.api('/api/v1/teams/groups/' + default_id + '/users', {'user_ids': local_ids}, 'PUT')
    check('default_auto_bindings_survive_manual_edit', all(memberships(x['id']).get(default_id) == 'ad_default' for x in identities.values()))
    department_id = department['alice']['id']
    for method, suffix, data in [('PUT', '', {'name': 'tampered'}), ('DELETE', '', None),
                                 ('PUT', '/users', {'user_ids': local_ids})]:
        status, body, _ = admin.response('/api/v1/teams/groups/' + department_id + suffix, data, method)
        check('managed_group_denied_' + method + suffix, status == 403 and body.get('code') == 10646)

    model = load('model.json')
    resource = next((entry for entry in admin.api('/api/v1/teams/models')['models']
                     if entry['remark'] == 'AD department-only model'), None)
    if resource is None:
        resource = admin.api('/api/v1/teams/models', {'provider': 'DeepSeek', 'api_key': model['api_key'],
             'base_url': model['base_url'], 'model': model['model'], 'interface_type': 'openai_chat',
             'temperature': .2, 'remark': 'AD department-only model', 'support_image': False, 'group_ids': [department_id]})
    check('managed_group_resource_config_allowed', any(g['id'] == department_id for g in resource['groups']))
    def model_ids(client):
        return {entry['id'] for entry in client.api('/api/v1/users/models?limit=100')['models']}
    check('default_and_department_resource_union', resource['id'] in model_ids(clients['alice'])
          and load('web-fixture.json')['model_id'] in model_ids(clients['alice'])
          and resource['id'] not in model_ids(clients['charlie']))

    directory = load('fixture/directory.json')
    alice = next(item for item in directory['entries'] if item.get('attributes', {}).get('sAMAccountName') == 'alice')
    alice['attributes']['sAMAccountName'] = 'alice-renamed'
    users['alice-renamed'] = dict(users['alice'], username='alice-renamed')
    save('fixture/directory.json', directory)
    renamed_client, renamed = login_ad('alice-renamed', users)
    check('user_rename_keeps_guid_identity', renamed['id'] == identities['alice']['id'] and renamed['login_name'] == 'alice-renamed')
    old_ou = 'OU=软件部,OU=研发中心,OU=公司,' + directory['base_dn']
    renamed_ou = 'OU=软件工程部,OU=研发中心,OU=公司,' + directory['base_dn']
    for entry in directory['entries']:
        if entry['dn'] == old_ou or entry['dn'].endswith(',' + old_ou):
            entry['dn'] = entry['dn'][:-len(old_ou)] + renamed_ou
    save('fixture/directory.json', directory)
    clients['bob'], _ = login_ad('bob', users)
    renamed_group = next(g for g in admin.api('/api/v1/teams/groups')['groups'] if g['id'] == department_id)
    check('ou_rename_keeps_group_and_authorization', renamed_group['ou_path'] == '公司／研发中心／软件工程部'
          and resource['id'] in model_ids(clients['bob']))
    moved_ou = 'OU=软件工程部,OU=技术支持,OU=公司,' + directory['base_dn']
    for entry in directory['entries']:
        if entry['dn'] == renamed_ou or entry['dn'].endswith(',' + renamed_ou):
            entry['dn'] = entry['dn'][:-len(renamed_ou)] + moved_ou
    save('fixture/directory.json', directory)
    clients['bob'], _ = login_ad('bob', users)
    moved_group = next(g for g in admin.api('/api/v1/teams/groups')['groups'] if g['id'] == department_id)
    check('ou_move_keeps_group_and_authorization', moved_group['ou_path'] == '公司／技术支持／软件工程部'
          and resource['id'] in model_ids(clients['bob']))
    bob_before = memberships(identities['bob']['id'])
    alice['dn'] = 'CN=alice,OU=软件部,OU=技术支持,OU=公司,' + directory['base_dn']
    save('fixture/directory.json', directory)
    moved_client, moved_actor = login_ad('alice-renamed', users)
    after = memberships(moved_actor['id'])
    check('employee_transfer_only_own_auto_group', after.get(default_id) == 'ad_default'
          and after.get(manual['id']) == 'manual' and after.get(department['charlie']['id']) == 'ad_ou'
          and department_id not in after and memberships(identities['bob']['id']) == bob_before)
    check('transfer_refreshes_new_resource_access', resource['id'] not in model_ids(moved_client))
    directory['fail_ou_query'] = True
    save('fixture/directory.json', directory)
    status, body, _ = Client().response('/api/v1/users/ad-login', {'account': 'alice-renamed', 'password': users['alice-renamed']['password']})
    check('ou_query_failure_preserves_memberships', status == 503 and body.get('code') == 10641
          and memberships(moved_actor['id']) == after)
    directory.pop('fail_ou_query')
    save('fixture/directory.json', directory)

    admin.api('/api/v1/teams/users/' + identities['noemail']['id'], {'is_blocked': True}, 'PUT')
    status, _, _ = clients['noemail'].response('/api/v1/users/tasks')
    check('administrator_block_revokes_user_session', status == 401)
    current = admin.api('/api/v1/teams/ad')['config']
    candidate.update(enabled=False, revision=current['revision'])
    config = admin.api('/api/v1/teams/ad', candidate, 'PUT')['config']
    status, body, _ = Client().response('/api/v1/users/ad-login', {'account': 'bob', 'password': users['bob']['password']})
    check('disable_ad_rejects_new_login', status == 403 and body.get('code') == 10643)
    check('disable_ad_preserves_existing_session', clients['bob'].response('/api/v1/users/tasks')[0] == 200)
    candidate.update(enabled=True, revision=config['revision'])
    admin.api('/api/v1/teams/ad', candidate, 'PUT')
    save('fixture/users.json', users)
    save('ad-identities.json', {name: {'id': value['id'], 'login_name': value['login_name']} for name, value in identities.items()})
    REPORT['final_ad_enabled'] = True
    REPORT['test_accounts'] = ['bob', 'charlie', 'escaped', 'noou', 'alice-renamed']
    save('ad-api-report.json', REPORT)
    return clients['bob'], clients['charlie'], identities['bob'], admin


def real_agent_files_preview(owner, foreign, actor, admin, reuse_task=False):
    fixture = load('web-fixture.json')
    wait(lambda: any(item['id'] == fixture['node_id'] and item['status'] == 'online'
                    for item in owner.api('/api/v1/users/hosts').get('hosts', [])), 'independent runtime registry')
    previous = load('ad-real-task.json') if reuse_task and (STATE / 'ad-real-task.json').exists() else None
    if previous is not None:
        check('resume_existing_task_owned', previous['actor'] == actor['id'])
        task_id, marker = str(uuid.UUID(previous['task'])), previous['marker']
    else:
        marker = 'AD_REAL_' + secrets.token_hex(10)
        content = '请使用工具在 /workspace/AD真实任务.txt 写入以下标记，然后只回复同一标记：' + marker
        task = owner.api('/api/v1/users/tasks', {'content': content, 'host_id': fixture['node_id'],
                 'image_id': fixture['image_id'], 'model_id': fixture['model_id'], 'cli_name': 'opencode',
                 'task_type': 'develop', 'repo': {'repo_url': '', 'branch': 'main'},
                 'resource': {'core': 1, 'memory': 2 << 30, 'life': 3600}})
        task_id = str(uuid.UUID(task['id']))
    save('ad-real-task.json', {'task': task_id, 'actor': actor['id'], 'marker': marker})
    def complete():
        state = sql("SELECT state FROM runtime_commands WHERE task_id='" + task_id + "' AND operation='task' ORDER BY turn LIMIT 1;")
        if state in ('failed', 'canceled'):
            raise RuntimeError('Real Agent run ended ' + state + '; inspect private diagnostics')
        return state == 'complete'
    wait(complete, 'real AD user OpenCode task', 300)
    detail = owner.api('/api/v1/users/tasks/' + task_id)
    environment = detail['virtualmachine']['id']
    sandbox = sql("SELECT sandbox_id FROM runtime_environments WHERE id='" + environment + "';")
    save('ad-real-task.json', {'task': task_id, 'actor': actor['id'], 'marker': marker,
                               'environment': environment, 'sandbox': sandbox})
    chunks = map(json.loads, sql("SELECT chunk FROM runtime_events WHERE task_id='" + task_id + "' ORDER BY seq;").splitlines())
    output = ''
    for chunk in chunks:
        if chunk.get('event') != 'task-running':
            continue
        text = base64.b64decode(chunk.get('data', '')).decode('utf-8', errors='replace')
        if chunk.get('kind') == 'acp_event':
            update = json.loads(text).get('update', {})
            if update.get('sessionUpdate') == 'agent_message_chunk':
                output += update.get('content', {}).get('text', '')
        else:
            output += text
    check('real_model_task_and_output', marker in output and detail['user_id'] == actor['id'])
    def query(path):
        return urllib.parse.urlencode({'id': environment, 'path': path})
    path = '/workspace/AD真实任务.txt'
    status, data, _ = owner.raw('/api/v1/users/files/download?' + query(path))
    check('real_agent_created_file', status == 200 and marker.encode() in data)
    boundary = 'ad-' + secrets.token_hex(12)
    originals = {'中文文件.txt': '部门员工上传验收\n'.encode(), '二进制.bin': bytes(range(256)) * 8, '空文件.txt': b''}
    for name, expected in originals.items():
        path = '/workspace/' + name
        body = (('--' + boundary + '\r\nContent-Disposition: form-data; name="file"; filename="' + name
                  + '"\r\nContent-Type: application/octet-stream\r\n\r\n').encode() + expected
                + ('\r\n--' + boundary + '--\r\n').encode())
        status, response, _ = owner.raw('/api/v1/users/files/upload?' + query(path), body, 'POST',
                                       {'Content-Type': 'multipart/form-data; boundary=' + boundary})
        check('upload_' + name, status == 200 and json.loads(response).get('code') == 0)
        status, content, headers = owner.raw('/api/v1/users/files/download?' + query(path))
        check('download_' + name, status == 200 and content == expected and headers.get('Content-Length') == str(len(expected)))
    owner.api('/api/v1/users/files/save', {'id': environment, 'path': '/workspace/中文文件.txt',
              'content': '编辑后的中文内容\n'}, 'PUT')
    check('edit_chinese_file', owner.raw('/api/v1/users/files/download?' + query('/workspace/中文文件.txt'))[1] == '编辑后的中文内容\n'.encode())
    copied, moved = '/workspace/中文复制.bin', '/workspace/中文移动.bin'
    owner.api('/api/v1/users/files/copy', {'id': environment, 'source': '/workspace/二进制.bin', 'target': copied})
    owner.api('/api/v1/users/files/move', {'id': environment, 'source': copied, 'target': moved}, 'PUT')
    check('copy_move_exact', owner.raw('/api/v1/users/files/download?' + query(moved))[1] == originals['二进制.bin'])
    owner.api('/api/v1/users/files', {'id': environment, 'path': moved}, 'DELETE')
    status, content, _ = foreign.raw('/api/v1/users/files/download?' + query('/workspace/二进制.bin'))
    try:
        denied = json.loads(content).get('code') != 0
    except ValueError:
        denied = status != 200
    check('cross_user_file_denied', denied)

    container = 'agent-compose-' + sandbox[:12]
    preview = load('ad-preview.json') if reuse_task and (STATE / 'ad-preview.json').exists() else None
    if preview is None or preview.get('environment') != environment or not preview.get('receipt', '').startswith('preview-fixture-AD-'):
        preview = {'receipt': 'preview-fixture-AD-' + secrets.token_hex(8), 'task': task_id,
                   'environment': environment, 'container': container}
    save('ad-preview.json', preview)
    subprocess.run(['docker', 'cp', str(ROOT / 'preview_fixture.py'), container + ':/tmp/jingjiaagent-preview-fixture.py'],
                   check=True, stdout=subprocess.DEVNULL)
    subprocess.run(['docker', 'cp', str(STATE / 'ad-preview.json'), container + ':/tmp/jingjiaagent-preview-fixture.json'],
                   check=True, stdout=subprocess.DEVNULL)
    host_id = detail['virtualmachine']['host']['id']
    route = '/api/v1/users/hosts/' + host_id + '/vms/' + environment + '/ports'
    if not any(item['port'] == 47880 for item in owner.api(route)):
        subprocess.run(['docker', 'exec', '-d', container, 'python3', '/tmp/jingjiaagent-preview-fixture.py'], check=True)
    wait(lambda: any(item['port'] == 47880 for item in owner.api(route)), 'actual preview server', 30)
    status, response, _ = owner.response('/api/v1/users/hosts/client-ip')
    check('preview_client_ip_available', status == 200 and isinstance(response.get('ip'), str))
    client_ip = response['ip']
    opened = owner.api(route, {'port': 47880, 'white_list': [client_ip]})
    forward = opened['forward_id']
    status, _, headers = owner.response('/api/v1/runtime/previews/' + forward)
    check('preview_authorized_admission', status == 303)
    location = urllib.parse.urlparse(headers['Location'])
    check('preview_uses_independent_port', location.netloc == forward + '.localhost:47427')
    def preview_http(path, cookie=None):
        connection = http.client.HTTPConnection('127.0.0.1', 47427, timeout=15)
        outgoing = {'Host': location.netloc}
        if cookie:
            outgoing['Cookie'] = cookie
        connection.request('GET', path, headers=outgoing)
        response = connection.getresponse()
        result = response.status, response.read(), dict(response.headers)
        connection.close()
        return result
    status, _, grant = preview_http(location.path + '?' + location.query)
    cookie = SimpleCookie(grant.get('Set-Cookie'))
    cookie_header = 'jingjiaagent_preview=' + cookie['jingjiaagent_preview'].value
    check('preview_ticket_redeemed', status == 303)
    status, data, _ = preview_http('/receipt', cookie_header)
    check('preview_actual_guest_access', status == 200 and json.loads(data)['receipt'] == preview['receipt'])
    check('preview_anonymous_denied', preview_http('/receipt')[0] == 401)
    check('preview_cross_user_denied', foreign.response('/api/v1/runtime/previews/' + forward)[0] == 403)
    owner.api(route + '/47880', {'forward_id': forward}, 'DELETE')
    check('preview_closed_revokes_cookie', preview_http('/receipt', cookie_header)[0] == 403)
    save('ad-real-task.json', {'task': task_id, 'actor': actor['id'], 'marker': marker,
                               'environment': environment, 'sandbox': sandbox, 'task_url': ORIGIN + '/console/task/' + task_id})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--real-agent', action='store_true')
    parser.add_argument('--real-only', action='store_true', help='Resume only the real Agent/files/preview checks after AD API checks')
    args = parser.parse_args()
    if not (STATE / 'environment-ready.json').is_file():
        raise SystemExit('Prepare the dedicated fresh acceptance environment first.')
    try:
        if args.real_only:
            REPORT.update(load('ad-api-report.json'))
            users = load('fixture/users.json')
            owner, actor = login_ad('bob', users)
            foreign, _ = login_ad('charlie', users)
            admin = Client()
            admin.api('/api/v1/teams/users/login', load('web-account.json'))
        else:
            owner, foreign, actor, admin = authentication_and_groups()
        if args.real_agent or args.real_only:
            real_agent_files_preview(owner, foreign, actor, admin, reuse_task=args.real_only)
        save('ad-api-report.json', REPORT)
        print(json.dumps({'passed_checks': sum(value is True for value in REPORT.values()),
                          'ad_enabled': REPORT.get('final_ad_enabled'), 'real_agent': REPORT.get('real_model_task_and_output', False),
                          'origin': ORIGIN, 'real_ad_verified': False}, ensure_ascii=False))
    except Exception as error:
        save('ad-api-failure.json', {'stage': str(error), 'completed_checks': REPORT})
        print(str(error), file=sys.stderr)
        raise SystemExit(1) from None


if __name__ == '__main__':
    main()
