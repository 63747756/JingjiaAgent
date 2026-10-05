"""Durably apply a task-scoped policy; acknowledge active bridges after observation."""
import base64
import fcntl
import json
import os
import pathlib
import re
import secrets
import sys
import time

ROOT = pathlib.Path('/data/state/jingjiaagent-policies')
INTERACTIONS = pathlib.Path('/data/state/jingjiaagent-interactions')
BRIDGE = pathlib.Path('/opt/agent-compose-runtime/jingjiaagent-opencode.py')


def atomic_json(path, value):
    temporary = path.with_name(path.name + '.' + secrets.token_hex(8))
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as stream:
            json.dump(value, stream)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def apply(req):
    task = req.get('task_id', '')
    if (not re.fullmatch(r'[a-f0-9-]{36}', task) or type(req.get('enabled')) is not bool or
            type(req.get('revision')) is not int or req['revision'] < 1 or
            not re.fullmatch(r'[a-f0-9-]{36}', req.get('command_id', ''))):
        raise ValueError('invalid approval policy')
    if 'POLICY_PROTOCOL = 1' not in BRIDGE.read_text():
        raise ValueError('Guest approval policy protocol unavailable')
    ROOT.mkdir(mode=0o700, parents=True, exist_ok=True)
    path = ROOT / (task + '.json')
    fd = os.open(ROOT / (task + '.lock'), os.O_RDWR | os.O_CREAT, 0o600)
    with os.fdopen(fd, 'r+') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        current = json.loads(path.read_text()) if path.exists() else None
        if current and current['revision'] >= req['revision']:
            if current != req:
                raise ValueError('approval policy replay conflict')
        else:
            atomic_json(path, req)
    # A bridge holds a shared lock while approving a request. The exclusive
    # write above waits for that decision; the acknowledgement below ensures
    # a live bridge has observed the new policy before the caller reports success.
    deadline = time.monotonic() + 20
    while True:
        waiting = False
        paths = [path for path in INTERACTIONS.glob('*.json') if re.fullmatch(r'[A-Za-z0-9_-]{1,128}', path.stem)]
        if len(paths) > 128:
            raise RuntimeError('too many active native bridges')
        for pointer in paths:
            try:
                active = json.loads(pointer.read_text())
                if active.get('task_id') != task:
                    continue
                try:
                    os.kill(active['pid'], 0)
                except ProcessLookupError:
                    continue
                proof = pointer.with_name(pointer.stem + '.policy.json')
                observed = json.loads(proof.read_text()) if proof.exists() else {}
                if observed.get('revision') != req['revision'] or observed.get('enabled') != req['enabled']:
                    waiting = True
            except FileNotFoundError:
                continue
        if not waiting:
            return dict(success=True)
        if time.monotonic() >= deadline:
            raise RuntimeError('approval policy observation requires reconciliation')
        time.sleep(.1)


if __name__ == '__main__':
    try:
        print(json.dumps(dict(data=apply(json.loads(base64.b64decode(sys.argv[1], validate=True))))))
    except (ValueError, FileNotFoundError):
        print(json.dumps(dict(error='Guest approval policy rejected', invalid=True)))
    except Exception:
        print(json.dumps(dict(error='Guest approval policy requires reconciliation')))
