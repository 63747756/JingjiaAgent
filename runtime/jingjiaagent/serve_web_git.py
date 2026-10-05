"""Serve a read-only authenticated smart HTTP Git fixture on local port 47593."""
import base64
import hmac
import http.server
import json
import os
import pathlib
import subprocess
import threading
import time
import urllib.parse

from linux_web_common import state
fixture = json.loads((state/'web-git.json').read_text(encoding='utf-8'))
expected = 'Basic ' + base64.b64encode((fixture['username']+':'+fixture['owner_token']).encode()).decode()
audit_lock = threading.Lock()


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        self.serve_git()

    def do_POST(self):
        self.serve_git()

    def serve_git(self):
        parsed = urllib.parse.urlsplit(self.path)
        valid_route = (self.command == 'GET' and parsed.path == '/fixture.git/info/refs' and
                       parsed.query == 'service=git-upload-pack') or (
                       self.command == 'POST' and parsed.path == '/fixture.git/git-upload-pack' and not parsed.query)
        if not valid_route:
            self.send_error(404)
            return
        if not hmac.compare_digest(self.headers.get('Authorization',''),expected):
            self.send_response(401)
            self.send_header('WWW-Authenticate','Basic realm="Local Git acceptance"')
            self.send_header('Content-Length','0')
            self.end_headers()
            return
        try:
            size = int(self.headers.get('Content-Length','0'))
            if size < 0 or size > 4*1024*1024:
                raise ValueError()
        except ValueError:
            self.send_error(413)
            return
        env = dict(os.environ, GIT_PROJECT_ROOT=str((state/'git-http'/'repos').resolve()),
                   GIT_HTTP_EXPORT_ALL='1', PATH_INFO=parsed.path, QUERY_STRING=parsed.query,
                   REQUEST_METHOD=self.command, CONTENT_TYPE=self.headers.get('Content-Type',''),
                   CONTENT_LENGTH=str(size), REMOTE_USER=fixture['username'])
        if self.headers.get('Git-Protocol'):
            env['HTTP_GIT_PROTOCOL'] = self.headers['Git-Protocol']
        try:
            result = subprocess.run(['git','http-backend'], input=self.rfile.read(size),
                                    env=env, capture_output=True, timeout=30,
                                    creationflags=subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0)
            headers, separator, body = result.stdout.partition(b'\r\n\r\n')
            if result.returncode or not separator:
                raise RuntimeError()
        except (subprocess.TimeoutExpired, RuntimeError):
            self.send_error(502)
            return
        status = 200
        fields = []
        for raw in headers.decode('ascii').splitlines():
            name, value = raw.split(':',1)
            if name.lower() == 'status':
                status = int(value.strip().split()[0])
            elif name.lower() not in ('content-length','connection','transfer-encoding'):
                fields.append((name,value.strip()))
        self.send_response(status)
        for name,value in fields:
            self.send_header(name,value)
        self.send_header('Content-Length',str(len(body)))
        self.end_headers()
        self.wfile.write(body)
        with audit_lock:
            with (state/'web-git-audit.jsonl').open('a',encoding='utf-8') as output:
                output.write(json.dumps({'at':time.time(),'method':self.command,'path':parsed.path,
                                         'status':status,'response_bytes':len(body)})+'\n')


server = http.server.ThreadingHTTPServer(('0.0.0.0',47593), Handler)
print('Local authenticated read-only Git fixture listening on port 47593.',flush=True)
server.serve_forever()
