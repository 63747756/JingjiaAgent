"""Provider session control via authenticated Exec, with replay-safe resets."""
import base64
import fcntl
import json
import os
import pathlib
import re
import sys
import tempfile

ROOT = pathlib.Path('/data/state')


def atomic_json(target, value):
    target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(dir=target.parent)
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as stream:
            json.dump(value, stream)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, target)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def control(req):
    provider = req.get('provider')
    if provider not in ('opencode', 'codex', 'claude'):
        raise ValueError('provider session control is not accepted')
    ROOT.mkdir(mode=0o700, parents=True, exist_ok=True)
    controls = ROOT / 'jingjiaagent-controls'
    controls.mkdir(mode=0o700, exist_ok=True)
    with open(controls / 'lock', 'a+') as lock:
        os.chmod(lock.name, 0o600)
        fcntl.flock(lock, fcntl.LOCK_EX)
        target = ROOT / 'agents' / 'providers' / (provider + '.json')
        record = {}
        if target.exists():
            record = json.loads(target.read_text(encoding='utf-8'))
            if not isinstance(record, dict):
                raise ValueError('invalid provider session record')
        session = record.get('threadId', record.get('sessionId', ''))
        if not isinstance(session, str) or len(session) > 256:
            raise ValueError('invalid provider session ID')
        if req.get('op') == 'read':
            return dict(session_id=session)
        if req.get('op') != 'restart' or not isinstance(req.get('load_session'), bool):
            raise ValueError('invalid provider session request')
        key = req.get('command_id', '')
        if not re.fullmatch(r'[a-f0-9-]{36}', key):
            raise ValueError('invalid control command ID')
        receipt = controls / (key + '.json')
        if receipt.exists():
            result = json.loads(receipt.read_text(encoding='utf-8'))
            if result.get('load_session') != req['load_session'] or result.get('provider', 'opencode') != provider:
                raise ValueError('provider control replay conflict')
            return result
        if not req['load_session']:
            # Forget only the current session pointer. Keep files and native
            # history; the next prompt creates a new provider session.
            atomic_json(target, dict(provider=provider, threadId=''))
            session = ''
        result = dict(session_id=session, load_session=req['load_session'], provider=provider)
        atomic_json(receipt, result)
        return result


if __name__ == '__main__':
    try:
        req = json.loads(base64.b64decode(sys.argv[1], validate=True))
        print(json.dumps(dict(data=control(req))))
    except Exception:
        # Never reflect session/config contents in diagnostics.
        print(json.dumps(dict(error='Guest provider session control failed')))
