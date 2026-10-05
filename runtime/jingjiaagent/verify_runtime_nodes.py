"""Real local node heartbeat checks; optional restart operates only on this PoC daemon.

Credentials and identity details remain private. No model, Run or task is created.
"""
import argparse
import http.cookiejar
import json
import pathlib
import subprocess
import time
import urllib.error
import urllib.request
import uuid

parser = argparse.ArgumentParser()
parser.add_argument('--exercise-restart', action='store_true')
args = parser.parse_args()
from linux_web_common import state
node_id = str(uuid.UUID(json.loads((state / 'web-node.json').read_text())['id']))
token = (state / 'daemon.token').read_text().strip()
platform = 'http://127.0.0.1:47424'
node_url = 'http://127.0.0.1:47410/internal/jingjiaagent/node'
daemon = 'jingjiaagent-runtime-poc-daemon-1'


def client():
    return urllib.request.build_opener(urllib.request.ProxyHandler({}),
             urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))


def query(opener, url, body=None, headers=None):
    req = urllib.request.Request(url, data=json.dumps(body).encode() if body is not None else None,
                                 headers={'Content-Type': 'application/json', **(headers or {})})
    try:
        with opener.open(req, timeout=5) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as response:
        return response.code, {}


def api(opener, path, body=None):
    status, result = query(opener, platform + path, body)
    if status != 200 or result.get('code') != 0:
        raise SystemExit('Original API check failed: ' + path)
    return result['data']


def ledger():
    sql = ("SELECT json_build_object('instance_id',n.instance_id,'fingerprint',n.fingerprint,"
           "'ready',n.ready,'seen',extract(epoch from n.last_seen_at),'observation',n.observation,"
           "'host_cores',h.cores,'host_memory',h.memory,'host_arch',h.arch,'owner',h.user_id) "
           "FROM runtime_nodes n JOIN hosts h ON h.id=n.node_id WHERE n.node_id='" + node_id + "'")
    data = subprocess.check_output(['docker', 'exec', 'jingjiaagent-postgres-1', 'psql',
                                   '-U', 'postgres', '-d', 'jingjiaagent', '-tAc', sql])
    return json.loads(data)


def await_condition(fn, seconds=25):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        if fn():
            return
        time.sleep(0.5)
    raise SystemExit('Node heartbeat did not reach the expected state.')


anonymous = client()
for headers in ({}, {'Authorization': 'Bearer invalid'},
                {'Authorization': 'Bearer ' + token, 'Origin': 'https://untrusted.example'}):
    if query(anonymous, node_url, headers=headers)[0] != 401:
        raise SystemExit('Private node metadata authorization failed.')
status, actual = query(anonymous, node_url, headers={'Authorization': 'Bearer ' + token})
if status != 200 or actual.get('schema') != 'jingjiaagent.runtime.node.v1':
    raise SystemExit('Actual daemon node metadata was unavailable.')
docker_info = json.loads(subprocess.check_output(['docker', 'info', '--format', '{{json .}}']))
if (actual['cores'], actual['memory'], actual['arch']) != (docker_info['NCPU'], docker_info['MemTotal'], docker_info['Architecture']):
    raise SystemExit('Node capacity did not describe the real Docker host.')
if actual.get('memory_available_bytes', 0) > actual['memory']:
    raise SystemExit('Invalid node available memory.')
if actual.get('storage_available_bytes', 0) > actual.get('storage_total_bytes', 0):
    raise SystemExit('Invalid node available storage.')
before = ledger()
if not before['ready'] or (before['instance_id'], before['fingerprint']) != (actual['instance_id'], actual['fingerprint']):
    raise SystemExit('Business node ledger did not bind the actual daemon.')
if (before['host_cores'], before['host_memory'], before['host_arch']) != (actual['cores'], actual['memory'], actual['arch']):
    raise SystemExit('Original host metadata did not synchronize actual capacity.')
opener = client()
api(opener, '/api/v1/users/password-login', json.loads((state / 'web-account.json').read_text()))


def original_host_status():
    hosts = api(opener, '/api/v1/users/hosts')['hosts']
    item = next(host for host in hosts if host['id'] == node_id)
    if (item['cores'], item['memory'], item['arch']) != (actual['cores'], actual['memory'], actual['arch']):
        raise SystemExit('Original authorized host API lost actual capacity.')
    return item['status']


if original_host_status() != 'online':
    raise SystemExit('Original host API did not expose fresh heartbeat status.')
configuration=json.loads((state/'web/config/server/config.yaml').read_text())
installer_configured=bool(configuration['runtime'].get('installer_manifest_file'))
expected_personal=403 if installer_configured else 503
expected_script=403 if installer_configured else 503
for path,expected in (('/api/v1/users/hosts/install-command',expected_personal),('/api/v1/users/hosts/install?token=any',expected_script)):
    if query(opener, platform + path)[0] != expected:
        raise SystemExit('Compose runtime installer scope or unconfigured gate failed.')
manager = client()
api(manager, '/api/v1/teams/users/login', json.loads((state / 'web-account.json').read_text()))
status,result=query(manager, platform + '/api/v1/teams/hosts/install-command')
if installer_configured:
    if status!=200 or result.get('code')!=0 or 'taskflow' in result['data']['command'].lower():
        raise SystemExit('Configured team installer did not provide the compatible runtime command.')
elif status!=503:
    raise SystemExit('Team installer bypassed the unconfigured runtime gate.')
await_condition(lambda: ledger()['seen'] > before['seen'])
after = ledger()
if (after['instance_id'], after['fingerprint'], after['owner']) != (before['instance_id'], before['fingerprint'], before['owner']):
    raise SystemExit('Background heartbeat changed persistent identity or ownership.')
checks = {'private_metadata_auth': True, 'actual_docker_capacity': True,
          'original_host_metadata_sync': True, 'background_heartbeat_progress': True,
          'identity_and_owner_preserved': True,
          'configured_installer_scope' if installer_configured else 'legacy_installers_gated': True}
if args.exercise_restart:
    known = json.loads(subprocess.check_output(['docker', 'inspect', daemon]))[0]
    images = json.loads((state / 'images.json').read_text())
    if known['Config']['Labels'].get('com.docker.compose.project') != 'jingjiaagent-runtime-poc' or known['Image'] != images['daemon']:
        raise SystemExit('The target daemon is not the owned immutable-image PoC.')
    try:
        subprocess.run(['docker', 'stop', '--time', '15', daemon], check=True, stdout=subprocess.DEVNULL)
        await_condition(lambda: not ledger()['ready'] and original_host_status() == 'offline')
        offline = ledger()
        if offline['seen'] != after['seen'] and offline['seen'] < after['seen']:
            raise SystemExit('Offline observation rewrote node history.')
        if (offline['instance_id'], offline['fingerprint'], offline['owner']) != (before['instance_id'], before['fingerprint'], before['owner']):
            raise SystemExit('Node outage erased persistent identity or ownership.')
        checks['real_daemon_outage_offline'] = True
    finally:
        subprocess.run(['docker', 'start', daemon], check=True, stdout=subprocess.DEVNULL)
    await_condition(lambda: ledger()['ready'] and original_host_status() == 'online')
    _, restored = query(anonymous, node_url, headers={'Authorization': 'Bearer ' + token})
    if (restored['instance_id'], restored['fingerprint']) != (actual['instance_id'], actual['fingerprint']):
        raise SystemExit('Daemon restart changed the recorded runtime identity.')
    checks['daemon_restart_identity_and_recovery'] = True
report = {'checks': checks, 'sample': {'cores': actual['cores'], 'memory_bytes': actual['memory'],
          'available_memory_reported': 'memory_available_bytes' in actual,
          'runtime_storage_reported': 'storage_total_bytes' in actual},
          'boundary': 'Real daemon/Docker and original authenticated APIs. No model or Run submitted; capacity reservation, dynamic registration and cross-host deployment remain unaccepted.'}
(state / 'runtime-nodes-report.json').write_text(json.dumps(report, indent=2), encoding='utf-8')
print(json.dumps(report, ensure_ascii=False))
