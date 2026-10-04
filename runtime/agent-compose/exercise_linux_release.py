"""Upgrade and roll back only jingjia-phase4-web, preserving private state.

Uses the recorded immutable baseline and the current candidate image. Keeps
all data volumes, encryption keys, runtime nodes and submitted Run identities.
It does not migrate environments or pretend a missing Taskflow server works.
"""
import base64
import hashlib
import json
import pathlib
import secrets
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
from linux_web_common import TaskSocket, api, load, login, request, runtime_rpc, save, sql, state, task_output, wait

root = pathlib.Path(__file__).resolve().parent
config_path = state / 'config/server/config.yaml'
env_path = state / 'compose.env'
baseline = load('release-baseline.json')
candidate = dict(baseline)
candidate['backend'] = json.loads(subprocess.check_output(
    ['docker', 'image', 'inspect', 'jingjia-monkeycode-web:phase4']))[0]['Id']
if candidate['backend'] == baseline['backend']:
    raise RuntimeError('Build a distinct candidate before the release exercise')
save('release-candidate.json', candidate)
for label, images in [('baseline', baseline), ('candidate', candidate)]:
    subprocess.run(['docker', 'image', 'inspect', images['backend']], check=True, stdout=subprocess.DEVNULL)
    # Docker's containerd image store may remove an untagged manifest when
    # its final container disappears. An ID in a JSON file does not retain it.
    subprocess.run(['docker', 'tag', images['backend'], 'jingjia-monkeycode-web:phase4-' + label], check=True)
original_config = json.loads(config_path.read_text())
if original_config['runtime']['backend'] != 'agent_compose':
    raise RuntimeError('Release exercise requires the original compose default')
compose = ['docker', 'compose', '-p', 'jingjia-phase4-web', '--env-file', str(env_path), '-f', str(root / 'compose.web.yaml')]
tasks = load('capacity-tasks-p17.json')
git_task = load('git-agent.json')['task']
git_detail = api(login('web-member-account.json')[0], '/api/v1/users/tasks/' + git_task)
fixtures = [tasks['owner-opencode'], tasks['owner-claude'], tasks['member-codex'],
    {'task': git_task, 'vm': git_detail['virtualmachine']['id']}]
record = load('release-report.json') if (state / 'release-report.json').exists() else {
    'marker': 'RELEASE_' + secrets.token_hex(12), 'steps': {}}
save('release-report.json', record)


def query_rows(query):
    return [json.loads(line) for line in sql(query).splitlines()]


def snapshot():
    return {
        'environments': query_rows("SELECT json_build_object('id',id,'node',node_id,'backend',backend,'sandbox',sandbox_id) FROM runtime_environments ORDER BY id;"),
        'runs': query_rows("SELECT json_build_object('id',id,'task',task_id,'turn',turn,'run',run_id,'state',state) FROM runtime_commands WHERE operation='task' ORDER BY id;"),
        'sessions': query_rows("SELECT json_build_object('task',task_id,'provider',provider,'session',session_id) FROM runtime_task_sessions ORDER BY task_id;"),
        'events': query_rows("SELECT json_build_object('task',task_id,'seq',max(seq),'count',count(*)) FROM runtime_events GROUP BY task_id ORDER BY task_id;"),
        'reservations': query_rows("SELECT json_build_object('environment',environment_id,'active',active) FROM runtime_reservations ORDER BY environment_id;")}


def assert_unchanged(before):
    after = snapshot()
    for key in before:
        if before[key] != after[key]:
            raise RuntimeError('Release changed persisted ' + key + ' without a user command')


def guest_hash(task, filename, jar):
    sandbox = sql("SELECT sandbox_id FROM runtime_environments WHERE id='" + task['vm'] + "';")
    with TaskSocket(task['task'], jar, control=True):
        wait(lambda: runtime_rpc('SandboxService', 'GetSandbox', {'sandboxId': sandbox})['sandbox']['status'] == 'SANDBOX_STATUS_RUNNING', 60, label='release original task resume')
        code = 'import hashlib,pathlib; print(hashlib.sha256(pathlib.Path(' + repr(filename) + ').read_bytes()).hexdigest())'
        result = runtime_rpc('ExecService', 'Exec', {'sandboxId': sandbox,
            'command': {'command': 'python3', 'args': ['-c', code]}, 'timeoutMs': 10000, 'maxOutputBytes': 128})['result']
        if result.get('exitCode', 0) != 0:
            raise RuntimeError('Release persistent file missing')
        value = result.get('stdout', '').strip()
        if len(value) != 64:
            raise RuntimeError('Release file hash invalid')
        return value


def fingerprints():
    owner, _, jar = login()
    member, _, member_jar = login('web-member-account.json')
    for task in fixtures:
        client = owner if task in fixtures[:2] else member
        detail = api(client, '/api/v1/users/tasks/' + task['task'])
        if detail['virtualmachine']['id'] != task['vm']:
            raise RuntimeError('Release business environment identity changed')
        history = api(client, '/api/v1/users/tasks/rounds?' + urllib.parse.urlencode({'id': task['task'], 'limit': 10}))
        if not history.get('chunks'):
            raise RuntimeError('Release original authenticated history missing')
    return {
        'boundary': guest_hash(fixtures[0], '/workspace/phase4-file-boundary/边界二进制.bin', jar),
        'approved': guest_hash(fixtures[1], '/workspace/web-native-allow-' + load('web-native-controls.json')['marker'] + '.txt', jar),
        'git_push': guest_hash(fixtures[3], '/workspace/phase4-push.txt', member_jar)}


def deploy(images, default):
    cfg = json.loads(json.dumps(original_config))
    cfg['runtime']['backend'] = default
    config_path.write_text(json.dumps(cfg, indent=2), encoding='utf-8')
    config_path.chmod(0o600)
    values = dict(line.split('=', 1) for line in env_path.read_text().splitlines() if '=' in line)
    values['WEB_BACKEND_IMAGE'] = images['backend']
    env_path.write_text(''.join(key + '=' + value + '\n' for key, value in values.items()), encoding='utf-8')
    env_path.chmod(0o600)
    save('images.json', images)
    subprocess.run(compose + ['up', '-d', '--force-recreate', '--wait', '--wait-timeout', '120', 'backend'], check=True)
    subprocess.run([sys.executable, str(root / 'start_linux_web.py')], check=True)
    running = json.loads(subprocess.check_output(['docker', 'inspect', 'jingjia-phase4-web-backend-1']))[0]
    if running['Image'] != images['backend'] or running['State'].get('Health', {}).get('Status') != 'healthy':
        raise RuntimeError('Release did not deploy the recorded immutable image')


def git_bridge():
    vm = fixtures[3]['vm']
    key = sql("SELECT api_key FROM model_api_keys WHERE virtualmachine_id='" + vm + "' AND kind='runtime' AND deleted_at IS NULL;")
    if not key or '\n' in key:
        raise RuntimeError('Release scoped Git credential missing or ambiguous')
    repo = urllib.parse.urlparse(load('git-fixture.json')['url'])
    body = {'task_id': git_task, 'vm_id': vm, 'protocol': repo.scheme, 'host': repo.netloc, 'path': repo.path.lstrip('/')}
    http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    req = urllib.request.Request('http://127.0.0.1:47424/api/v1/runtime/git-credential', data=json.dumps(body).encode(),
        headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + key})
    with http.open(req, timeout=30) as response:
        result = json.load(response)
        if response.headers.get('Cache-Control') != 'no-store' or result.get('code') != 0 or result['data'].get('password') != load('git-pat.json')['token']:
            raise RuntimeError('Release lost current project Git credential authorization')
    body['path'] = 'another-project/denied.git'
    req = urllib.request.Request('http://127.0.0.1:47424/api/v1/runtime/git-credential', data=json.dumps(body).encode(),
        headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + key})
    try:
        http.open(req, timeout=30).close()
        raise RuntimeError('Release broadened Git credential repository scope')
    except urllib.error.HTTPError as error:
        if error.code != 403:
            raise RuntimeError('Release Git scope rejection differed') from None
    # Exercise real Git's helper chain as well as the HTTP callback. Repeated
    # task preparation must leave one empty reset and one scoped helper.
    sandbox = sql("SELECT sandbox_id FROM runtime_environments WHERE id='" + vm + "';")
    expected = '!python3 /data/state/monkeycode-git/' + git_task + '.py'
    code = ('import subprocess,json,hashlib; '
        'p=subprocess.run(["git","config","--local","--get-all","credential.helper"],capture_output=True,text=True,check=True); '
        'helpers=p.stdout.splitlines(); '
        'r=subprocess.run(["git","credential","fill"],input=' + repr('protocol=' + repo.scheme + '\nhost=' + repo.netloc + '\npath=' + repo.path.lstrip('/') + '\n\n') + ',capture_output=True,text=True,timeout=15); '
        'values=dict(line.split("=",1) for line in r.stdout.splitlines() if "=" in line); '
        'print(json.dumps({"helper_chain":helpers==["",' + repr(expected) + '],"credential_fill":r.returncode==0 and hashlib.sha256(values.get("password","").encode()).hexdigest()==' + repr(hashlib.sha256(load('git-pat.json')['token'].encode()).hexdigest()) + '}))')
    result = runtime_rpc('ExecService', 'Exec', {'sandboxId': sandbox, 'command': {'command': 'python3', 'args': ['-c', code]},
        'cwd': '/workspace', 'timeoutMs': 20000, 'maxOutputBytes': 512})['result']
    if result.get('exitCode', 0) != 0 or json.loads(result.get('stdout', '{}')) != {'helper_chain': True, 'credential_fill': True}:
        raise RuntimeError('Repeated preparation broke the actual scoped Git helper chain')


wait(lambda: sql("SELECT count(*) FROM runtime_commands WHERE state NOT IN ('complete','failed','canceled');") == '0', label='release starts without pending commands')
before_files = fingerprints()
expected_files = {
    'boundary': load('file-boundary-report.json')['sha256'],
    'approved': hashlib.sha256(load('web-native-controls.json')['marker'].encode()).hexdigest(),
    'git_push': hashlib.sha256((load('git-agent.json')['marker'] + '\n').encode()).hexdigest()}
if before_files != expected_files:
    raise RuntimeError('Release baseline files differ from their accepted bytes')
before = snapshot()
save('release-state-before.json', before)
try:
    for name, images, default in [('upgrade', candidate, 'agent_compose'),
            ('image_rollback', baseline, 'agent_compose'), ('upgrade_again', candidate, 'agent_compose'),
            ('new_environment_route_rollback', candidate, 'taskflow')]:
        print('Testing isolated release step: ' + name, flush=True)
        deploy(images, default)
        assert_unchanged(before)
        if fingerprints() != before_files:
            raise RuntimeError('Release file contents changed')
        if default == 'taskflow':
            git_bridge()
        assert_unchanged(before)
        record['steps'][name] = {'passed': True, 'backend_image': images['backend'], 'default': default}
        save('release-report.json', record)
    # There is no Taskflow server in this fixture. A new request must fail,
    # never fall back to compose or consume its capacity. Do not retry it.
    owner, _, _ = login()
    if not record.get('missing_legacy_route_checked'):
        fixture = load('web-fixture.json')
        status, result = request(owner, '/api/v1/users/tasks', {'content': 'Missing legacy route ' + record['marker'],
            'host_id': fixture['node_id'], 'image_id': fixture['image_id'], 'model_id': fixture['model_id'],
            'cli_name': 'opencode', 'task_type': 'develop', 'resource': {'core': 1, 'memory': 2 << 30, 'life': 3600}})
        if status == 200 and result.get('code') == 0:
            raise RuntimeError('Missing legacy route accepted a new task')
        assert_unchanged(before)
        record['missing_legacy_route_checked'] = True
        save('release-report.json', record)
    # An explicit NEW user turn on an existing compose environment still
    # works under a Taskflow default. Stable prompt prevents replay on retry.
    member, _, jar = login('web-member-account.json')
    prompt = '只回复以下标记，不要使用工具：' + record['marker']
    with TaskSocket(git_task, jar, control=True), TaskSocket(git_task, jar) as stream:
        already = any(base64.b64encode(prompt.encode()) in base64.b64decode(json.loads(line).get('data', ''))
            for line in sql("SELECT chunk FROM runtime_events WHERE task_id='" + git_task + "' AND chunk->>'event'='user-input';").splitlines())
        previous = int(sql("SELECT max(turn) FROM runtime_commands WHERE task_id='" + git_task + "' AND operation='task';"))
        if not already:
            stream.send('user-input', {'content': base64.b64encode(prompt.encode()).decode(), 'attachments': []})
            wait(lambda: int(sql("SELECT max(turn) FROM runtime_commands WHERE task_id='" + git_task + "' AND operation='task';")) > previous, label='pinned compose follow-up')
        record['followup_turn'] = int(sql("SELECT max(turn) FROM runtime_commands WHERE task_id='" + git_task + "' AND operation='task';"))
        save('release-report.json', record)
    wait(lambda: sql("SELECT state FROM runtime_commands WHERE task_id='" + git_task + "' AND operation='task' AND turn=" + str(record['followup_turn']) + ';') == 'complete', 240, label='existing compose Worker after default rollback')
    if record['marker'] not in task_output(git_task):
        raise RuntimeError('Existing compose real-model follow-up missing')
    record['existing_compose_worker_after_default_rollback'] = True
    if fingerprints() != before_files:
        raise RuntimeError('Follow-up changed persistent files')
    after = snapshot()
    for key in ('environments', 'sessions', 'reservations'):
        if before[key] != after[key]:
            raise RuntimeError('Pinned follow-up changed existing ' + key)
    old_runs = {item['id']: item for item in before['runs']}
    new_runs = {item['id']: item for item in after['runs']}
    if any(new_runs.get(key) != value for key, value in old_runs.items()):
        raise RuntimeError('Pinned follow-up replayed or rewrote an old Run')
    added = [value for key, value in new_runs.items() if key not in old_runs]
    if len(added) != 1 or added[0]['task'] != git_task or added[0]['turn'] != record['followup_turn'] or not added[0]['run']:
        raise RuntimeError('Pinned follow-up created unexpected Runs')
    record['no_old_run_replayed'] = True
    save('release-report.json', record)
finally:
    deploy(candidate, 'agent_compose')
    record['restored_candidate_compose_default'] = True
    save('release-report.json', record)
if fingerprints() != before_files:
    raise RuntimeError('Final candidate restoration changed files')
print('Immutable image upgrade/rollback, histories, files, session/Run identities, scoped Git bridge and existing compose Worker after default rollback passed.', flush=True)
