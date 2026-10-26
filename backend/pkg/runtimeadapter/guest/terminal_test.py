"""Real PTY/process checks, executed inside an expendable Linux Guest container."""
import base64
import json
import os
import pathlib
import signal
import subprocess
import time
import urllib.request
import unittest
import uuid

SOURCE = pathlib.Path(__file__).with_name('terminal.py').read_bytes()
ENCODED = base64.b64encode(SOURCE).decode()


def rpc(request):
    output = subprocess.check_output(['python3', '-c',
        "import base64;BROKER_SOURCE=base64.b64decode('" + ENCODED + "');exec(BROKER_SOURCE)",
        base64.b64encode(json.dumps(request).encode()).decode()])
    response = json.loads(output)
    if response.get('error'):
        raise ValueError(response['error'])
    return response['data']


class TerminalTest(unittest.TestCase):
    def background(self, op, **kwargs):
        return rpc(dict(op='background_' + op, scope=self.scope, generation='test-generation', **kwargs))

    def setUp(self):
        self.scope = uuid.uuid4().hex

    def tearDown(self):
        for record in self.background('list'):
            if record['state'] == 'running':
                self.background('kill', terminal_id=record['id'])

    def test_background_server_idempotency_scope_and_precise_kill(self):
        folder = pathlib.Path('/workspace') / self.scope
        folder.mkdir()
        (folder / 'index.html').write_text('BACKGROUND_HTTP_OK')
        server = self.background('create', command='python3 -m http.server 18181 --bind 127.0.0.1', cwd=str(folder), request_id='server-1')
        other = self.background('create', command='sleep 120', request_id='other-1')
        retried = self.background('create', command='python3 -m http.server 18181 --bind 127.0.0.1', cwd=str(folder), request_id='server-1')
        self.assertEqual(retried['id'], server['id'])
        self.assertEqual(retried['pid'], server['pid'])
        self.assertEqual(len(self.background('list')), 2)
        with self.assertRaisesRegex(ValueError, 'another command'):
            self.background('create', command='sleep 100', request_id='server-1')
        for key, value in [('scope', uuid.uuid4().hex), ('generation', 'different-generation')]:
            request = dict(op='background_output_path', scope=self.scope, generation='test-generation', terminal_id=server['id'])
            request[key] = value
            with self.assertRaisesRegex(ValueError, 'not found'):
                rpc(request)
        deadline = time.monotonic() + 5
        while True:
            try:
                with urllib.request.urlopen('http://127.0.0.1:18181', timeout=1) as response:
                    self.assertEqual(response.read(), b'BACKGROUND_HTTP_OK')
                break
            except OSError:
                if time.monotonic() > deadline: raise
                time.sleep(0.05)
        path = self.background('output_path', terminal_id=server['id'])['output_path']
        self.assertTrue(path.startswith('/data/runtime/background-terminals/'))
        self.background('kill', terminal_id=server['id'])
        os.kill(other['pid'], 0)  # Stopping one PTY did not kill another task process.
        self.assertEqual(self.background('create', command='python3 -m http.server 18181 --bind 127.0.0.1', cwd=str(folder), request_id='server-1')['state'], 'stopped')

    def test_background_logs_bound_and_exited_retry_not_replayed(self):
        marker = pathlib.Path('/workspace') / (self.scope + '.count')
        command = "printf x >> '%s'; python3 -c \"import sys;sys.stdout.write('L'*11000000)\"" % marker
        session = self.background('create', command=command, request_id='log-1')
        deadline = time.monotonic() + 10
        while self.background('list')[0]['state'] == 'running':
            self.assertLess(time.monotonic(), deadline)
            time.sleep(0.1)
        self.assertEqual(marker.read_text(), 'x')
        result = self.background('create', command=command, request_id='log-1')
        self.assertEqual(result['state'], 'exited')
        self.assertEqual(marker.read_text(), 'x')
        for suffix in ('', '.1'):
            log = pathlib.Path(session['output_path'] + suffix)
            self.assertTrue(log.exists())
            self.assertLessEqual(log.stat().st_size, 4 * 1024 * 1024)

    def test_background_cwd_confined_and_no_implicit_restart(self):
        link = pathlib.Path('/workspace') / self.scope
        link.symlink_to('/tmp')
        for cwd in ['/tmp', '../tmp', str(link)]:
            with self.assertRaisesRegex(ValueError, 'inside /workspace'):
                self.background('create', command='sleep 120', cwd=cwd, request_id='bad-cwd')
        with self.assertRaisesRegex(ValueError, 'identifier'):
            self.background('create', command='sleep 120', request_id='../escape')
        self.assertEqual(self.background('list'), [])

    def test_background_broker_loss_retains_start_receipt_without_replay(self):
        marker = pathlib.Path('/workspace') / (self.scope + '.receipt')
        command = "printf x >> '%s'; sleep 120" % marker
        session = self.background('create', command=command, request_id='start-once')
        deadline = time.monotonic() + 5
        while not marker.exists():
            self.assertLess(time.monotonic(), deadline)
            time.sleep(0.05)
        brokers = []
        for process in pathlib.Path('/proc').iterdir():
            if process.name.isdigit():
                try:
                    cmdline = (process / 'cmdline').read_bytes()
                    if cmdline.endswith(b'--serve\x00') and b'base64' in cmdline:
                        brokers.append(int(process.name))
                except FileNotFoundError:
                    pass
        self.assertEqual(len(brokers), 1)
        os.kill(brokers[0], signal.SIGKILL)
        time.sleep(0.1)
        result = self.background('create', command=command, request_id='start-once')
        self.assertEqual(result['id'], session['id'])
        self.assertIn(result['state'], ('exited', 'unmanaged'))
        self.assertEqual(marker.read_text(), 'x')
        # Restored files in a different runtime generation do not masquerade as
        # active sessions. A new generation only starts after an explicit create.
        self.assertEqual(rpc(dict(op='background_list', scope=self.scope, generation='restored-generation')), [])

    def test_detach_resize_readonly_and_close(self):
        tid, cid = uuid.uuid4().hex, uuid.uuid4().hex
        args = dict(terminal_id=tid, client_id=cid)
        cursor = rpc(dict(op='attach', row=24, col=80, **args))['offset']
        try:
            rid = uuid.uuid4().hex
            command = b"printf 'PTY_%s' OK; stty size\n"
            write = dict(op='write', data=base64.b64encode(command).decode(), request_id=rid, **args)
            rpc(write)
            rpc(write)  # Lost-response retry must not execute input twice.
            output = b''
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                result = rpc(dict(op='read', offset=cursor, **args))
                output += base64.b64decode(result['data'])
                cursor = result['offset']
                if b'PTY_OK' in output and b'24 80' in output:
                    break
                time.sleep(0.05)
            self.assertEqual(output.count(b'PTY_OK'), 1)
            self.assertIn(b'24 80', output)
            rpc(dict(op='write', resize=dict(row=31, col=101), request_id=uuid.uuid4().hex, **args))
            rpc(dict(op='write', data=base64.b64encode(b"stty size; sleep 60 &\n").decode(), request_id=uuid.uuid4().hex, **args))
            rpc(dict(op='detach', **args))
            self.assertEqual(rpc(dict(op='list'))[0]['connected_count'], 0)
            cid = uuid.uuid4().hex
            args['client_id'] = cid
            cursor = rpc(dict(op='attach', readonly=True, **args))['offset']
            with self.assertRaisesRegex(ValueError, 'read-only'):
                rpc(dict(op='write', data='eA==', request_id=uuid.uuid4().hex, **args))
            output = b''
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                result = rpc(dict(op='read', offset=cursor, **args))
                output += base64.b64decode(result['data'])
                cursor = result['offset']
                if b'31 101' in output:
                    break
                time.sleep(0.05)
            self.assertIn(b'PTY_OK', output)  # Disconnect retained the shell/output.
            self.assertIn(b'31 101', output)
            self.assertEqual(rpc(dict(op='list'))[0]['id'], tid)
        finally:
            rpc(dict(op='close', terminal_id=tid))
        self.assertEqual(rpc(dict(op='list')), [])
        for entry in os.listdir('/proc'):
            if entry.isdigit():
                try:
                    self.assertFalse(pathlib.Path('/proc/' + entry + '/cmdline').read_bytes().startswith(b'sleep\x0060'))
                except FileNotFoundError:
                    pass

    def test_readonly_creation_invalid_size_and_id(self):
        for request, message in [
            (dict(readonly=True), 'read-only'),
            (dict(row=65536), 'invalid terminal size'),
            (dict(terminal_id='../escape'), 'invalid terminal identifier'),
        ]:
            r = dict(op='attach', terminal_id=uuid.uuid4().hex, client_id=uuid.uuid4().hex)
            r.update(request)
            with self.assertRaisesRegex(ValueError, message):
                rpc(r)
        self.assertEqual(rpc(dict(op='list')), [])


if __name__ == '__main__':
    unittest.main()
