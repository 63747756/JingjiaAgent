"""Helpers restricted to the independent Linux Web acceptance fixture."""
import http.cookiejar
import base64
import json
import pathlib
import os
import re
import secrets
import socket
import subprocess
import time
import urllib.error
import urllib.request

root = pathlib.Path(__file__).resolve().parent
state = pathlib.Path(os.environ.get('JINGJIAAGENT_RUNTIME_WEB_STATE_DIRECTORY',str(root / '.state/linux-web'))).resolve()
project = os.environ.get('JINGJIAAGENT_RUNTIME_WEB_PROJECT','jingjiaagent')
if not state.is_relative_to((root / '.state').resolve()) or not re.fullmatch(r'[a-z0-9][a-z0-9_-]{0,62}',project):
    raise SystemExit('Invalid private state directory or Compose project name.')
origin = 'http://127.0.0.1:47424'
database = project+'-postgres-1'

def save(name, value):
    path = state / name
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2), encoding='utf-8')
    path.chmod(0o600)

def load(name):
    return json.loads((state / name).read_text(encoding='utf-8'))

def sql(query):
    return subprocess.check_output(['docker', 'exec', '-i', database, 'psql', '-v', 'ON_ERROR_STOP=1',
        '-U', 'postgres', '-d', 'jingjiaagent', '-At'], input=query, text=True, encoding='utf-8').strip()

def client():
    jar = http.cookiejar.CookieJar()
    return urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar)), jar

def request(http, route, data=None, method=None):
    req = urllib.request.Request(origin + route,
        data=json.dumps(data).encode() if data is not None else None,
        headers={'Content-Type': 'application/json'}, method=method)
    try:
        with http.open(req, timeout=60) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        try:
            result = json.load(error)
        except ValueError:
            result = {}
        return error.code, result

def api(http, route, data=None, method=None):
    status, result = request(http, route, data, method)
    if status != 200 or result.get('code') != 0:
        # API diagnostics and successful identity responses may contain credentials.
        raise RuntimeError(f'{route}: HTTP {status}, code {result.get("code")}')
    return result.get('data')

def login(filename='web-account.json', team=False):
    http, jar = client()
    user = api(http, '/api/v1/teams/users/login' if team else '/api/v1/users/password-login', load(filename))
    return http, user, jar

def wait(predicate, seconds=180, label='acceptance condition'):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        result = predicate()
        if result:
            return result
        time.sleep(.5)
    raise RuntimeError(label + ' did not become ready')

def runtime_rpc(service, method, data):
    token = (state/'daemon.token').read_text().strip()
    req = urllib.request.Request('http://127.0.0.1:47418/agentcompose.v2.'+service+'/'+method,
        data=json.dumps(data).encode(),headers={'Content-Type':'application/json','Authorization':'Bearer '+token})
    http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with http.open(req,timeout=30) as response:
        return json.load(response)


def task_output(task_id):
    output = ''
    for line in sql("SELECT chunk FROM runtime_events WHERE task_id='"+str(__import__('uuid').UUID(task_id))+"' ORDER BY seq;").splitlines():
        chunk = json.loads(line)
        if chunk.get('event') != 'task-running':
            continue
        data = base64.b64decode(chunk.get('data','')).decode('utf-8',errors='replace')
        if chunk.get('kind') == 'acp_event':
            update = json.loads(data).get('update',{})
            if update.get('sessionUpdate') == 'agent_message_chunk':
                output += update.get('content',{}).get('text','')
        else:
            output += data
    return output


class TaskSocket:
    """Original product WebSocket, isolated local API and existing login cookie."""
    def __init__(self, task_id, jar, control=False):
        import uuid
        from preview_fixture import frame, read_frame
        self.frame, self.read_frame = frame, read_frame
        self.socket = socket.create_connection(('127.0.0.1',47424),timeout=15)
        self.reader = self.socket.makefile('rb')
        route = '/api/v1/users/tasks/'+('control' if control else 'stream')+'?id='+str(uuid.UUID(task_id))
        if not control:route += '&mode=new'
        key = base64.b64encode(secrets.token_bytes(16)).decode()
        cookie = '; '.join(item.name+'='+item.value for item in jar)
        request = f'GET {route} HTTP/1.1\r\nHost: 127.0.0.1:47424\r\nOrigin: {origin}\r\nCookie: {cookie}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n'
        self.socket.sendall(request.encode())
        header = b''
        while not header.endswith(b'\r\n\r\n'):
            value = self.reader.read(1)
            if not value or len(header)>8192:
                self.close()
                raise RuntimeError('Original WebSocket handshake unavailable')
            header += value
        if not header.startswith(b'HTTP/1.1 101'):
            self.close()
            raise RuntimeError('Original WebSocket access rejected')
    def send(self, kind, data, subtype=''):
        message = {'type':kind,'data':base64.b64encode(json.dumps(data,ensure_ascii=False).encode()).decode(),'kind':subtype}
        self.socket.sendall(self.frame(1,json.dumps(message).encode(),secrets.token_bytes(4)))
    def call(self, kind, data):
        request_id = secrets.token_hex(12)
        self.send('call',dict(data,request_id=request_id),kind)
        while True:
            frame_kind, raw = self.read_frame(self.reader)
            if frame_kind == 9:
                self.socket.sendall(self.frame(10,raw,secrets.token_bytes(4)))
                continue
            if frame_kind != 1:
                raise RuntimeError('Original task control disconnected')
            reply = json.loads(raw)
            if reply.get('type') == 'call-response':
                value = json.loads(base64.b64decode(reply['data']))
                if value.get('request_id') == request_id:
                    if not value.get('success'):
                        raise RuntimeError('Original control call failed: '+kind)
                    return value
    def close(self):
        self.socket.close()
        self.reader.close()
    def __enter__(self):return self
    def __exit__(self,*_):self.close()
