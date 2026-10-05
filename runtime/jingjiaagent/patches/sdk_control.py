"""Correlate native SDK control requests with the existing authenticated Guest API."""
import base64
import fcntl
import hashlib
import json
import os
import pathlib
import re
import sys
import time

from opencode_bridge import atomic_json

ROOT = pathlib.Path('/data/state/jingjiaagent-interactions')
POLICIES = pathlib.Path('/data/state/jingjiaagent-policies')


def policy(active):
    POLICIES.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd = os.open(POLICIES / (active['task_id'] + '.lock'), os.O_CREAT | os.O_RDWR, 0o600)
    with os.fdopen(fd, 'r+') as lock:
        fcntl.flock(lock, fcntl.LOCK_SH)
        path = POLICIES / (active['task_id'] + '.json')
        value = json.loads(path.read_text()) if path.exists() else dict(enabled=False, revision=0)
        atomic_json(ROOT / (active['run_id'] + '.policy.json'), dict(enabled=value['enabled'], revision=value['revision']))
        return bool(value['enabled'])


def wait_decision(request, publish):
    run, task = os.environ.get('AGENT_COMPOSE_RUN_ID', ''), os.environ.get('JINGJIAAGENT_TASK_ID', '')
    provider, native = request['provider'], request['native_id']
    if provider not in ('claude', 'codex') or not re.fullmatch(r'[A-Za-z0-9_-]{1,128}', run) or not re.fullmatch(r'[a-f0-9-]{36}', task):
        raise ValueError('invalid SDK request correlation')
    kind = request['kind']
    if kind not in ('question', 'permission'):
        raise ValueError('invalid SDK control kind')
    identifier = ('que' if kind == 'question' else 'per') + hashlib.sha256((run + provider + str(native)).encode()).hexdigest()[:40]
    session = provider + '-' + run
    directory = ROOT / run
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    active = dict(run_id=run, task_id=task, session_id=session, backend='sdk', provider=provider, pid=request.get('parent_pid', os.getpid()))
    atomic_json(ROOT / (run + '.json'), active)
    questions = request.get('questions')
    if kind == 'permission':
        questions = [dict(question=request.get('title') or '允许执行此工具？', multiple=False, custom=False, options=[dict(label='允许一次'), dict(label='拒绝')])]
    if not isinstance(questions, list) or not questions or len(questions) > 16:
        raise ValueError('invalid SDK questions')
    metadata = dict(request_id=identifier, run_id=run, session_id=session, request_kind=kind, questions=questions)
    target = directory / (identifier + '.json')
    atomic_json(target, dict(request=metadata, backend='sdk', native_id=native, provider=provider))
    publish(dict(event='request', metadata=metadata))
    while True:
        enabled = policy(active)
        response_path = directory / (identifier + '.answer.json')
        if response_path.exists():
            response = json.loads(response_path.read_text())
            answer = response['answer']
            if any(answer.get(key) != metadata.get(key) for key in ('run_id', 'request_id', 'session_id', 'request_kind')):
                raise ValueError('SDK decision correlation mismatch')
            atomic_json(directory / (identifier + '.consumed.json'), dict(receipt=response['receipt']))
            return answer
        if kind == 'permission' and enabled:
            publish(dict(event='auto_reply', metadata=metadata))
            return dict(answers=[['允许一次']], cancelled=False)
        time.sleep(0.1)


if __name__ == '__main__':
    try:
        request = json.loads(base64.b64decode(sys.argv[1], validate=True))
        emit = lambda value: print(json.dumps(value, ensure_ascii=False), flush=True)
        answer = wait_decision(request, emit)
        emit(dict(event='decision', answer=answer))
    except Exception:
        # Tool/config values may contain credentials: diagnostics stay fixed.
        print(json.dumps(dict(event='error', message='SDK control request failed')), flush=True)
        sys.exit(1)
