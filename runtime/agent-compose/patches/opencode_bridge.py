"""Run one native OpenCode HTTP session; publish CLI-compatible events.

The authenticated server is private to the Guest and the Run. Browser lifetime
does not own this process. No permission or question is answered implicitly.
"""
import argparse
import base64
import hashlib
import fcntl
import importlib.util
import json
import os
import pathlib
import queue
import re
import secrets
import signal
import socket
import subprocess
import sys
import threading
import time
import traceback
import urllib.parse
import urllib.request
import urllib.error

ROOT = pathlib.Path('/data/state/monkeycode-interactions')
POLICIES = pathlib.Path('/data/state/monkeycode-policies')
POLICY_PROTOCOL = 1
ATTACHMENT_PROTOCOL = 1
ATTACHMENT_STATE = pathlib.Path('/data/state/monkeycode-attachments')
ATTACHMENT_WORKSPACE = pathlib.Path('/workspace')
LOCAL_HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def auto_permission(record, metadata):
    task = record.get('task_id', '')
    if not re.fullmatch(r'[a-f0-9-]{36}', task):
        return False
    POLICIES.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd = os.open(POLICIES / (task + '.lock'), os.O_RDWR | os.O_CREAT, 0o600)
    with os.fdopen(fd, 'r+') as lock:
        fcntl.flock(lock, fcntl.LOCK_SH)
        path = POLICIES / (task + '.json')
        policy = json.loads(path.read_text()) if path.exists() else dict(revision=0, enabled=False)
        atomic_json(ROOT / (record['run_id'] + '.policy.json'),
                    dict(revision=policy['revision'], enabled=policy['enabled']))
        if metadata is None or not policy['enabled']:
            return False
        # Only native pending permissions reach this branch. Explicit native
        # deny rules remain deny rules; user questions are never auto-answered.
        spec = importlib.util.spec_from_file_location('monkeycode_interaction',
                    '/opt/agent-compose-runtime/monkeycode-interaction.py')
        controls = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(controls)
        request = {key: metadata[key] for key in ('run_id', 'session_id', 'request_id', 'request_kind')}
        request.update(answers=[['允许一次']], cancelled=False)
        try:
            return controls.control(request)['success']
        except (ValueError, FileNotFoundError):
            # A simultaneous explicit response or expired native request wins.
            return False
        except (RuntimeError, urllib.error.URLError, TimeoutError):
            # The shared control receipt retains uncertainty. Retry that exact
            # decision on the next poll, including after the pending API clears.
            return False


def emit_auto_reply(record, metadata):
    emit('monkeycode_permission_reply', record['session_id'], interaction=metadata)


def attachment_parts(record):
    task=record.get('task_id','')
    if not re.fullmatch(r'[a-f0-9-]{36}',task):
        return []
    pointer=ATTACHMENT_STATE / (task+'.json')
    if not pointer.exists():
        return []
    manifest=json.loads(pointer.read_text())
    if manifest.get('task_id')!=task or not re.fullmatch(r'[a-f0-9-]{36}',manifest.get('command_id','')):
        raise ValueError('invalid attachment correlation')
    root=ATTACHMENT_WORKSPACE / '.monkeycode/attachments' / task / manifest['command_id']
    if not isinstance(manifest.get('files'),list) or len(manifest['files'])>10:
        raise ValueError('invalid attachment selection')
    result=[]
    for item in manifest['files']:
        path=pathlib.Path(item['path'])
        if path.parent!=root or path.resolve()!=path or not path.is_file():
            raise ValueError('attachment path unavailable')
        if path.stat().st_size>32<<20:
            raise ValueError('attachment exceeds limit')
        content=path.read_bytes()
        if len(content)!=item['size'] or hashlib.sha256(content).hexdigest()!=item['sha256']:
            raise ValueError('attachment content was changed')
        mime=item['mime']
        # Native OpenCode embeds text, image and PDF file parts. Other binary
        # formats are still delivered byte-for-byte and named in the prompt,
        # allowing the Agent to choose its existing file tools.
        result.append(dict(type='text',text='用户附件 '+item['filename']+'，本地路径：'+str(path)))
        try:
            content.decode('utf-8')
            if not mime.startswith('image/') and mime!='application/pdf' and b'\x00' not in content:
                mime='text/plain'
        except UnicodeDecodeError:
            pass
        if mime.startswith('text/') or mime.startswith('image/') or mime=='application/pdf':
            result.append(dict(type='file',mime=mime,filename=item['filename'],url=path.as_uri()))
    return result


def atomic_json(path, value):
    path.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
    temporary = path.with_name(path.name + '.' + secrets.token_hex(8))
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as target:
            json.dump(value, target, ensure_ascii=False)
            target.flush()
            os.fsync(target.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def api(record, route, body=None, timeout=5):
    auth = base64.b64encode(('opencode:' + record['password']).encode()).decode()
    url = 'http://127.0.0.1:' + str(record['port']) + route
    separator = '&' if '?' in route else '?'
    url += separator + urllib.parse.urlencode({'directory': record['directory']})
    request = urllib.request.Request(url, data=None if body is None else json.dumps(body).encode(),
                                     headers={'Authorization': 'Basic ' + auth, 'Content-Type': 'application/json'})
    with LOCAL_HTTP.open(request, timeout=timeout) as response:
        payload = response.read(16 * 1024 * 1024 + 1)
        if len(payload) > 16 * 1024 * 1024:
            raise ValueError('native response exceeds limit')
        return json.loads(payload) if payload else None


def emit(kind, session, part=None, **extra):
    print(json.dumps(dict(type=kind, timestamp=int(time.time() * 1000), sessionID=session,
                          part=part or {}, **extra), ensure_ascii=False), flush=True)


def audit_events(record, stopped, ready, updates):
    """Keep native replies and generation events on the same subscription."""
    auth = base64.b64encode(('opencode:' + record['password']).encode()).decode()
    url = 'http://127.0.0.1:' + str(record['port']) + '/event?' + urllib.parse.urlencode({'directory': record['directory']})
    subscribed = False
    while not stopped.is_set():
        try:
            request = urllib.request.Request(url, headers={'Authorization': 'Basic ' + auth})
            with LOCAL_HTTP.open(request, timeout=10) as response:
                if subscribed:
                    # /event has no replay cursor. Put the reset on the same
                    # queue, before any event from the replacement connection,
                    # including reconnects caused by an ordinary HTTP EOF.
                    if not queue_update(updates, stopped, dict(type='monkeycode_subscription_reset')):
                        return
                subscribed = True
                ready.set()
                for line in response:
                    if stopped.is_set():
                        return
                    if not line.startswith(b'data:') or len(line) > 1024 * 1024:
                        continue
                    event = json.loads(line[5:])
                    kind, data = event.get('type', ''), event.get('properties', {})
                    if kind in ('message.updated', 'message.part.updated', 'message.part.delta'):
                        if not queue_update(updates, stopped, event):
                            return
                        continue
                    if kind not in ('question.replied', 'question.rejected', 'permission.replied',
                                    'question.v2.replied', 'question.v2.rejected', 'permission.v2.replied'):
                        continue
                    identifier = data.get('requestID', '')
                    if data.get('sessionID') != record['session_id'] or not re.fullmatch(r'(que|per)[A-Za-z0-9_-]+', identifier):
                        continue
                    atomic_json(ROOT / record['run_id'] / (identifier + '.native.json'), dict(kind=kind, data=data))
        except Exception:
            stopped.wait(.2)


def queue_update(updates, stopped, event):
    while not stopped.is_set():
        try:
            updates.put(event, timeout=.1)
            return True
        except queue.Full:
            continue
    return False


def pending(record, emitted, approvals):
    auto_permission(record, None)
    for key, metadata in approvals.items():
        if key + '/auto' not in emitted and auto_permission(record, metadata):
            emitted.add(key + '/auto')
            emit_auto_reply(record, metadata)
    for kind, route in [('question', '/question'), ('permission', '/permission')]:
        requests = api(record, route)
        for request in requests:
            if request.get('sessionID') != record['session_id']:
                continue
            key = request.get('id', '')
            if not re.fullmatch(r'(que|per)[A-Za-z0-9_-]+', key):
                raise ValueError('invalid native interaction ID')
            if key in emitted:
                continue
            if kind == 'question':
                questions = [dict(question, custom=question.get('custom', True)) for question in request['questions']]
            else:
                # Reuse the existing question UI for an explicit native
                # permission decision. "once" never changes the allow-list.
                questions = [dict(header='执行审批', question='允许执行 ' + request['permission'] + '：' +
                                  '\n'.join(request['patterns']), multiple=False, custom=False,
                                  options=[dict(label='允许一次', description='仅允许本次请求'),
                                           dict(label='拒绝', description='拒绝本次请求')])]
            metadata = dict(request_id=key, session_id=record['session_id'], run_id=record['run_id'],
                            request_kind=kind, questions=questions)
            atomic_json(ROOT / record['run_id'] / (key + '.json'), dict(request=metadata))
            emitted.add(key)
            emit('monkeycode_interaction', record['session_id'], interaction=metadata)
            if kind == 'permission':
                approvals[key] = metadata
                if auto_permission(record, metadata):
                    emitted.add(key + '/auto')
                    emit_auto_reply(record, metadata)


def stream_text(record, part, text_offsets):
    identifier, kind = part.get('id'), part.get('type')
    if not identifier or kind not in ('text', 'reasoning'):
        return
    text = part.get('text', '')
    prior = text_offsets.get(identifier, '')
    if not isinstance(text, str):
        raise ValueError('invalid native text part')
    if not text.startswith(prior):
        if prior.startswith(text):
            return  # An older snapshot cannot rewind live output.
        raise ValueError('native text part changed after publication')
    if len(text) > len(prior):
        emit('monkeycode_' + kind + '_delta', record['session_id'], dict(part, text=text[len(prior):]))
        text_offsets[identifier] = text


class NativeTextUpdates:
    """Accumulate native deltas; reconcile against persisted part snapshots.

    OpenCode saves the full text part at completion. Its message.part.delta
    subscription is therefore the source of live generation. Both sources
    share publication offsets so the final snapshot only fills missing text.
    Reconnection discards additive state, but never a trusted prefix. Neither
    snapshots nor deltas carry a replay cursor, so a post-gap snapshot cannot
    prove where subsequent deltas belong. After a gap, use complete snapshots
    alone for the rest of this Run, including newly seen parts: those parts may
    also have been created while disconnected. Gap-free Runs still stream live.
    """
    def __init__(self):
        self.roles = {}
        self.parts = {}
        self.snapshot_only = False

    def consume(self, record, previous, text_offsets, event, publish=True):
        kind, data = event.get('type'), event.get('properties', {})
        if kind == 'monkeycode_subscription_reset':
            self.parts.clear()
            self.snapshot_only = True
            return
        if kind == 'message.updated':
            info = data.get('info', {})
            if info.get('sessionID') == record['session_id'] and info.get('id') not in previous:
                self.roles[info.get('id')] = info.get('role')
            return
        if kind == 'message.part.updated':
            part = data.get('part', {})
            if part.get('sessionID') != record['session_id'] or part.get('messageID') in previous:
                return
            if part.get('type') not in ('text', 'reasoning') or not part.get('id'):
                return
            text = part.get('text', '')
            if not isinstance(text, str):
                raise ValueError('invalid native text part')
            # A stale snapshot must not rewind an in-flight prefix, including
            # text coalesced in this drain but not yet published.
            known = self.parts.get(part['id'], {}).get('text', text_offsets.get(part['id'], ''))
            if known.startswith(text) and len(known) > len(text):
                return
            # Updated parts are complete snapshots, never additive deltas.
            self.parts[part['id']] = dict(part)
        elif kind == 'message.part.delta':
            if self.snapshot_only:
                return  # A snapshot cannot safely re-establish a post-gap base.
            if data.get('sessionID') != record['session_id'] or data.get('field') != 'text':
                return
            part = self.parts.get(data.get('partID'))
            if not part or part.get('messageID') != data.get('messageID') or data.get('messageID') in previous:
                return  # After a subscription gap the final snapshot repairs it.
            if not isinstance(data.get('delta'), str):
                raise ValueError('invalid native text delta')
            part['text'] = part.get('text', '') + data['delta']
        else:
            return
        if self.roles.get(part.get('messageID')) == 'assistant':
            if publish:
                stream_text(record, part, text_offsets)
            return part

    def drain(self, record, previous, text_offsets, updates):
        changed = {}

        def flush():
            for part in changed.values():
                stream_text(record, part, text_offsets)
            changed.clear()

        while True:
            try:
                event = updates.get_nowait()
            except queue.Empty:
                break
            if event.get('type') == 'monkeycode_subscription_reset':
                # Publish trusted pre-gap text before invalidating its base.
                # Clearing or replacing changed could otherwise lose a prefix
                # that has not reached text_offsets during this same drain.
                flush()
            part = self.consume(record, previous, text_offsets, event, publish=False)
            if part:
                changed[part['id']] = part
        # Native deltas often contain just one token. Publish at most one
        # suffix per part per poll/connection boundary, not one per token.
        flush()


def parts(record, previous, emitted, text_offsets):
    messages = api(record, '/session/' + record['session_id'] + '/message')
    for message in messages:
        info = message.get('info', {})
        if info.get('id') in previous or info.get('role') != 'assistant':
            continue
        if info.get('error') and info['error'].get('name') != 'MessageAbortedError':
            raise ValueError('native Agent request failed')
        for part in message.get('parts', []):
            kind, identifier = part.get('type'), part.get('id')
            if not identifier:
                continue
            if kind in ('text', 'reasoning'):
                # Stream the newly generated suffix, including unfinished parts.
                # The complete block still feeds the CLI result parser once;
                # its marker prevents publishing or transcribing it twice.
                stream_text(record, part, text_offsets)
                if not part.get('time', {}).get('end'):
                    continue
                key = identifier
                event = kind
            elif kind == 'tool':
                state = part.get('state', {}).get('status')
                if part.get('tool') == 'question':
                    # The native question request above owns the UI ID; a
                    # separate tool-call ID would create a second question card.
                    continue
                key = identifier + '/' + str(state)
                event = 'tool_use'
            elif kind in ('step-start', 'step-finish'):
                key = identifier
                event = kind.replace('-', '_')
            else:
                continue
            if key not in emitted:
                emitted.add(key)
                emit(event, record['session_id'], part, **({'monkeycode_streamed': True} if kind in ('text', 'reasoning') else {}))


def run():
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=['run'])
    parser.add_argument('prompt')
    parser.add_argument('--format', choices=['json'])
    parser.add_argument('--dir', required=True)
    parser.add_argument('--model', default='')
    parser.add_argument('--session', default='')
    args = parser.parse_args()
    run_id = os.environ.get('AGENT_COMPOSE_RUN_ID', '')
    if not re.fullmatch(r'[A-Za-z0-9_-]{1,128}', run_id):
        raise ValueError('invalid Run ID')
    ROOT.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(ROOT, 0o700)
    with socket.socket() as temporary:
        temporary.bind(('127.0.0.1', 0))
        port = temporary.getsockname()[1]
    password = secrets.token_urlsafe(32)
    env = dict(os.environ, OPENCODE_SERVER_PASSWORD=password, OPENCODE_SERVER_USERNAME='opencode')
    server = subprocess.Popen(['opencode', 'serve', '--hostname', '127.0.0.1', '--port', str(port)],
                              cwd=args.dir, env=env, stdin=subprocess.DEVNULL,
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
    stopped = threading.Event()
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, lambda *_: stopped.set())
    record = dict(run_id=run_id, port=port, password=password, directory=args.dir,
                  session_id=args.session, pid=server.pid, task_id=os.environ.get('MONKEYCODE_TASK_ID', ''))
    pointer = ROOT / (run_id + '.json')
    try:
        deadline = time.monotonic() + 30
        while not stopped.is_set():
            try:
                if api(record, '/global/health', timeout=1).get('healthy'):
                    break
            except Exception:
                if server.poll() is not None or time.monotonic() >= deadline:
                    raise ValueError('native server did not start') from None
                time.sleep(.1)
        if stopped.is_set():
            return
        if not record['session_id']:
            record['session_id'] = api(record, '/session', {}, timeout=30)['id']
        previous = {x['info']['id'] for x in api(record, '/session/' + record['session_id'] + '/message', timeout=30)}
        atomic_json(pointer, record)
        audit_ready = threading.Event()
        updates = queue.Queue(maxsize=4096)
        native_text = NativeTextUpdates()
        threading.Thread(target=audit_events, args=(record, stopped, audit_ready, updates), daemon=True).start()
        if not audit_ready.wait(5):
            raise ValueError('native receipt subscription did not start')
        prompt = dict(parts=[dict(type='text', text=args.prompt)] + attachment_parts(record))
        if args.model:
            provider, model = args.model.split('/', 1)
            prompt['model'] = dict(providerID=provider, modelID=model)
        done, outcome = threading.Event(), []

        def submit():
            try:
                outcome.append(api(record, '/session/' + record['session_id'] + '/message', prompt, timeout=86400))
            except Exception:
                outcome.append(None)
            finally:
                done.set()

        threading.Thread(target=submit, daemon=True).start()
        emitted, approvals, text_offsets = set(), {}, {}
        while not stopped.is_set():
            pending(record, emitted, approvals)
            parts(record, previous, emitted, text_offsets)
            native_text.drain(record, previous, text_offsets, updates)
            if done.is_set():
                parts(record, previous, emitted, text_offsets)
                native_text.drain(record, previous, text_offsets, updates)
                if not outcome or outcome[0] is None:
                    raise ValueError('native Agent request failed')
                # Emit the session even for an empty successful assistant reply.
                emit('monkeycode_session', record['session_id'])
                return
            stopped.wait(.2)
    finally:
        if record['session_id']:
            try:
                api(record, '/session/' + record['session_id'] + '/abort', {}, timeout=2)
            except Exception:
                pass
        pointer.unlink(missing_ok=True)
        pointer.with_name(pointer.stem + '.policy.json').unlink(missing_ok=True)
        stopped.set()
        if server.poll() is None:
            os.killpg(server.pid, signal.SIGTERM)
            try:
                server.wait(timeout=3)
            except subprocess.TimeoutExpired:
                os.killpg(server.pid, signal.SIGKILL)
                server.wait()


if __name__ == '__main__':
    try:
        run()
    except Exception as error:
        # Types and our source line numbers help diagnose the bridge without
        # reflecting HTTP bodies, provider errors, URLs or credentials.
        frames = [str(frame.lineno) for frame in traceback.extract_tb(error.__traceback__) if frame.filename == __file__]
        code = type(error).__name__ + ':' + ','.join(frames)
        emit('error', '', error=dict(name='GuestControlError', data=dict(message='Guest native Agent execution failed (' + code + ')')))
        sys.exit(1)
