"""Authenticated loopback MCP fixture for the original Web management flow.

This real test tool computes a result with an unpredictable receipt. It is not
a model simulator or a replacement for the product gateway/authorization.
Credentials and audit evidence stay in the ignored local PoC state directory.
"""
import argparse
import http.server
import json
import pathlib
import secrets
import threading
import uuid

from linux_web_common import state
state.mkdir(parents=True, exist_ok=True)
fixture_file = state / 'web-mcp.json'
if not fixture_file.exists():
    fixture_file.write_text(json.dumps({
        'name': 'Remote Runtime MCP PoC', 'tool': 'runtime_probe',
        'token': secrets.token_urlsafe(32), 'sync_token': secrets.token_urlsafe(32),
        'receipt': 'JINGJIAAGENT_WEB_MCP_' + uuid.uuid4().hex,
        'url': 'http://127.0.0.1:47592/mcp',
    }, indent=2), encoding='utf-8')
    fixture_file.chmod(0o600)
fixture = json.loads(fixture_file.read_text(encoding='utf-8'))
if 'team_receipt' not in fixture:
    fixture.update({'team_name': 'Remote Team MCP PoC', 'team_tool': 'team_runtime_probe',
                    'team_receipt': 'JINGJIAAGENT_WEB_TEAM_MCP_' + uuid.uuid4().hex, 'team_url': 'http://127.0.0.1:47592/team/mcp'})
    fixture_file.write_text(json.dumps(fixture, indent=2), encoding='utf-8')
    fixture_file.chmod(0o600)
config_file = state / 'web-mcp-config.json'
config_file.write_text(json.dumps({
    'enabled': True, 'url': 'http://127.0.0.1:47424',
    'token': fixture['sync_token'], 'upstream_timeout': '15s',
}, indent=2), encoding='utf-8')
config_file.chmod(0o600)
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--prepare', action='store_true')
args = parser.parse_args()
if args.prepare:
    print('Prepared private MCP fixture configuration; no credentials were printed.')
    raise SystemExit(0)

audit_lock = threading.Lock()


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        self.send_response(200 if self.path == '/health' else 405)
        self.end_headers()

    def do_POST(self):
        if self.path not in ('/mcp', '/team/mcp'):
            self.send_error(404)
            return
        if not secrets.compare_digest(self.headers.get('Authorization', ''), 'Bearer ' + fixture['token']):
            self.send_error(401)
            return
        try:
            size = int(self.headers.get('Content-Length', '0'))
            if size <= 0 or size > 65536:
                raise ValueError()
            request = json.loads(self.rfile.read(size))
            method = request['method']
            params = request.get('params', {})
        except (ValueError, KeyError, TypeError):
            self.send_error(400)
            return
        if method == 'notifications/initialized':
            self.send_response(202)
            self.end_headers()
            return
        team = self.path == '/team/mcp'
        tool = fixture['team_tool'] if team else fixture['tool']
        receipt = fixture['team_receipt'] if team else fixture['receipt']
        error = None
        if method == 'initialize':
            result = {'protocolVersion': params.get('protocolVersion', '2025-03-26'),
                      'capabilities': {'tools': {}}, 'serverInfo': {'name': 'isolated-web-mcp-proof', 'version': '1'}}
        elif method == 'tools/list':
            result = {'tools': [{'name': tool,
                                'description': 'Compute twice value and return the private acceptance receipt.',
                                'inputSchema': {'type': 'object', 'properties': {'value': {'type': 'integer'}},
                                                'required': ['value'], 'additionalProperties': False}}]}
        elif method == 'tools/call':
            value = params.get('arguments', {}).get('value')
            if params.get('name') != tool or type(value) is not int or abs(value) > 1000000:
                result, error = None, {'code': -32602, 'message': 'Invalid acceptance tool arguments'}
            else:
                result = {'content': [{'type': 'text', 'text': str(2*value) + ' ' + receipt}], 'isError': False}
                with audit_lock, (state / 'web-mcp-audit.jsonl').open('a', encoding='utf-8') as audit:
                    audit.write(json.dumps({'method': method, 'scope': 'team' if team else 'user', 'value': value, 'result': result}) + '\n')
        elif method == 'ping':
            result = {}
        else:
            result, error = None, {'code': -32601, 'message': 'Method not found'}
        response = {'jsonrpc': '2.0', 'id': request.get('id')}
        response['error' if error else 'result'] = error or result
        raw = json.dumps(response).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


print('Serving isolated authenticated MCP fixture at http://127.0.0.1:47592/mcp', flush=True)
http.server.ThreadingHTTPServer(('127.0.0.1', 47592), Handler).serve_forever()
