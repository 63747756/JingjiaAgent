"""Bounded streaming reads of OpenCode 1.18.9 message snapshots.

OpenCode pages are oldest-first *within* a page, with the newest page returned
first. Follow the opaque X-Next-Cursor instead of sorting message identifiers.
Temporary files let us replay pages chronologically without retaining a whole
turn in memory. They are private and removed on completion or any exception.

ijson avoids materializing whole responses and discarded user parts. It still
materializes a single JSON scalar: memory is therefore not constant for a huge
string. A per-response wire budget bounds that case; retained message metadata
and assistant snapshots have separate, smaller budgets. These are transport
resource guards, not model context or user-input limits.
"""
import base64
from decimal import Decimal
import json
import math
import pathlib
import tempfile
import urllib.parse
import urllib.request

import ijson
from ijson.common import ObjectBuilder

LOCAL_HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}))
PAGE_SIZE = 50
MAX_RESPONSE_BYTES = 256 * 1024 * 1024
MAX_METADATA_BYTES = 1024 * 1024
MAX_MESSAGE_BYTES = 64 * 1024 * 1024
MAX_TURN_BYTES = 512 * 1024 * 1024
MAX_EVENTS = 2_000_000
MAX_PAGES = 2000
MAX_DEPTH = 128


def open_response(record, route, body=None, timeout=5):
    """Open an authenticated loopback response; the caller must close it."""
    auth = base64.b64encode(('opencode:' + record['password']).encode()).decode()
    url = 'http://127.0.0.1:' + str(record['port']) + route
    url += ('&' if '?' in route else '?') + urllib.parse.urlencode({'directory': record['directory']})
    request = urllib.request.Request(
        url, data=None if body is None else json.dumps(body).encode('utf-8'),
        headers={'Authorization': 'Basic ' + auth, 'Content-Type': 'application/json'},
    )
    return LOCAL_HTTP.open(request, timeout=timeout)


class _Reader:
    def __init__(self, source, copy=None):
        self.source, self.copy, self.size = source, copy, 0

    def read(self, size=-1):
        # ijson requests bounded blocks. Do not allow a consumer to accidentally
        # turn this wrapper into an unbounded read.
        if size < 0:
            raise ValueError('unbounded native response read')
        data = self.source.read(min(size, 65536))
        self.size += len(data)
        if self.size > MAX_RESPONSE_BYTES:
            raise ValueError('native message response exceeds transport budget')
        if self.copy is not None:
            self.copy.write(data)
        return data


def _events(source):
    depth, count = 0, 0
    try:
        # YAJL's use_float=True also limits integer tokens to 64 bits. OpenCode
        # can legitimately serialize a Number such as 1e20 as a larger integer
        # token in MCP arguments. Keep integer tokens as Python ints; convert
        # Decimal fractions/exponents to finite doubles before ObjectBuilder so
        # snapshots remain JSON-serializable and match native Number semantics.
        for prefix, event, value in ijson.parse(source, use_float=False):
            count += 1
            if count > MAX_EVENTS:
                raise ValueError('native message JSON exceeds event budget')
            if isinstance(value, Decimal):
                value = float(value)
                if not math.isfinite(value):
                    raise ValueError('native message JSON contains a non-finite number')
            if event in ('start_map', 'start_array'):
                depth += 1
                if depth > MAX_DEPTH:
                    raise ValueError('native message JSON is too deeply nested')
            elif event in ('end_map', 'end_array'):
                depth -= 1
            yield prefix, event, value
    except ijson.JSONError as error:
        raise ValueError('invalid native message JSON') from error


def _cost(event, value):
    if isinstance(value, str):
        return len(value.encode('utf-8')) + 64
    if type(value) is int:
        # Integer tokens are now arbitrary precision; account for their size
        # instead of charging a constant amount of retained metadata memory.
        return value.bit_length() // 8 + 65
    return 64


def _metadata(info, session_id):
    if not isinstance(info, dict) or not isinstance(info.get('id'), str) or not info['id']:
        raise ValueError('native message metadata is missing an ID')
    if info.get('sessionID') != session_id:
        raise ValueError('native message belongs to another session')
    if info.get('role') not in ('user', 'assistant'):
        raise ValueError('invalid native message role')
    if info.get('error') is not None and not isinstance(info['error'], dict):
        raise ValueError('invalid native message error')
    return info


def _parse_messages(source, session_id, *, array=True, selected=None):
    """Read metadata only, or selected assistant snapshots, one item at a time.

    Native 1.18.9 serializes ``{info, parts}`` in that order. Refuse a different
    shape rather than buffering arbitrary parts before knowing their owner.
    ``selected=None`` discards every parts array; selected IDs retain assistants.
    Even discarded content is parsed so malformed/truncated JSON cannot succeed.
    """
    base = 'item' if array else ''
    info_prefix = base + '.info' if base else 'info'
    parts_prefix = base + '.parts' if base else 'parts'
    builder, building, size, info, parts, keys = None, None, 0, None, None, set()
    root_seen = root_ended = False
    for prefix, event, value in _events(source):
        if not root_seen:
            expected = 'start_array' if array else 'start_map'
            if prefix != '' or event != expected:
                raise ValueError('invalid native message response shape')
            root_seen = True
        if array and prefix == 'item' and event not in ('start_map', 'end_map', 'map_key'):
            raise ValueError('native message array contains a non-object')
        if prefix == '' and event == ('end_array' if array else 'end_map'):
            root_ended = True
        if prefix == base and event == 'start_map':
            info, parts, keys = None, None, set()
        if prefix == base and event == 'map_key':
            if value in keys:
                raise ValueError('duplicate native message field')
            keys.add(value)
            if value not in ('info', 'parts'):
                raise ValueError('unexpected native message field')
        if prefix == info_prefix and event == 'start_map':
            builder, building, size = ObjectBuilder(), 'info', 0
        elif prefix == parts_prefix and event == 'start_array':
            if info is None:
                raise ValueError('native message parts precede metadata')
            parts = []
            if selected is not None and info['id'] in selected and info['role'] == 'assistant':
                builder, building, size = ObjectBuilder(), 'parts', 0
        if building is not None:
            size += _cost(event, value)
            limit = MAX_METADATA_BYTES if building == 'info' else MAX_MESSAGE_BYTES
            if size > limit:
                raise ValueError('native message snapshot exceeds retained data budget')
            builder.event(event, value)
            if (building == 'info' and prefix == info_prefix and event == 'end_map'):
                info = _metadata(builder.value, session_id)
                builder, building = None, None
            elif building == 'parts' and prefix == parts_prefix and event == 'end_array':
                parts = builder.value
                builder, building = None, None
        if prefix == base and event == 'end_map':
            if info is None or parts is None or keys != {'info', 'parts'}:
                raise ValueError('incomplete native message response')
            yield {'info': info, 'parts': parts}
    if not root_seen or not root_ended:
        raise ValueError('incomplete native message JSON')


def _route(record, *, limit=None, before=None):
    session = urllib.parse.quote(record['session_id'], safe='')
    route = '/session/' + session + '/message'
    query = {}
    if limit is not None:
        query['limit'] = limit
    if before is not None:
        query['before'] = before
    return route + ('?' + urllib.parse.urlencode(query) if query else '')


def recent_boundary(record, timeout=5):
    """Return the latest stored message info without retaining any parts."""
    with open_response(record, _route(record, limit=1), timeout=timeout) as response:
        items = list(_parse_messages(_Reader(response), record['session_id']))
    if len(items) > 1:
        raise ValueError('native server ignored message pagination')
    return items[0]['info'] if items else None


def read_message(record, message_id, timeout=5):
    """Read one message; user parts are validated but never retained."""
    route = _route(record) + '/' + urllib.parse.quote(message_id, safe='')
    with open_response(record, route, timeout=timeout) as response:
        items = list(_parse_messages(_Reader(response), record['session_id'], array=False,
                                     selected={message_id}))
    if len(items) != 1 or items[0]['info']['id'] != message_id:
        raise ValueError('native message lookup returned a different message')
    return items[0]


def iter_turn_messages(record, boundary_id, timeout=5, page_size=PAGE_SIZE, *, include_users=False):
    """Yield this turn in chronological page order, excluding the boundary.

    include_users exposes metadata-only user records (parts=[]), useful for
    correlating auto-compaction continuation messages without retaining input.
    A missing nonempty boundary is an error, never permission to replay history.
    """
    if not isinstance(page_size, int) or not 1 <= page_size <= PAGE_SIZE:
        raise ValueError('invalid native message page size')
    cursor, seen_cursors, seen_messages, pages = None, set(), set(), []
    found, total = boundary_id is None, 0
    with tempfile.TemporaryDirectory(prefix='jingjiaagent-opencode-messages-') as directory:
        for page_index in range(MAX_PAGES):
            # First pass discards all parts while recording metadata. The private
            # spill file allows the second pass to skip old assistant output too,
            # even when it precedes the boundary in the same page.
            with tempfile.TemporaryFile(mode='w+b', dir=directory) as wire:
                with open_response(record, _route(record, limit=page_size, before=cursor), timeout=timeout) as response:
                    items = list(_parse_messages(_Reader(response, wire), record['session_id']))
                    next_cursor = response.headers.get('X-Next-Cursor')
                if len(items) > page_size:
                    raise ValueError('native server ignored message pagination')
                if next_cursor is not None and (not next_cursor or len(next_cursor) > 4096):
                    raise ValueError('invalid native message cursor')
                if next_cursor is not None and next_cursor in seen_cursors:
                    raise ValueError('native message cursor repeated')
                ids = [item['info']['id'] for item in items]
                if len(set(ids)) != len(ids) or any(identifier in seen_messages for identifier in ids):
                    raise ValueError('native message pages overlap')
                seen_messages.update(ids)
                if boundary_id in ids:
                    items = items[ids.index(boundary_id) + 1:]
                    found = True
                selected = {item['info']['id'] for item in items}
                wire.seek(0)
                page_path = pathlib.Path(directory) / (str(page_index) + '.jsonl')
                # TemporaryDirectory uses mode 0700. Files never leave it.
                with page_path.open('w', encoding='utf-8') as target:
                    for item in _parse_messages(wire, record['session_id'], selected=selected):
                        info = item['info']
                        if info['id'] not in selected or (info['role'] == 'user' and not include_users):
                            continue
                        encoded = json.dumps(item, ensure_ascii=False, separators=(',', ':'), allow_nan=False)
                        total += len(encoded.encode('utf-8')) + 1
                        if total > MAX_TURN_BYTES:
                            raise ValueError('native turn snapshot exceeds temporary storage budget')
                        target.write(encoded + '\n')
                pages.append(page_path)
            if (boundary_id is not None and found) or next_cursor is None:
                break
            if not items:
                raise ValueError('native empty message page has continuation cursor')
            seen_cursors.add(next_cursor)
            cursor = next_cursor
        else:
            raise ValueError('native message pagination exceeds page budget')
        if not found:
            raise ValueError('native turn boundary is missing')
        for page_path in reversed(pages):
            with page_path.open('r', encoding='utf-8') as source:
                for line in source:
                    yield json.loads(line)


def submit_prompt(record, body, timeout=86400):
    """Drain and validate a synchronous prompt response; return metadata only.

    Final parts are reconciled through message snapshots, not held here as a
    second complete copy. HTTP success alone must not imply task success. Abort
    metadata is preserved for the caller's cancellation handling; other native
    failures produce a sanitized error without provider response bodies.
    """
    with open_response(record, _route(record), body=body, timeout=timeout) as response:
        items = list(_parse_messages(_Reader(response), record['session_id'], array=False))
    if len(items) != 1 or items[0]['info']['role'] != 'assistant':
        raise ValueError('native prompt did not return an assistant message')
    info = items[0]['info']
    if info.get('error') and info['error'].get('name') != 'MessageAbortedError':
        raise ValueError('native Agent returned an execution error')
    return info
