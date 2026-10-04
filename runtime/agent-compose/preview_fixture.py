"""Loopback-only HTTP/WS fixture inside an explicitly named local test Guest."""
import base64
import hashlib
import http.server
import json
import pathlib
import socket
import struct
import threading
import time

BINARY = bytes([0, 255, 13, 10, 42]) * 300000
FIXTURE = pathlib.Path('/tmp/monkeycode-preview-fixture.json')


def exact(stream, size):
    data = stream.read(size)
    if len(data) != size:
        raise EOFError()
    return data


def read_frame(stream):
    first, second = exact(stream, 2)
    length = second & 127
    if length == 126:
        length = struct.unpack('!H', exact(stream, 2))[0]
    elif length == 127:
        length = struct.unpack('!Q', exact(stream, 8))[0]
    if length > 2 * 1024 * 1024:
        raise ValueError('fixture message exceeds limit')
    mask = exact(stream, 4) if second & 128 else b''
    data = exact(stream, length)
    if mask:
        data = bytes(value ^ mask[index % 4] for index, value in enumerate(data))
    return first & 15, data


def frame(kind, data, mask=b''):
    size = len(data)
    header = bytes([128 | kind])
    marker = 128 if mask else 0
    if size < 126:
        header += bytes([marker | size])
    elif size <= 65535:
        header += bytes([marker | 126]) + struct.pack('!H', size)
    else:
        header += bytes([marker | 127]) + struct.pack('!Q', size)
    if mask:
        data = bytes(value ^ mask[index % 4] for index, value in enumerate(data))
    return header + mask + data


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *_):
        pass

    def reply(self, data, content_type='application/octet-stream'):
        self.send_response(200)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        route = self.path.split('?')[0]
        if route == '/ws':
            key = self.headers.get('Sec-WebSocket-Key', '')
            if not key or self.headers.get('Upgrade', '').lower() != 'websocket':
                self.send_error(400)
                return
            accept = base64.b64encode(hashlib.sha1((key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest()).decode()
            self.send_response(101)
            self.send_header('Upgrade', 'websocket')
            self.send_header('Connection', 'Upgrade')
            self.send_header('Sec-WebSocket-Accept', accept)
            self.end_headers()
            self.wfile.flush()
            try:
                while True:
                    kind, data = read_frame(self.rfile)
                    if kind == 8:
                        break
                    if kind == 9:
                        kind = 10
                    self.wfile.write(frame(kind, data))
                    self.wfile.flush()
            except (EOFError, OSError, ValueError):
                pass
            self.close_connection = True
        elif route == '/':
            page = """<!doctype html><html lang="zh-CN"><meta charset="utf-8">
<title>远程预览验收</title><style>body{font:18px system-ui;margin:48px;background:#f4f6fa;color:#18243b}main{max-width:760px;padding:32px;border-radius:16px;background:white}li{margin:16px 0}</style>
<main><h1>远程预览验收</h1><p>当前页面来自实际沙箱中的回环端口。</p>
<ul><li id="http">HTTP：已连接</li><li id="asset">动态资源：等待</li><li id="ws">WebSocket：等待</li></ul>
<button id="reconnect">重新连接 WebSocket</button><p id="closed"></p></main><script src="/asset.js"></script></html>"""
            self.reply(page.encode(), 'text/html; charset=utf-8')
        elif route == '/asset.js':
            script = """
document.getElementById('asset').textContent='动态资源：已通过';
function connect(){let ws=new WebSocket((location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/ws');
ws.onopen=()=>ws.send('中文预览验收');
ws.onmessage=e=>{document.getElementById('ws').textContent=e.data==='中文预览验收'?'WebSocket：已通过':'WebSocket：内容异常';document.getElementById('closed').textContent=''};
ws.onclose=()=>document.getElementById('closed').textContent='WebSocket：连接已关闭';}
document.getElementById('reconnect').onclick=connect;connect();
"""
            self.reply(script.encode(), 'application/javascript; charset=utf-8')
        elif route == '/receipt':
            self.reply(json.dumps({'receipt': self.server.receipt}).encode(), 'application/json')
        elif route == '/binary':
            self.reply(BINARY)
        elif route == '/headers':
            self.reply(json.dumps(dict(self.headers)).encode(), 'application/json')
        elif route == '/stream':
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.send_header('Connection', 'close')
            self.end_headers()
            try:
                self.wfile.write(b'data: first\n\n')
                self.wfile.flush()
                time.sleep(1)
                self.wfile.write(b'data: last\n\n')
                self.wfile.flush()
            except OSError:
                pass
            self.close_connection = True
        else:
            self.send_error(404)

    def do_POST(self):
        length = int(self.headers.get('Content-Length', '0'))
        if length > 2 * 1024 * 1024:
            self.send_error(413)
            return
        self.reply(self.rfile.read(length))


if __name__ == '__main__':
    config = json.loads(FIXTURE.read_text())
    if not config.get('receipt', '').startswith('preview-fixture-'):
        raise SystemExit('Invalid dedicated preview fixture')
    servers = []
    for port in [47880, 47881]:
        server = http.server.ThreadingHTTPServer(('127.0.0.1', port), Handler)
        server.daemon_threads = True
        server.receipt = config['receipt']
        servers.append(server)
        threading.Thread(target=server.serve_forever, daemon=True).start()
    pathlib.Path('/tmp/monkeycode-preview-fixture.pid').write_text(str(__import__('os').getpid()))
    while True:
        time.sleep(60)
