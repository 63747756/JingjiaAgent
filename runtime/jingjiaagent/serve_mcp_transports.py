"""Private real MCP transport fixtures: persistent Streamable HTTP and legacy SSE.

No model simulation. Each tool computes a result and returns an unpredictable
receipt. Credentials/session evidence remain in ignored local state.
"""
import argparse
import http.server
import json
import os
import pathlib
import queue
import secrets
import select
import threading
import time
import urllib.parse
import uuid

from linux_web_common import state
path = state / 'mcp-transports.json'
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--prepare', action='store_true')
args = parser.parse_args()
if not path.exists():
    base_url = os.environ.get('JINGJIAAGENT_TEST_MCP_BASE_URL', 'http://127.0.0.1:47594').rstrip('/')
    path.write_text(json.dumps({'token': secrets.token_urlsafe(32),
        'stream_receipt': 'STREAM_MCP_' + uuid.uuid4().hex,
        'legacy_receipt': 'LEGACY_MCP_' + uuid.uuid4().hex,
        'stream_url': base_url + '/stream/mcp',
        'legacy_url': base_url + '/legacy/sse'}, indent=2))
    path.chmod(0o600)
fixture = json.loads(path.read_text())
if args.prepare:
    print('Prepared private MCP transport fixtures.')
    raise SystemExit(0)

sessions, lock = {}, threading.Lock()
audit_path = state / 'mcp-transports-audit.jsonl'


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *_):
        pass

    def authorized(self):
        if secrets.compare_digest(self.headers.get('Authorization', ''), 'Bearer ' + fixture['token']):
            return True
        self.status(401)
        return False

    def status(self, code):
        self.send_response(code)
        self.send_header('Content-Length', '0')
        self.end_headers()

    def json(self, result, session=None):
        raw = json.dumps(result).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(raw)))
        if session:
            self.send_header('Mcp-Session-Id', session)
        self.end_headers()
        self.wfile.write(raw)

    def event(self, data, event='message'):
        self.wfile.write(('event: ' + event + '\ndata: ' + data + '\n\n').encode())
        self.wfile.flush()

    def disconnected(self):
        readable, _, _ = select.select([self.connection], [], [], .1)
        return bool(readable) and self.connection.recv(1, 2) == b''

    def do_GET(self):
        if self.path == '/health':
            self.status(200)
            return
        if not self.authorized():
            return
        if self.path != '/legacy/sse':
            self.status(405)
            return
        sid, events = uuid.uuid4().hex, queue.Queue()
        with lock:
            sessions[sid] = {'transport': 'legacy', 'initialized': False, 'queue': events}
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.send_header('Cache-Control', 'no-cache')
        self.send_header('Connection', 'close')
        self.end_headers()
        self.close_connection = True
        try:
            self.event('/legacy/messages?session=' + sid, 'endpoint')
            while True:
                try:
                    self.event(json.dumps(events.get(timeout=.1)))
                except queue.Empty:
                    if self.disconnected():
                        break
        except (ConnectionError, OSError):
            pass
        finally:
            with lock:
                sessions.pop(sid, None)

    def do_DELETE(self):
        if not self.authorized():
            return
        sid = self.headers.get('Mcp-Session-Id')
        with lock:
            removed = sessions.pop(sid, None)
        self.status(204 if removed else 404)

    def do_POST(self):
        if not self.authorized():
            return
        # Consume POST bodies even when selecting the older transport.
        try:
            size = int(self.headers.get('Content-Length', '0'))
            if not 0 < size <= 65536:
                raise ValueError()
            request = json.loads(self.rfile.read(size))
            method, params = request['method'], request.get('params', {})
        except (ValueError, KeyError, TypeError):
            self.status(400)
            return
        if self.path == '/legacy/sse':
            self.status(405)
            return
        legacy = urllib.parse.urlsplit(self.path).path == '/legacy/messages'
        if not legacy and self.path != '/stream/mcp':
            self.status(404)
            return
        sid = (urllib.parse.parse_qs(urllib.parse.urlsplit(self.path).query).get('session') or [''])[0] if legacy else self.headers.get('Mcp-Session-Id', '')
        if method == 'initialize' and not legacy:
            sid = uuid.uuid4().hex
            with lock:
                sessions[sid] = {'transport': 'stream', 'initialized': False}
        with lock:
            session = sessions.get(sid)
        if not session or session['transport'] != ('legacy' if legacy else 'stream'):
            self.status(404)
            return
        if method == 'notifications/initialized':
            with lock:
                session['initialized'] = True
            self.status(202)
            return
        if method != 'initialize' and not session['initialized']:
            self.status(409)
            return
        tool = 'legacy_transport_probe' if legacy else 'stream_transport_probe'
        receipt = fixture['legacy_receipt' if legacy else 'stream_receipt']
        error = None
        if method == 'initialize':
            result = {'protocolVersion': params.get('protocolVersion', '2025-03-26'),
                'capabilities': {'tools': {}}, 'serverInfo': {'name': 'transport-proof', 'version': '1'}}
        elif method == 'tools/list':
            result = {'tools': [{'name': tool, 'description': 'Compute twice value and return a private transport receipt.',
                'inputSchema': {'type': 'object', 'properties': {'value': {'type': 'integer'}}, 'required': ['value'], 'additionalProperties': False}}]}
        elif method == 'tools/call':
            value = params.get('arguments', {}).get('value')
            if params.get('name') != tool or type(value) is not int or abs(value) > 1000000:
                result, error = None, {'code': -32602, 'message': 'Invalid acceptance arguments'}
            else:
                time.sleep(.15) # Concurrent calls overlap at the real upstream.
                result = {'content': [{'type': 'text', 'text': str(value * 2) + ' ' + receipt}], 'isError': False}
                with lock, audit_path.open('a') as out:
                    out.write(json.dumps({'transport': 'legacy' if legacy else 'stream', 'session': sid,
                        'request_id': request.get('id'), 'value': value, 'receipt': receipt, 'time': time.time()}) + '\n')
        elif method == 'ping':
            result = {}
        else:
            result, error = None, {'code': -32601, 'message': 'Method not found'}
        response = {'jsonrpc': '2.0', 'id': request.get('id'), 'error' if error else 'result': error or result}
        if legacy:
            self.status(202)
            session['queue'].put({'jsonrpc': '2.0', 'method': 'notifications/message', 'params': {'level': 'info', 'data': 'Fixture progress'}})
            session['queue'].put(response)
        elif method == 'initialize':
            self.json(response, sid)
        else:
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.send_header('X-Request-Id', uuid.uuid4().hex)
            self.send_header('Connection', 'close')
            self.end_headers()
            self.close_connection = True
            try:
                self.event(json.dumps({'jsonrpc': '2.0', 'method': 'notifications/message', 'params': {'level': 'info', 'data': 'Fixture progress'}}))
                self.event(json.dumps(response))
                deadline = time.monotonic() + 20
                while time.monotonic() < deadline and not self.disconnected():
                    pass
            except (ConnectionError, OSError):
                pass


listen = os.environ.get('JINGJIAAGENT_TEST_MCP_LISTEN', '127.0.0.1')
print('Serving real MCP transport fixtures on the configured acceptance interface.', flush=True)
http.server.ThreadingHTTPServer((listen, 47594), Handler).serve_forever()
