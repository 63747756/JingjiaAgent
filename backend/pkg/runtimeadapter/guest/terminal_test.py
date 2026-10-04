"""Real PTY/process checks, executed inside an expendable Linux Guest container."""
import base64
import json
import os
import pathlib
import subprocess
import time
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
