"""Sandbox-local PTY broker. Browser detach never terminates a shell.

The authenticated runtime Exec interface is the only host-facing transport.
The broker listens on a private Unix socket, never on a network port.
"""
import base64
import collections
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pty
import re
import selectors
import signal
import socket
import struct
import subprocess
import sys
import termios
import time

SOCKET_DIR = '/tmp/jingjiaagent-pty'
SOCKET_PATH = SOCKET_DIR + '/broker.sock'
BUFFER_LIMIT = 1024 * 1024
CLIENT_TTL = 30
BACKGROUND_ROOT = Path('/data/runtime/background-terminals')
LOG_LIMIT = 4 * 1024 * 1024


def process_identity(pid):
    try:
        # comm may contain spaces and parentheses; fields after the last ')' are stable.
        fields = Path('/proc/%d/stat' % pid).read_text().rsplit(')', 1)[1].split()
        return None if fields[0] == 'Z' else fields[19]
    except (FileNotFoundError, ProcessLookupError):
        return None


def background_directory(scope):
    folder = BACKGROUND_ROOT / identifier(scope)
    folder.mkdir(mode=0o700, parents=True, exist_ok=True)
    if folder.is_symlink() or BACKGROUND_ROOT.is_symlink():
        raise ValueError('invalid background terminal directory')
    return folder


def save_record(record):
    path = background_directory(record['scope']) / (record['id'] + '.json')
    temporary = path.with_suffix('.tmp')
    with open(temporary, 'w', encoding='utf-8') as stream:
        json.dump(record, stream)
    os.replace(temporary, path)


def background_records(scope, generation):
    records = []
    for path in sorted(background_directory(scope).glob('*.json')):
        if path.is_symlink():
            raise ValueError('invalid background terminal record')
        record = json.loads(path.read_text())
        if record['generation'] != generation:
            continue
        records.append(record)
    return records


def background_args(request):
    command = request.get('command')
    if not isinstance(command, str) or not command.strip() or len(command.encode()) > 16384 or '\x00' in command:
        raise ValueError('command must contain 1 to 16384 bytes')
    cwd = request.get('cwd') or '/workspace'
    if not isinstance(cwd, str) or '\x00' in cwd:
        raise ValueError('invalid working directory')
    cwd = Path(cwd) if cwd.startswith('/') else Path('/workspace') / cwd
    cwd = cwd.resolve(strict=True)
    if not cwd.is_dir() or not cwd.is_relative_to(Path('/workspace').resolve()):
        raise ValueError('working directory must stay inside /workspace')
    return command, str(cwd)


def identifier(value):
    if not isinstance(value, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,128}', value):
        raise ValueError('invalid terminal identifier')
    return value


def dimensions(row, col):
    row, col = int(row or 24), int(col or 80)
    if not (1 <= row <= 1000 and 1 <= col <= 1000):
        raise ValueError('invalid terminal size')
    return row, col


def resize(fd, row, col):
    row, col = dimensions(row, col)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', row, col, 0, 0))


class Session:
    def __init__(self, request):
        dimensions(request.get('row'), request.get('col'))
        self.id = identifier(request['terminal_id'])
        self.created = int(time.time())
        self.clients = {}
        self.buffer = bytearray()
        self.start = 0
        self.end = 0
        self.writes = collections.OrderedDict()
        self.closed = False
        self.record = request.get('_background_record')
        self.log = None
        if self.record:
            self.log = open(self.record['output_path'], 'ab', buffering=0)
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.chdir(request.get('_cwd', '/workspace'))
            os.environ['TERM'] = 'xterm-256color'
            command = request.get('exec')
            if command:
                os.execl('/bin/bash', 'bash', '-lc', command)
            os.execl('/bin/bash', 'bash', '--noprofile', '--norc', '-i')
        try:
            os.set_blocking(self.fd, False)
            resize(self.fd, request.get('row'), request.get('col'))
            if self.record:
                self.record.update(pid=self.pid, process_start=process_identity(self.pid), state='running')
                save_record(self.record)
        except Exception:
            try:
                self.close()
            except OSError:
                pass
            raise

    def append(self, data):
        if self.log:
            if self.log.tell() + len(data) > LOG_LIMIT:
                self.log.close()
                os.replace(self.record['output_path'], self.record['output_path'] + '.1')
                self.log = open(self.record['output_path'], 'wb', buffering=0)
            self.log.write(data)
        self.buffer.extend(data)
        self.end += len(data)
        if len(self.buffer) > BUFFER_LIMIT:
            removed = len(self.buffer) - BUFFER_LIMIT
            del self.buffer[:removed]
            self.start += removed

    def close(self):
        # Bash job control gives foreground/background jobs separate groups.
        # End every process in this PTY session without touching other terminals.
        processes = []
        for entry in os.listdir('/proc'):
            if entry.isdigit():
                try:
                    if os.getsid(int(entry)) == self.pid:
                        processes.append((int(entry), process_identity(int(entry))))
                except ProcessLookupError:
                    pass
        for sig in (signal.SIGTERM, signal.SIGKILL):
            for pid, identity in processes:
                try:
                    if identity is not None and process_identity(pid) == identity:
                        os.kill(pid, sig)
                except ProcessLookupError:
                    pass
        try:
            os.close(self.fd)
        except OSError:
            pass
        self.closed = True
        self.finish('stopped')

    def finish(self, state='exited'):
        if self.log:
            self.log.close()
            self.log = None
        if self.record:
            self.record.update(state=state, finished_at=int(time.time()))
            save_record(self.record)


def serve():
    os.makedirs(SOCKET_DIR, mode=0o700, exist_ok=True)
    lock = open(SOCKET_DIR + '/broker.lock', 'a')
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        return
    try:
        os.unlink(SOCKET_PATH)
    except FileNotFoundError:
        pass
    listener = socket.socket(socket.AF_UNIX)
    listener.bind(SOCKET_PATH)
    os.chmod(SOCKET_PATH, 0o600)
    listener.listen(16)
    listener.setblocking(False)
    selector = selectors.DefaultSelector()
    selector.register(listener, selectors.EVENT_READ, None)
    sessions = {}

    def request(r):
        op = r['op']
        now = time.monotonic()
        if op.startswith('background_'):
            scope = identifier(r['scope'])
            generation = identifier(r['generation'])
            records = {record['id']: record for record in background_records(scope, generation)}
            # A broker restart loses its PTY descriptors. Never replay commands or
            # claim that an old process is healthy just because its files survived.
            for record in records.values():
                if record['state'] == 'running' and record['id'] not in sessions:
                    record['state'] = 'unmanaged' if process_identity(record['pid']) == record['process_start'] else 'exited'
                    save_record(record)
            if op == 'background_list':
                return sorted(records.values(), key=lambda record: record['created_at'], reverse=True)
            if op == 'background_create':
                command, cwd = background_args(r)
                request_id = identifier(r['request_id'])
                tid = 'bg_' + hashlib.sha256((scope + ':' + generation + ':' + request_id).encode()).hexdigest()[:32]
                fingerprint = hashlib.sha256(json.dumps([command, cwd], ensure_ascii=False).encode()).hexdigest()
                if tid in records:
                    if records[tid]['fingerprint'] != fingerprint:
                        raise ValueError('request_id already belongs to another command; use a new request_id')
                    return records[tid]
                if sum(not session.closed for session in sessions.values()) >= 32:
                    raise ValueError('terminal session limit reached')
                record = dict(id=tid, scope=scope, generation=generation, command=command, cwd=cwd,
                              request_id=request_id, fingerprint=fingerprint, created_at=int(time.time()),
                              state='starting', output_path=str(background_directory(scope) / (tid + '.log')))
                # Write the intent first. A lost start result must not replay on retry.
                save_record(record)
                try:
                    s = Session(dict(terminal_id=tid, exec=command, _cwd=cwd, _background_record=record))
                    sessions[tid] = s
                    selector.register(s.fd, selectors.EVENT_READ, s)
                except Exception:
                    record['state'] = 'unknown'
                    save_record(record)
                    raise
                return record
            tid = identifier(r['terminal_id'])
            record = records.get(tid)
            if record is None:
                raise ValueError('background terminal not found in this task and environment')
            if op == 'background_output_path':
                return dict(terminal_id=tid, output_path=record['output_path'], state=record['state'])
            if op == 'background_kill':
                s = sessions.get(tid)
                if s is not None and not s.closed:
                    selector.unregister(s.fd)
                    s.close()
                elif record['state'] == 'unmanaged':
                    # Without the live PTY session we cannot reliably own every
                    # descendant; fail explicitly instead of risking another job.
                    raise ValueError('terminal broker lost this session; inspect its PID before stopping it')
                record['state'] = 'stopped'
                save_record(record)
                return dict(terminal_id=tid, state='stopped')
            raise ValueError('unsupported background terminal operation')
        if op == 'list':
            return [dict(id=s.id, title='bash', connected_count=sum(now - t < CLIENT_TTL for _, t in s.clients.values()),
                         created_at=s.created) for s in sessions.values() if not s.closed]
        tid = identifier(r['terminal_id'])
        s = sessions.get(tid)
        if op == 'close':
            if s:
                if not s.closed:
                    selector.unregister(s.fd)
                    s.close()
                del sessions[tid]
            return {}
        client = identifier(r['client_id'])
        if op == 'attach':
            readonly = r.get('readonly', False)
            if s is None or s.closed:
                if readonly:
                    raise ValueError('read-only connection cannot create a terminal')
                if len(sessions) >= 32:
                    raise ValueError('terminal session limit reached')
                s = Session(r)
                sessions[tid] = s
                selector.register(s.fd, selectors.EVENT_READ, s)
            elif not readonly:
                resize(s.fd, r.get('row'), r.get('col'))
            s.clients[client] = (readonly, now)
            return dict(offset=s.start)
        if s is None:
            raise ValueError('terminal session not found')
        if op == 'detach':
            s.clients.pop(client, None)
            return {}
        if client not in s.clients:
            raise ValueError('terminal connection not found')
        readonly, _ = s.clients[client]
        s.clients[client] = (readonly, now)
        if op == 'read':
            offset = int(r['offset'])
            if offset < s.start:
                raise ValueError('terminal output expired; reconnect to replay retained output')
            if offset > s.end:
                raise ValueError('invalid terminal output offset')
            data = bytes(s.buffer[offset - s.start:offset - s.start + 32768])
            return dict(data=base64.b64encode(data).decode(), offset=offset + len(data),
                        closed=s.closed and offset + len(data) == s.end)
        if op == 'write':
            if readonly:
                raise ValueError('terminal is read-only')
            if s.closed:
                raise ValueError('terminal process exited')
            rid = identifier(r['request_id'])
            if rid in s.writes:
                return {}
            if r.get('resize'):
                resize(s.fd, r['resize'].get('row'), r['resize'].get('col'))
            data = base64.b64decode(r.get('data') or '', validate=True)
            if len(data) > 32768:
                raise ValueError('terminal input exceeds limit')
            # Return errors without replaying input whose outcome is uncertain.
            s.writes[rid] = True
            while data:
                n = os.write(s.fd, data)
                data = data[n:]
            while len(s.writes) > 1024:
                s.writes.popitem(last=False)
            return {}
        raise ValueError('unsupported terminal operation')

    while True:
        for key, _ in selector.select(0.1):
            if key.data is not None:
                s = key.data
                try:
                    data = os.read(s.fd, 32768)
                    if data:
                        s.append(data)
                        continue
                except BlockingIOError:
                    continue
                except OSError:
                    pass
                selector.unregister(s.fd)
                os.close(s.fd)
                s.closed = True
                s.finish()
            else:
                connection, _ = listener.accept()
                with connection:
                    connection.settimeout(5)
                    try:
                        line = connection.makefile('rb').readline(65537)
                        if len(line) > 65536:
                            raise ValueError('terminal request exceeds limit')
                        response = dict(data=request(json.loads(line)))
                    except Exception as error:
                        response = dict(error=str(error))
                    connection.sendall(json.dumps(response).encode() + b'\n')
        # Reap exited processes without closing active shells on client detach.
        now = time.monotonic()
        for s in list(sessions.values()):
            s.clients = {cid: state for cid, state in s.clients.items() if now - state[1] < CLIENT_TTL}
            if s.closed and not s.clients:
                del sessions[s.id]
        while True:
            try:
                pid, _ = os.waitpid(-1, os.WNOHANG)
                if pid == 0:
                    break
            except ChildProcessError:
                break


def call(request, source):
    encoded = base64.b64encode(source).decode()
    started = False
    for _ in range(50):
        connection = socket.socket(socket.AF_UNIX)
        connection.settimeout(8)
        try:
            connection.connect(SOCKET_PATH)
        except (FileNotFoundError, ConnectionRefusedError):
            connection.close()
            if not started:
                subprocess.Popen([sys.executable, '-c', "import base64;exec(base64.b64decode('" + encoded + "'))", '--serve'],
                                 stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                 start_new_session=True, close_fds=True)
                started = True
            time.sleep(0.02)
            continue
        with connection:
            connection.sendall(json.dumps(request).encode() + b'\n')
            return json.loads(connection.makefile('rb').readline(2 * BUFFER_LIMIT))
    raise RuntimeError('terminal broker unavailable')


if __name__ == '__main__':
    if sys.argv[1] == '--serve':
        serve()
    else:
        try:
            # The embedding wrapper supplies the exact source for detached startup.
            print(json.dumps(call(json.loads(base64.b64decode(sys.argv[1])), BROKER_SOURCE)))
        except Exception as error:
            print(json.dumps(dict(error=str(error))))
