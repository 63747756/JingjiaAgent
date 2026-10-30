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
import stat
import subprocess
import sys
import tempfile
import threading
import time
import traceback
import urllib.parse
import urllib.request
import urllib.error

from opencode_messages import recent_boundary, read_message, iter_turn_messages, submit_prompt

ROOT = pathlib.Path('/data/state/jingjiaagent-interactions')
POLICIES = pathlib.Path('/data/state/jingjiaagent-policies')
POLICY_PROTOCOL = 1
ATTACHMENT_PROTOCOL = 1
ATTACHMENT_STATE = pathlib.Path('/data/state/jingjiaagent-attachments')
ATTACHMENT_WORKSPACE = pathlib.Path('/workspace')
LOCAL_HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}))
MAX_SSE_BYTES = 1024 * 1024
MAX_PENDING_BYTES = 4 * 1024 * 1024
EVENT_QUEUE_SIZE = 64
PROMPT_PROTOCOL = 1


def validate_prompt_runtime(runtime):
    """Check the frozen, Run-owned instructions before OpenCode loads config.

    This receipt protects transport/lifecycle integrity. It is not a sandbox
    permission boundary; filesystem and network policy still belong to the host.
    """
    fields = {'run_id', 'instructions_path', 'instructions_sha256'}
    if (not isinstance(runtime, dict) or set(runtime) != fields
            or any(not isinstance(value, str) for value in runtime.values())
            or runtime['run_id'] != os.environ.get('AGENT_COMPOSE_RUN_ID', '')):
        raise ValueError('invalid OpenCode runtime receipt')
    instructions = pathlib.Path(runtime['instructions_path'])
    directory = instructions.parent
    if (not instructions.is_absolute()
            or instructions.name != 'jingjiaagent-system.md'
            or directory.parent.resolve() != pathlib.Path(tempfile.gettempdir()).resolve()
            or not re.fullmatch(r'agent-compose-opencode-[A-Za-z0-9_]{6,16}', directory.name)
            or os.environ.get('JINGJIAAGENT_SYSTEM_INSTRUCTIONS') != str(instructions)
            or directory.is_symlink()):
        raise ValueError('invalid OpenCode runtime paths')
    directory_stat = directory.stat()
    if (not stat.S_ISDIR(directory_stat.st_mode) or directory_stat.st_uid != os.getuid()
            or stat.S_IMODE(directory_stat.st_mode) != 0o700):
        raise ValueError('invalid OpenCode runtime directory')
    info = instructions.lstat()
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid()
            or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size > 4 << 20
            or not re.fullmatch(r'[a-f0-9]{64}', runtime['instructions_sha256'])):
        raise ValueError('invalid OpenCode runtime file')
    raw = instructions.read_bytes()
    if hashlib.sha256(raw).hexdigest() != runtime['instructions_sha256']:
        raise ValueError('OpenCode runtime file integrity mismatch')
    raw.decode('utf-8')
    return str(instructions)


def verify_system_instructions(record, instructions_path):
    # Validate the effective native merge, never log provider keys or config.
    config = api(record, '/config', timeout=30)
    if (not instructions_path or not isinstance(config, dict)
            or not isinstance(config.get('instructions'), list)
            or config['instructions'].count(instructions_path) != 1):
        raise ValueError('OpenCode system instructions are not active')


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
        spec = importlib.util.spec_from_file_location('jingjiaagent_interaction',
                    '/opt/agent-compose-runtime/jingjiaagent-interaction.py')
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
    emit('jingjiaagent_permission_reply', record['session_id'], interaction=metadata)


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
    root=ATTACHMENT_WORKSPACE / '.jingjiaagent/attachments' / task / manifest['command_id']
    work_root=ATTACHMENT_WORKSPACE / 'input' / task / manifest['command_id']
    if not isinstance(manifest.get('files'),list) or len(manifest['files'])>10:
        raise ValueError('invalid attachment selection')
    result=[]
    for item in manifest['files']:
        path=pathlib.Path(item['path'])
        if path.parent not in (root,work_root) or path.resolve()!=path or not path.is_file():
            raise ValueError('attachment path unavailable')
        work_input=manifest.get('work_mode') is True or path.parent==work_root
        if path.stat().st_size>32<<20:
            raise ValueError('attachment exceeds limit')
        if work_input and not (item['mime'].startswith('image/') or item['mime']=='application/pdf'):
            # Work inputs are available to local file tools. Do not inline a
            # whole spreadsheet/report/large CSV into a native model message.
            size,digest=0,hashlib.sha256()
            with path.open('rb') as source:
                while chunk:=source.read(65536):
                    size+=len(chunk);digest.update(chunk)
                    if size>32<<20: raise ValueError('attachment exceeds limit')
            if size!=item['size'] or digest.hexdigest()!=item['sha256']:
                raise ValueError('attachment content was changed')
            result.append(dict(type='text',text='用户资料 '+item['filename']+'，本地路径：'+str(path)))
            continue
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


def sse_events(response, stopped):
    """Read bounded SSE frames; an oversized frame is a gap, never silent loss."""
    data, oversized = bytearray(), False
    while not stopped.is_set():
        line = response.readline(MAX_SSE_BYTES + 1)
        if not line:
            return  # An incomplete frame is repaired after the EOF/reset.
        if len(line) > MAX_SSE_BYTES:
            oversized = True
            data.clear()
            while not line.endswith(b'\n') and not stopped.is_set():
                line = response.readline(MAX_SSE_BYTES + 1)
                if not line:
                    break
            continue
        if not line.strip():
            if oversized:
                yield dict(type='jingjiaagent_subscription_reset')
            elif data:
                yield json.loads(data)
            data, oversized = bytearray(), False
        elif line.startswith(b'data:') and not oversized:
            value = line[5:].rstrip(b'\r\n')
            if value.startswith(b' '):
                value = value[1:]
            if len(data) + len(value) + 1 > MAX_SSE_BYTES:
                oversized = True
                data.clear()
            else:
                if data:
                    data.extend(b'\n')
                data.extend(value)


def audit_events(record, stopped, ready, updates):
    """Keep native replies and generation events on the same subscription."""
    auth = base64.b64encode(('opencode:' + record['password']).encode()).decode()
    url = 'http://127.0.0.1:' + str(record['port']) + '/event?' + urllib.parse.urlencode({'directory': record['directory']})
    while not stopped.is_set():
        subscribed = False
        try:
            request = urllib.request.Request(url, headers={'Authorization': 'Basic ' + auth})
            with LOCAL_HTTP.open(request, timeout=10) as response:
                subscribed = True
                ready.set()
                for event in sse_events(response, stopped):
                    if stopped.is_set():
                        return
                    kind, data = event.get('type', ''), event.get('properties', {})
                    if kind in ('message.updated', 'message.part.updated', 'message.part.delta',
                                'session.error', 'jingjiaagent_subscription_reset'):
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
            pass
        finally:
            if subscribed and not stopped.is_set():
                # /event has no replay cursor. Notify immediately on a gap,
                # even if reconnect keeps failing, so HTTP snapshots can repair.
                queue_update(updates, stopped, dict(type='jingjiaagent_subscription_reset'))
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
            emit('jingjiaagent_interaction', record['session_id'], interaction=metadata)
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
        emit('jingjiaagent_' + kind + '_delta', record['session_id'], dict(part, text=text[len(prior):]))
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
        if kind == 'jingjiaagent_subscription_reset':
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
            cached = self.parts.get(part['id'], {})
            if cached.get('time', {}).get('end') and not part.get('time', {}).get('end'):
                return  # An older snapshot cannot reopen a completed part.
            # A stale snapshot must not rewind an in-flight prefix, including
            # text coalesced in this drain but not yet published.
            known = cached.get('text', text_offsets.get(part['id'], ''))
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
            if part.get('time', {}).get('end'):
                # A completed snapshot already includes every delta. A delayed
                # delta must not be appended to that authoritative full text.
                return
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
            if event.get('type') == 'jingjiaagent_subscription_reset':
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


def check_message_error(info):
    error = info.get('error')
    if error and error.get('name') != 'MessageAbortedError':
        raise ValueError('native Agent request failed')


def publish_part(record, part, emitted, text_offsets, tool_states):
    """One output mapping shared by SSE events and persisted snapshots."""
    kind, identifier = part.get('type'), part.get('id')
    if not identifier:
        return
    if kind in ('text', 'reasoning'):
        text, prior = part.get('text', ''), text_offsets.get(identifier, '')
        if isinstance(text, str) and len(prior) > len(text) and prior.startswith(text):
            return  # A stale final snapshot must not publish a shorter block.
        stream_text(record, part, text_offsets)
        if not part.get('time', {}).get('end'):
            return
        key, event = identifier, kind
    elif kind == 'tool':
        if part.get('tool') == 'question':
            return  # The native question UI owns its interaction/card ID.
        state = part.get('state', {}).get('status')
        rank = {'pending': 0, 'running': 1, 'completed': 2, 'error': 2}
        if state not in rank:
            raise ValueError('invalid native tool state')
        previous = tool_states.get(identifier)
        if previous is not None and rank[previous] > rank[state]:
            return
        if previous in ('completed', 'error') and previous != state:
            raise ValueError('native tool terminal state changed')
        tool_states[identifier] = state
        key, event = identifier + '/' + state, 'tool_use'
    elif kind in ('step-start', 'step-finish'):
        key, event = identifier, kind.replace('-', '_')
    else:
        return
    if key not in emitted:
        emitted.add(key)
        emit(event, record['session_id'], part,
             **({'jingjiaagent_streamed': True} if kind in ('text', 'reasoning') else {}))


def publish_messages(record, messages, emitted, text_offsets, previous=frozenset(), tool_states=None):
    """Publish already-scoped snapshots; this function never queries history."""
    if tool_states is None:
        tool_states = {}
    for message in messages:
        info = message.get('info', {})
        if info.get('id') in previous or info.get('role') != 'assistant':
            continue
        check_message_error(info)
        for part in message.get('parts', []):
            publish_part(record, part, emitted, text_offsets, tool_states)


def message_order(info):
    """The native message page order is (time_created, id), not a UUID guess."""
    identifier, created = info.get('id'), info.get('time', {}).get('created')
    if not isinstance(identifier, str) or not identifier or type(created) is not int:
        raise ValueError('invalid native message ordering metadata')
    return created, identifier


class NativeRunUpdates:
    """Scope one run and reconcile both event and snapshot output exactly once."""
    def __init__(self, record, boundary=None):
        self.record = record
        self.boundary = boundary
        self.boundary_order = message_order(boundary) if boundary else None
        self.native = NativeTextUpdates()
        self.infos, self.ignored, self.pending_events = {}, set(), {}
        self.pending_bytes = 0
        self.emitted, self.text_offsets, self.tool_states = set(), {}, {}
        self.changed = {}
        self.needs_reconcile = False
        self.applying_snapshot = False

    def flush(self):
        for part in self.changed.values():
            stream_text(self.record, part, self.text_offsets)
        self.changed.clear()

    def reset(self):
        self.flush()
        self.native.consume(self.record, set(), self.text_offsets,
                            dict(type='jingjiaagent_subscription_reset'))
        # Unknown events may have an untrusted delta base after a gap. Keep IDs
        # for lookup, but discard their payloads and stay snapshot-only this run.
        self.pending_events = {key: [] for key in self.pending_events}
        self.pending_bytes = 0
        self.needs_reconcile = True

    def accept_info(self, info):
        if info.get('sessionID') != self.record['session_id']:
            return False
        identifier = info.get('id')
        order = message_order(info)
        if info.get('role') not in ('user', 'assistant'):
            raise ValueError('invalid native message role')
        waiting = self.pending_events.pop(identifier, [])
        self.pending_bytes -= sum(size for _, size in waiting)
        if self.boundary_order is not None and order <= self.boundary_order:
            self.ignored.add(identifier)
            return False
        if info.get('error'):
            self.flush()
            check_message_error(info)
        self.infos[identifier] = info
        self.native.consume(self.record, set(), self.text_offsets,
                            dict(type='message.updated', properties=dict(info=info)), publish=False)
        for event, _ in waiting:
            self.consume(event)
        return True

    def consume(self, event):
        kind, data = event.get('type'), event.get('properties', {})
        if kind == 'jingjiaagent_subscription_reset':
            self.reset()
            return
        if kind == 'session.error':
            if data.get('sessionID') == self.record['session_id']:
                # Native OpenCode also emits this event before automatic
                # compaction recovers a provider context-length rejection.
                # Let message metadata/the synchronous result decide whether
                # recovery failed; those remain fatal, including auto=false.
                if (data.get('error') or {}).get('name') == 'ContextOverflowError':
                    return
                check_message_error(data)
            return
        if kind == 'message.updated':
            self.accept_info(data.get('info', {}))
            return
        if kind not in ('message.part.updated', 'message.part.delta'):
            return
        part = data.get('part', {}) if kind == 'message.part.updated' else data
        if part.get('sessionID') != self.record['session_id']:
            return
        if self.native.snapshot_only and not self.applying_snapshot:
            # A gap can also hide step-start/tool transitions. Publishing a
            # post-gap finish before replaying its start misattributes usage in
            # the Node runner. Repair ALL part types in native snapshot order.
            self.needs_reconcile = True
            return
        identifier = part.get('messageID')
        if not isinstance(identifier, str) or not identifier or identifier in self.ignored:
            return
        info = self.infos.get(identifier)
        if info is None:
            size = len(json.dumps(event, ensure_ascii=False).encode('utf-8'))
            if self.pending_bytes + size > MAX_PENDING_BYTES or len(self.pending_events) >= 256:
                self.reset()
                return
            self.pending_events.setdefault(identifier, []).append((event, size))
            self.pending_bytes += size
            return
        if info['role'] != 'assistant':
            return
        # Compaction summaries remain in native history. They still contribute
        # errors and step usage, but aren't presented as the user's final reply.
        text_kind = part.get('type') in ('text', 'reasoning') or kind == 'message.part.delta'
        if info.get('summary') and text_kind:
            return
        if text_kind:
            if (kind == 'message.part.delta' and not self.native.snapshot_only
                    and part.get('partID') not in self.native.parts):
                self.reset()  # Missing initial part: don't guess a delta base.
                return
            updated = self.native.consume(self.record, set(), self.text_offsets, event, publish=False)
            if updated:
                self.changed[updated['id']] = updated
                if updated.get('time', {}).get('end'):
                    self.flush()
                    publish_part(self.record, updated, self.emitted, self.text_offsets, self.tool_states)
            return
        self.flush()
        publish_part(self.record, part, self.emitted, self.text_offsets, self.tool_states)

    def drain(self, updates):
        # Bounded batches leave time for approvals/cancellation under a hot SSE.
        for _ in range(1024):
            try:
                self.consume(updates.get_nowait())
            except queue.Empty:
                break
        self.flush()

    def snapshot(self, message):
        self.applying_snapshot = True
        try:
            info = message['info']
            if not self.accept_info(info):
                return
            for part in message['parts']:
                if part.get('messageID') != info['id'] or part.get('sessionID') != self.record['session_id']:
                    raise ValueError('native message part belongs to another message')
                self.consume(dict(type='message.part.updated', properties=dict(part=part)))
            self.flush()
        finally:
            self.applying_snapshot = False

    def resolve_unknown(self):
        for identifier in list(self.pending_events)[:8]:
            try:
                message = read_message(self.record, identifier)
                info = message['info']
                if (info.get('role') == 'user' or self.boundary_order is not None
                        and message_order(info) <= self.boundary_order):
                    # A prune notification for old history isn't a stream gap.
                    self.accept_info(info)
                    continue
                # Lookup established an unknown current assistant. Repair the
                # whole current turn in order, not this one potentially late
                # message ahead of earlier missed tool/step transitions.
                self.reset()
                return
            except urllib.error.HTTPError as error:
                if error.code != 404:
                    raise
                self.needs_reconcile = True  # Native persistence may lag events.

    def reconcile(self):
        boundary_id = self.boundary['id'] if self.boundary else None
        for message in iter_turn_messages(self.record, boundary_id, include_users=True):
            self.snapshot(message)
        self.needs_reconcile = False


def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=['run'])
    parser.add_argument('--prompt-stdin', action='store_true', required=True)
    parser.add_argument('--format', choices=['json'])
    parser.add_argument('--dir', required=True)
    parser.add_argument('--model', default='')
    parser.add_argument('--session', default='')
    args = parser.parse_args()
    # Only the versioned, separated transport is accepted. Never recover a
    # system prefix from user text or reflect malformed input in an error.
    try:
        payload = json.loads(sys.stdin.buffer.read().decode('utf-8'))
    except (ValueError, UnicodeError):
        raise ValueError('invalid OpenCode prompt envelope') from None
    if (not isinstance(payload, dict) or set(payload) != {'version', 'prompt', 'runtime'}
            or type(payload['version']) is not int or payload['version'] != PROMPT_PROTOCOL
            or not isinstance(payload['prompt'], str)):
        raise ValueError('invalid OpenCode prompt envelope')
    args.instructions_path = validate_prompt_runtime(payload['runtime'])
    args.prompt = payload['prompt']
    return args


def run():
    args = parse_args()
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
                  session_id=args.session, pid=server.pid, task_id=os.environ.get('JINGJIAAGENT_TASK_ID', ''))
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
        verify_system_instructions(record, args.instructions_path)
        if stopped.is_set():
            return
        if not record['session_id']:
            record['session_id'] = api(record, '/session', {}, timeout=30)['id']
        boundary = recent_boundary(record, timeout=30)
        atomic_json(pointer, record)
        audit_ready = threading.Event()
        updates = queue.Queue(maxsize=EVENT_QUEUE_SIZE)
        current = NativeRunUpdates(record, boundary)
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
                outcome.append((submit_prompt(record, prompt), None))
            except Exception as error:
                outcome.append((None, error))
            finally:
                done.set()

        threading.Thread(target=submit, daemon=True).start()
        approvals = {}
        next_reconcile = 0.0
        while not stopped.is_set():
            pending(record, current.emitted, approvals)
            current.drain(updates)
            if done.is_set():
                if not outcome:
                    raise ValueError('native Agent request failed')
                info, error = outcome[0]
                if error is not None:
                    raise error
                check_message_error(info)
                # Freeze additive streaming before the final HTTP snapshot;
                # delayed SSE deltas may predate that snapshot. Full snapshots
                # still repair output and use the same idempotent event mapper.
                current.reset()
                current.reconcile()
                current.drain(updates)
                if info['id'] not in current.infos:
                    raise ValueError('native final message is missing from this turn')
                # Emit the session even for an empty successful assistant reply.
                emit('jingjiaagent_session', record['session_id'])
                return
            current.resolve_unknown()
            now = time.monotonic()
            if (current.needs_reconcile or current.native.snapshot_only) and now >= next_reconcile:
                current.reset()
                current.reconcile()
                # Only runs with a subscription gap use fallback polling.
                next_reconcile = time.monotonic() + 2
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
