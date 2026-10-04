"""Original API + real Docker Guest acceptance; only a named local test task."""
import argparse
import base64
import hashlib
import http.client
import http.cookiejar
from http.cookies import SimpleCookie
import json
import os
import pathlib
import re
import secrets
import socket
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

from preview_fixture import BINARY, frame, read_frame

root = pathlib.Path(__file__).resolve().parent
state = pathlib.Path(os.environ.get('RUNTIME_WEB_STATE_DIRECTORY', str(root / '.state'))).resolve()
if not state.is_relative_to((root / '.state').resolve()):
    raise SystemExit('Preview fixture state must stay inside the private acceptance directory')
origin = os.environ.get('RUNTIME_WEB_BASE_URL', 'http://127.0.0.1:47420')
linux = origin == 'http://127.0.0.1:47424'
if origin not in ('http://127.0.0.1:47420', 'http://127.0.0.1:47424'):
    raise SystemExit('Only the two named local acceptance APIs are allowed')
preview_port = 47425 if linux else 47421
if linux:
    from linux_web_common import database as linux_database
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--prepare', action='store_true')
parser.add_argument('--verify', action='store_true')
parser.add_argument('--task', help='Name the original administrator test task on first preparation')
args = parser.parse_args()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *_):
        return None


def logged(name):
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    result = api(client, '/api/v1/users/password-login', json.loads((state / name).read_text()))[1]
    if result.get('code') != 0:
        raise SystemExit('Original test login failed')
    return client


def api(client, route, data=None, method=None):
    req = urllib.request.Request(origin + route, data=json.dumps(data).encode() if data is not None else None,
        headers={'Content-Type': 'application/json'}, method=method)
    try:
        with client.open(req, timeout=25) as response:
            return response.status, json.load(response), dict(response.headers)
    except urllib.error.HTTPError as response:
        raw = response.read()
        try:
            value = json.loads(raw)
        except (ValueError, UnicodeError):
            value = {}
        return response.code, value, dict(response.headers)


def success(client, route, data=None, method=None):
    status, result, _ = api(client, route, data, method)
    if status != 200 or result.get('code') != 0:
        raise SystemExit('Original preview API failed: ' + route)
    return result.get('data')


def request_preview(host, path='/', cookie=None, data=None, headers=None):
    conn = http.client.HTTPConnection('127.0.0.1', preview_port, timeout=15)
    outgoing = {'Host': host}
    if cookie:
        outgoing['Cookie'] = cookie
    outgoing.update(headers or {})
    conn.request('POST' if data is not None else 'GET', path, body=data, headers=outgoing)
    response = conn.getresponse()
    value = (response.status, response.read(), dict(response.headers))
    conn.close()
    return value


def admission(client, forward):
    status, _, headers = api(client, '/api/v1/runtime/previews/' + forward)
    if status != 303:
        raise SystemExit('Original authenticated preview admission failed')
    location = urllib.parse.urlparse(headers['Location'])
    expected = forward + '.localhost:' + str(preview_port)
    if location.scheme != 'http' or location.netloc != expected:
        raise SystemExit('Preview did not use isolated configured host')
    status, _, headers = request_preview(expected, location.path + '?' + location.query)
    if status != 303:
        raise SystemExit('One-use preview ticket redemption failed')
    cookies = SimpleCookie(headers.get('Set-Cookie'))
    cookie = 'monkeycode_preview=' + cookies['monkeycode_preview'].value
    if request_preview(expected, location.path + '?' + location.query)[0] != 401:
        raise SystemExit('Preview ticket replay accepted')
    return expected, cookie


def websocket(host, cookie):
    conn = socket.create_connection(('127.0.0.1', preview_port), timeout=8)
    key = base64.b64encode(secrets.token_bytes(16)).decode()
    request = f'GET /ws HTTP/1.1\r\nHost: {host}\r\nOrigin: http://{host}\r\nCookie: {cookie}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n'
    conn.sendall(request.encode())
    header = b''
    while not header.endswith(b'\r\n\r\n'):
        header += conn.recv(1)
        if len(header) > 8192:
            raise SystemExit('Invalid WebSocket response')
    if not header.startswith(b'HTTP/1.1 101'):
        conn.close()
        raise SystemExit('Actual sandbox WebSocket upgrade failed')
    return conn, conn.makefile('rb')


owner = logged('web-account.json')
client_ip = '127.0.0.1'
if linux:
    status, value, _ = api(owner, '/api/v1/users/hosts/client-ip')
    if status != 200 or not value.get('ip'):
        raise SystemExit('Authenticated client IP could not be determined')
    client_ip = value['ip']
saved_path = state / 'web-preview-task.json'
if not saved_path.exists():
    if not args.prepare or not args.task:
        raise SystemExit('First preparation requires --task naming the isolated administrator task')
    selected = str(uuid.UUID(args.task))
    selected_task = success(owner, '/api/v1/users/tasks/' + selected)
    saved_path.write_text(json.dumps({'task': selected, 'content': selected_task['content']}, ensure_ascii=False, indent=2), encoding='utf-8')
saved = json.loads(saved_path.read_text(encoding='utf-8'))
task_id = str(uuid.UUID(saved['task']))
task = success(owner, '/api/v1/users/tasks/' + task_id)
if task['content'] != saved['content']:
    raise SystemExit('Only the named local acceptance task may host this fixture')
vm = task['virtualmachine']
env, host_id = vm['id'], vm['host']['id']
if not re.fullmatch('agent_[a-f0-9-]{36}', env):
    raise SystemExit('Unexpected test environment')
route = '/api/v1/users/hosts/' + urllib.parse.quote(host_id) + '/vms/' + urllib.parse.quote(env) + '/ports'
config_path = state / 'web-preview.json'
if args.prepare:
    mapping = subprocess.check_output(['docker', 'exec', linux_database if linux else 'jingjia-runtime-tests-20261002', 'psql', '-U', 'postgres',
        '-d', 'monkeycode' if linux else 'monkeycode_web_poc', '-tAc', "SELECT sandbox_id FROM runtime_environments WHERE id='" + env + "' AND state='online'"], text=True).strip()
    if not re.fullmatch('[a-f0-9]{64}', mapping):
        raise SystemExit('Named sandbox is not online')
    container = 'agent-compose-' + mapping[:12]
    labels = json.loads(subprocess.check_output(['docker', 'inspect', container]))[0]['Config']['Labels']
    if labels.get('agent-compose.sandbox_id') != mapping:
        raise SystemExit('Container ownership does not match test environment')
    if not config_path.exists():
        config_path.write_text(json.dumps({'receipt': 'preview-fixture-' + secrets.token_hex(16), 'task': task_id,
            'environment': env, 'container': container}, indent=2))
    config = json.loads(config_path.read_text())
    if config['environment'] != env or config['container'] != container:
        raise SystemExit('Preview fixture belongs to another environment')
    healthy = subprocess.run(['docker', 'exec', container, 'python3', '-c',
        "import urllib.request,json; assert json.load(urllib.request.urlopen('http://127.0.0.1:47880/receipt',timeout=2))['receipt']==json.load(open('/tmp/monkeycode-preview-fixture.json'))['receipt']"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0
    if not healthy:
        subprocess.run(['docker', 'cp', str(root / 'preview_fixture.py'), container + ':/tmp/monkeycode-preview-fixture.py'], check=True, stdout=subprocess.DEVNULL)
        subprocess.run(['docker', 'cp', str(config_path), container + ':/tmp/monkeycode-preview-fixture.json'], check=True, stdout=subprocess.DEVNULL)
        subprocess.run(['docker', 'exec', '-d', container, 'python3', '/tmp/monkeycode-preview-fixture.py'], check=True)
    detected = success(owner, route)
    if 47880 not in [p['port'] for p in detected] or 47881 not in [p['port'] for p in detected]:
        raise SystemExit('Original port discovery did not detect actual Guest servers')
    print(json.dumps({'prepared': True, 'task_url': (origin if linux else 'http://127.0.0.1:47430') + '/console/task/' + task_id, 'ports': [47880, 47881]}))

if args.verify:
    report = {}
    opened = {p['port']: p for p in success(owner, route)}
    if not opened.get(47880, {}).get('forward_id'):
        raise SystemExit('Open port 47880 using the original Web before API verification')
    first = opened[47880]
    original_whitelist = [client_ip] if linux else (first.get('white_list') or [client_ip])
    if linux:
        success(owner, route, {'port': 47880, 'forward_id': first['forward_id'], 'white_list': original_whitelist})
    second = success(owner, route, {'port': 47881, 'white_list': [client_ip]})
    before = {p['port']:(p.get('forward_id'), p.get('white_list')) for p in success(owner, route)}
    outsider = logged('web-outsider-account.json')
    for operation, data, method in [
        ('list', None, None), ('create', {'port': 47880, 'white_list': [client_ip]}, 'POST'),
        ('update', {'port': 47880, 'forward_id': first['forward_id'], 'white_list': [client_ip]}, 'POST')]:
        status, result, _ = api(outsider, route, data, method)
        if status != 200 or result.get('code') != 10207:
            raise SystemExit('Cross-user preview operation was not refused: ' + operation)
        report['foreign_' + operation + '_denied'] = True
    status, result, _ = api(outsider, route + '/47880', {'forward_id': first['forward_id']}, 'DELETE')
    if status != 200 or result.get('code') != 10208:
        raise SystemExit('Cross-user preview close was not refused')
    report['foreign_close_denied'] = True
    after = {p['port']:(p.get('forward_id'), p.get('white_list')) for p in success(owner, route)}
    if before != after:
        raise SystemExit('Denied preview requests changed forward state')
    report['denied_requests_no_forward_changes'] = True
    if api(outsider, '/api/v1/runtime/previews/' + first['forward_id'])[0] != 403:
        raise SystemExit('Copied preview URL admitted another user')
    report['copied_url_user_scope'] = True
    host, cookie = admission(owner, first['forward_id'])
    status, body, _ = request_preview(host, '/binary', cookie)
    if status != 200 or body != BINARY:
        raise SystemExit('Binary response corrupted')
    status, body, _ = request_preview(host, '/echo?q=%E4%B8%AD', cookie, BINARY)
    if status != 200 or body != BINARY:
        raise SystemExit('Binary request corrupted')
    report['binary_request_response_sha256'] = hashlib.sha256(BINARY).hexdigest()
    status, body, _ = request_preview(host, '/headers', cookie)
    headers = json.loads(body)
    if 'monkeycode_preview' in headers.get('Cookie', '') or 'Bearer ' in headers.get('Authorization', ''):
        raise SystemExit('Control plane credential reached Guest application')
    report['control_credentials_filtered'] = True
    if request_preview(host, '/receipt', cookie, headers={'Origin': 'http://other.localhost:' + str(preview_port)})[0] != 403:
        raise SystemExit('Cross-preview Origin accepted')
    sibling = second['forward_id'] + '.localhost:' + str(preview_port)
    if request_preview(sibling, '/receipt', cookie)[0] != 401 or request_preview(host)[0] != 401:
        raise SystemExit('Preview host or authentication isolation failed')
    report['cross_host_origin_and_anonymous_denied'] = True
    stream = http.client.HTTPConnection('127.0.0.1', preview_port, timeout=8)
    stream.request('GET', '/stream', headers={'Host': host, 'Cookie': cookie})
    response = stream.getresponse()
    started = time.monotonic()
    if response.readline() != b'data: first\n' or time.monotonic() - started > .7:
        raise SystemExit('HTTP stream was buffered')
    response.read();stream.close()
    report['stream_first_chunk_before_completion'] = True
    conn, reader = websocket(host, cookie)
    for kind, data in [(1, '中文 WebSocket 验收'.encode()), (2, BINARY[:80000])]:
        conn.sendall(frame(kind, data, b'abcd'))
        got_kind, got = read_frame(reader)
        if got_kind != kind or got != data:
            raise SystemExit('Actual WebSocket text/binary echo corrupted')
    report['websocket_text_binary'] = True
    conn.close();reader.close()
    conn, reader = websocket(host, cookie)
    success(owner, route, {'port': 47880, 'forward_id': first['forward_id'], 'white_list': ['192.0.2.1']})
    try:
        read_frame(reader)
        raise SystemExit('Whitelist change left WebSocket usable')
    except (EOFError, ConnectionResetError):
        report['whitelist_revokes_live_websocket'] = True
    finally:
        conn.close();reader.close()
    if request_preview(host, '/receipt', cookie, headers={'X-Forwarded-For': '192.0.2.1'})[0] != 403:
        raise SystemExit('Old grant or spoofed whitelist remained usable')
    report['old_cookie_revoked'] = True
    success(owner, route, {'port': 47880, 'forward_id': first['forward_id'], 'white_list': [client_ip]})
    host, cookie = admission(owner, first['forward_id'])
    conn, reader = websocket(host, cookie)
    success(owner, route + '/47880', {'forward_id': first['forward_id']}, 'DELETE')
    try:
        read_frame(reader)
        raise SystemExit('Closed port left WebSocket usable')
    except (EOFError, ConnectionResetError):
        report['close_revokes_live_websocket'] = True
    finally:
        conn.close();reader.close()
    if request_preview(host, '/receipt', cookie)[0] != 403:
        raise SystemExit('Closed port still accessible')
    success(owner, route, {'port': 47880, 'white_list': original_whitelist})
    host, cookie = admission(owner, first['forward_id'])
    if request_preview(host, '/receipt', cookie)[0] != 200:
        raise SystemExit('Port reopen/reconnect failed')
    report['reopen_reconnect'] = True
    success(owner, route + '/47881', {'forward_id': second['forward_id']}, 'DELETE')
    config = json.loads(config_path.read_text())
    config['forward_id'] = first['forward_id']
    config_path.write_text(json.dumps(config, indent=2))
    (state / 'web-preview-report.json').write_text(json.dumps(report, indent=2))
    print(json.dumps(report, indent=2))
