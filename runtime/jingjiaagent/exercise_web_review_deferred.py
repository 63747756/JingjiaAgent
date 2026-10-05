"""Verify deferred review on the named local PoC, without creating Agent work.

Uses disposable SQL GitBot fixtures and the real signed Webhook endpoints.
This verifies the unavailable boundary, not third-party Git integration/review.
"""
import hashlib
import hmac
import http.cookiejar
import json
import pathlib
import secrets
import subprocess
import urllib.error
import urllib.request
import uuid

from linux_web_common import state
origin = 'http://127.0.0.1:47424'
container = 'jingjiaagent-postgres-1'
client = urllib.request.build_opener(urllib.request.ProxyHandler({}),
    urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))


def sql(query):
    result = subprocess.run(['docker', 'exec', '-i', container, 'psql', '-U', 'postgres',
        '-d', 'jingjiaagent', '-v', 'ON_ERROR_STOP=1', '-tA'],
        input=query, text=True, capture_output=True)
    if result.returncode:
        raise SystemExit('Local review fixture SQL failed; diagnostics and credentials withheld.')
    return result.stdout.strip()


def request(route, payload=None, headers=None):
    req = urllib.request.Request(origin + route, data=payload, headers=headers or {}, method='POST' if payload is not None else 'GET')
    try:
        with client.open(req, timeout=20) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as response:
        return response.code, response.read()


def snapshot():
    return sql('SELECT json_build_array((SELECT count(*) FROM tasks),'
        '(SELECT count(*) FROM virtualmachines),(SELECT count(*) FROM runtime_environments),'
        '(SELECT count(*) FROM runtime_task_intents),(SELECT count(*) FROM runtime_commands));')


if sql('SELECT current_database();') != 'jingjiaagent':
    raise SystemExit('Requires the named isolated Web PoC database.')
account = json.loads((state / 'web-account.json').read_text())
status, body = request('/api/v1/users/password-login', json.dumps(account).encode(),
    {'Content-Type': 'application/json'})
if status != 200 or json.loads(body).get('code') != 0:
    raise SystemExit('Original local login failed; credentials withheld.')
owner = str(uuid.UUID(json.loads(body)['data']['id']))
node = json.loads((state / 'web-fixture.json').read_text())['node_id']
if not node or len(node) > 64 or any(c not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-' for c in node):
    raise SystemExit('Unexpected local node ID.')
status, body = request('/api/v1/users/hosts')
hosts = json.loads(body) if status == 200 else {}
if hosts.get('code') != 0 or node not in [host['id'] for host in hosts.get('data', {}).get('hosts', [])]:
    raise SystemExit('Original host API did not authorize the local fixture node.')
before = snapshot()
report = {'ordinary_login_unchanged': True, 'platforms': {}, 'third_party_git_review_verified': False}
payload = json.dumps({'action': 'opened', 'fixture': 'automatic-review-deferred'}).encode()
for platform in ['github', 'gitlab', 'gitee', 'gitea', 'codeup']:
    bot = str(uuid.uuid4())
    secret = secrets.token_hex(32)
    try:
        sql("INSERT INTO git_bots(id,user_id,name,host_id,platform,secret_token,token) VALUES('" + bot +
            "','" + owner + "','Deferred review boundary fixture','" + node + "','" + platform + "','" + secret + "','');")
        headers = {'Content-Type': 'application/json'}
        signature = hmac.new(secret.encode(), payload, hashlib.sha256).hexdigest()
        if platform == 'github':
            event_header, signature_header, valid = 'X-Github-Event', 'X-Hub-Signature-256', 'sha256=' + signature
            headers[event_header] = 'pull_request'
        elif platform == 'gitea':
            event_header, signature_header, valid = 'X-Gitea-Event', 'X-Gitea-Signature', signature
            headers[event_header] = 'pull_request'
        else:
            event_header, signature_header, valid = {
                'gitlab': ('X-Gitlab-Event', 'X-Gitlab-Token', secret),
                'gitee': ('X-Gitee-Event', 'X-Gitee-Token', secret),
                'codeup': ('X-Event-Type', 'X-Codeup-Token', secret)}[platform]
            headers[event_header] = 'Merge Request Hook'
        route = '/api/v1/' + platform + '/webhook/' + bot
        headers[signature_header] = valid
        status, response = request(route, payload, headers)
        if status != 503 or b'automatic PR/MR review is unavailable' not in response:
            raise SystemExit('Deferred signed review was not explicitly rejected: ' + platform)
        headers[signature_header] = 'invalid'
        if request(route, payload, headers)[0] != 401:
            raise SystemExit('Deferred review bypassed Webhook authentication: ' + platform)
        headers[signature_header], headers[event_header] = valid, 'ping'
        if request(route, payload, headers)[0] != 200:
            raise SystemExit('Non-review event handling changed: ' + platform)
        report['platforms'][platform] = {'signed_review_unavailable_503': True,
            'invalid_signature_401': True, 'non_review_ping_200': True}
    finally:
        # Only the newly generated fixture; retain its recoverable soft-delete
        # marker and erase disposable credentials. No tasks/repositories removed.
        sql("UPDATE git_bots SET deleted_at=now(),token='',secret_token='' WHERE id='" + bot + "';")
if snapshot() != before:
    raise SystemExit('Deferred review created business/runtime work.')
report['no_business_or_runtime_work_created'] = True
(state / 'web-review-deferred-report.json').write_text(json.dumps(report, indent=2), encoding='utf-8')
print(json.dumps(report, ensure_ascii=False))
