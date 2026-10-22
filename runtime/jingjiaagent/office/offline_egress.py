"""Intranet-only HTTP/CONNECT gateway; exact host:port policy, pinned DNS."""
import argparse
import http.client
import ipaddress
import json
from pathlib import Path
import select
import socket
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

PRIVATE = tuple(ipaddress.ip_network(value) for value in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"))
MAX_BODY = 64 << 20


def parse_authority(value):
    parsed = urlsplit("http://" + value)
    if parsed.username or parsed.password or parsed.path or parsed.query or parsed.fragment or not parsed.hostname:
        raise ValueError("invalid authority")
    port = parsed.port or 80
    return parsed.hostname.lower().rstrip("."), port


def load_policy(path):
    document = json.loads(Path(path).read_text(encoding="utf-8"))
    if set(document) != {"allowed_hosts"} or not isinstance(document["allowed_hosts"], list):
        raise ValueError("invalid egress policy")
    return frozenset(parse_authority(value) for value in document["allowed_hosts"])


def permitted_address(host, port, policy, resolver=socket.getaddrinfo):
    if (host.lower().rstrip("."), port) not in policy:
        raise ValueError("destination not allowed")
    addresses = list(dict.fromkeys(item[4][0] for item in resolver(host, port, type=socket.SOCK_STREAM)))
    if not addresses or any(not any(ipaddress.ip_address(value) in network for network in PRIVATE) for value in addresses):
        raise ValueError("destination is not a private intranet address")
    return addresses[0]


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *_args): pass
    def reject(self, code=403):
        self.send_response(code); self.send_header("Content-Length", "0")
        self.send_header("Connection", "close"); self.end_headers(); self.close_connection = True

    def do_CONNECT(self):
        remote = None
        try:
            host, port = parse_authority(self.path)
            address = permitted_address(host, port, self.server.policy)
            remote = socket.create_connection((address, port), timeout=10)
            self.send_response(200, "Connection established"); self.end_headers(); self.wfile.flush()
            self.connection.setblocking(False); remote.setblocking(False)
            deadline = time.monotonic() + 900
            while time.monotonic() < deadline:
                ready, _, _ = select.select([self.connection, remote], [], [], 60)
                if not ready: break
                for origin in ready:
                    data = origin.recv(65536)
                    if not data: return
                    target = remote if origin is self.connection else self.connection
                    target.setblocking(True); target.settimeout(30); target.sendall(data); target.setblocking(False)
        except (OSError, ValueError):
            if remote is None: self.reject()
        finally:
            if remote is not None: remote.close()
            self.close_connection = True

    def proxy(self):
        connection = None
        try:
            target = urlsplit(self.path)
            if target.scheme != "http" or target.username or target.password or target.fragment or not target.hostname:
                return self.reject()
            host, port = target.hostname.lower().rstrip("."), target.port or 80
            address = permitted_address(host, port, self.server.policy)
            if self.headers.get("Transfer-Encoding"): return self.reject(400)
            size = int(self.headers.get("Content-Length", "0"))
            if not 0 <= size <= MAX_BODY: return self.reject(413)
            self.connection.settimeout(30)
            body = self.rfile.read(size) if size else None
            if body is not None and len(body) != size: return self.reject(400)
            blocked = {"proxy-authorization", "proxy-connection", "connection", "keep-alive", "transfer-encoding", "te", "trailer", "upgrade", "host"}
            headers = {key: value for key, value in self.headers.items() if key.lower() not in blocked}
            headers["Host"] = target.netloc; headers["Connection"] = "close"
            connection = http.client.HTTPConnection(address, port, timeout=600)
            connection.request(self.command, (target.path or "/") + ("?" + target.query if target.query else ""), body, headers)
            response = connection.getresponse()
            self.send_response(response.status)
            for key, value in response.getheaders():
                if key.lower() not in blocked: self.send_header(key, value)
            self.send_header("Connection", "close"); self.end_headers()
            if self.command != "HEAD":
                while chunk := response.read1(65536): self.wfile.write(chunk); self.wfile.flush()
        except (OSError, ValueError, http.client.HTTPException):
            if connection is None: self.reject()
        finally:
            if connection is not None: connection.close()
            self.close_connection = True

    do_GET = proxy
    do_HEAD = proxy
    do_POST = proxy


if __name__ == "__main__":
    parser = argparse.ArgumentParser(); parser.add_argument("--policy", required=True)
    parser.add_argument("--port", type=int, default=3128); args = parser.parse_args()
    server = ThreadingHTTPServer(("0.0.0.0", args.port), Handler)
    server.daemon_threads = True; server.policy = load_policy(args.policy); server.serve_forever()
