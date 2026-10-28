"""Offline integration: real Node runner, Python bridge and pinned OpenCode.

Only the model is a localhost fixture. Run inside the built Guest with Docker
--network none. Request observation is installed in a temporary sitecustomize,
not the production bridge, and never records prompts, auth or response bodies.
"""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import threading
import time
import unittest


RUNNER = Path('/opt/agent-compose-runtime/dist/runners/opencode.js')
NODE_DRIVER = r"""
import fs from 'node:fs/promises';
import path from 'node:path';
import { OpenCodeRunner } from '/opt/agent-compose-runtime/dist/runners/opencode.js';
const root = process.argv[2];
const input = JSON.parse(await fs.readFile(path.join(root, 'input.json'), 'utf8'));
const results = [];
for (const [index, prompt] of input.prompts.entries()) {
  process.env.AGENT_COMPOSE_RUN_ID = `sse-fixture-${process.pid}-${index}`;
  const events = [];
  let transcript = '';
  const writer = {write: text => { transcript += text; },
    line: text => { transcript += text + '\n'; }, transcript: () => transcript};
  const runner = new OpenCodeRunner({provider: 'opencode',
    stateRoot: path.join(root, 'state'), sessionRoot: path.join(root, 'state'),
    workspace: path.join(root, 'workspace'), home: path.join(root, 'home'),
    runtimeRoot: path.join(root, 'runtime'), model: 'fixture/sse-regression',
    systemContext: 'Use the fixture response and execute the requested tool.',
    onEvent: event => events.push({at: Date.now() / 1000, ...event})}, writer);
  const result = await runner.runPrompt(prompt);
  results.push({threadId: result.threadId, finalText: result.finalText,
    stopReason: result.stopReason, events});
}
await fs.writeFile(path.join(root, 'result.json'), JSON.stringify(results));
"""

# A real stdio MCP server makes OpenCode persist its own JSON tool arguments.
# That serialization expands 1e20 into an integer literal wider than uint64.
NUMERIC_MCP = r"""
import json
import sys
from pathlib import Path

workspace = Path(sys.argv[1])
for line in sys.stdin:
    request = json.loads(line)
    if 'id' not in request:
        continue
    method = request['method']
    if method == 'initialize':
        result = {'protocolVersion': '2024-11-05', 'capabilities': {'tools': {}},
                  'serverInfo': {'name': 'numeric-fixture', 'version': '1.0'}}
    elif method == 'tools/list':
        result = {'tools': [{'name': 'calculate', 'description': 'Record numeric arguments',
            'inputSchema': {'type': 'object', 'properties': {'value': {'type': 'number'},
                'fraction': {'type': 'number'}, 'round': {'type': 'integer'}},
                'required': ['value', 'fraction', 'round']}}]}
    elif method == 'tools/call':
        args = request['params']['arguments']
        number = args['round']
        marker = f'SSE_TOOL_ROUND_{number}_OK'
        (workspace / f'sse-round-{number}.txt').write_text(marker + '\n')
        (workspace / f'numeric-round-{number}.json').write_text(json.dumps(args))
        result = {'content': [{'type': 'text', 'text': marker}],
                  'structuredContent': args}
    else:
        result = {}
    print(json.dumps({'jsonrpc': '2.0', 'id': request['id'], 'result': result}), flush=True)
"""

# Observe the actual bridge HTTP requests without replacing any real transport.
# Python is a child of Node, so PYTHONPATH is the only instrumentation needed.
OBSERVE_HTTP = r"""
import json
import os
import sys
import time
import urllib.parse
import urllib.request

if sys.argv and sys.argv[0].endswith('jingjiaagent-opencode.py'):
    _open = urllib.request.OpenerDirector.open
    _dropped = False
    _delayed_reconnect = False
    def _log(entry):
        line = (json.dumps(entry) + '\n').encode()
        fd = os.open(os.environ['SSE_FIXTURE_REQUEST_LOG'],
            os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
        try:
            os.write(fd, line)
        finally:
            os.close(fd)
    class _DropOneEventStream:
        def __init__(self, source):
            self.source = source
            self.drop_after_frame = False
            self.drop_now = False
        def __enter__(self):
            self.source.__enter__()
            return self
        def __exit__(self, *args):
            return self.source.__exit__(*args)
        def __getattr__(self, name):
            return getattr(self.source, name)
        def readline(self, size=-1):
            global _dropped
            if self.drop_now:
                _dropped = True
                self.source.close()
                _log({'at': time.time(), 'method': 'FAULT', 'path': '/event',
                      'run': os.environ.get('AGENT_COMPOSE_RUN_ID', '')})
                raise OSError('fixture disconnected native SSE')
            line = self.source.readline(size)
            if not _dropped and line.startswith(b'data:') and len(line) < 1024 * 1024:
                event = json.loads(line[5:])
                if event.get('type') == 'message.part.delta' and event.get('properties', {}).get('delta') == 'SSE_':
                    self.drop_after_frame = True
            if self.drop_after_frame and not line.strip():
                self.drop_now = True
            return line
    def _observed_open(self, fullurl, data=None, timeout=None):
        global _delayed_reconnect
        url = fullurl.full_url if isinstance(fullurl, urllib.request.Request) else fullurl
        target = urllib.parse.urlsplit(url)
        if target.hostname == '127.0.0.1':
            query = urllib.parse.parse_qs(target.query)
            entry = {'at': time.time(), 'method': fullurl.get_method()
                if isinstance(fullurl, urllib.request.Request) else 'GET',
                'path': target.path, 'query_keys': sorted(query),
                'limit': query.get('limit', []),
                'run': os.environ.get('AGENT_COMPOSE_RUN_ID', '')}
            _log(entry)
        fault_enabled = os.environ.get('SSE_FIXTURE_DROP_STREAM') == '1'
        if fault_enabled and target.path == '/event' and _dropped and not _delayed_reconnect:
            _delayed_reconnect = True
            time.sleep(1.1)  # Real deltas are emitted while no subscription exists.
        response = _open(self, fullurl, data=data, timeout=timeout)
        if fault_enabled and target.path == '/event' and not _dropped:
            return _DropOneEventStream(response)
        return response
    urllib.request.OpenerDirector.open = _observed_open
"""


def message_text(message):
    content = message.get('content', '')
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return ''.join(part.get('text', '') for part in content
                       if isinstance(part, dict) and part.get('type') == 'text')
    return ''


class FixtureServer(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, tools, compaction=False, overflow=False, numeric_mcp=False):
        super().__init__(('127.0.0.1', 0), FixtureHandler)
        self.tools = tools
        self.compaction = compaction
        self.compacted = False
        self.overflow = overflow
        self.overflow_injected = False
        self.numeric_mcp = numeric_mcp
        self.requests = []
        self.final_streams = []
        self.lock = threading.Lock()


class FixtureHandler(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *_args):
        pass

    def do_POST(self):
        size = int(self.headers.get('Content-Length', '0'))
        if self.path != '/v1/chat/completions' or not 0 < size <= 64 << 20:
            self.send_error(400, 'unexpected fixture request')
            return
        payload = json.loads(self.rfile.read(size))
        messages = payload.get('messages', [])
        user_index = max((index for index, message in enumerate(messages)
                          if message.get('role') == 'user'), default=-1)
        user = message_text(messages[user_index]) if user_index >= 0 else ''
        match = re.search(r'SSE_ROUND_(\d+)', user)
        # OpenCode 1.18.9 buildPrompt() uses this explicit final user prompt,
        # with no tools. Recognize the real native request, not a fake event.
        summary = self.server.compaction and user.startswith((
            'Create a new anchored summary from the conversation history.',
            'Update the anchored summary below using the conversation history above.'))
        if self.server.compaction and match is None:
            markers = re.findall(r'SSE_ROUND_(\d+)', '\n'.join(map(message_text, messages)))
            match = re.search(r'SSE_ROUND_(\d+)', 'SSE_ROUND_' + markers[-1]) if markers else None
        current = int(match[1]) if match else 0
        if self.server.overflow and current == 2 and not summary and not self.server.overflow_injected:
            self.server.overflow_injected = True
            with self.server.lock:
                self.server.requests.append(dict(round=current, overflow=True, summary=False,
                    tool=False, user_bytes=len(user.encode('utf-8')), has_summary=False,
                    has_first_round=True))
            # Exercise provider rejection recovery, not the separate native
            # pre-emptive compaction triggered by reported token usage below.
            self.respond(json.dumps({'error': {
                'message': "This model's maximum context length is 4096 tokens. "
                           'However, your messages resulted in 5000 tokens. '
                           'Please reduce the length of the messages.',
                'type': 'invalid_request_error', 'code': 'context_length_exceeded'}}).encode(), status=400)
            return
        marker = f'SSE_TOOL_ROUND_{current}_OK'
        complete = any(message.get('role') == 'tool' and marker in message_text(message)
                       for message in messages[user_index + 1:])
        if self.server.compaction and current == 1 and self.server.compacted:
            complete = True
        call = None
        if current and self.server.tools and not complete and not summary:
            tool_names = {tool.get('function', {}).get('name')
                          for tool in payload.get('tools', [])}
            tool_name = 'numeric_calculate' if self.server.numeric_mcp else 'bash'
            if tool_name not in tool_names:
                self.send_error(400, 'real OpenCode did not provide ' + tool_name)
                return
            command = f"printf '{marker}\\n' > sse-round-{current}.txt; cat sse-round-{current}.txt"
            arguments = ({'value': 1e20 if current == 1 else -1e20, 'fraction': 0.125,
                          'round': current} if self.server.numeric_mcp else {
                'command': command, 'description': 'Write the local SSE regression marker',
                'timeout': 10000})
            call = {'index': 0, 'id': f'sse-tool-{current}', 'type': 'function',
                'function': {'name': tool_name, 'arguments': json.dumps(arguments)}}
        with self.server.lock:
            self.server.requests.append({'round': current, 'tool': bool(call), 'summary': summary,
                'user_bytes': len(user.encode('utf-8')),
                'has_summary': any('SSE_COMPACTION_SUMMARY:' in message_text(message)
                                   for message in messages),
                'has_first_round': any('SSE_ROUND_1' in message_text(message)
                                      for message in messages if message.get('role') == 'user')})
        reply = f'SSE_FINAL_ROUND_{current}' if current else 'AUXILIARY'
        if summary:
            self.server.compacted = True
            reply = 'SSE_COMPACTION_SUMMARY: SSE_ROUND_1 tool completed; continue with final answer.'
        identity = {'id': 'fixture-' + str(time.time_ns()), 'created': int(time.time()),
                    'model': 'sse-regression'}
        usage = {'prompt_tokens': 100, 'completion_tokens': 20, 'total_tokens': 120}
        if self.server.compaction and current == 1 and call:
            usage = {'prompt_tokens': 4000, 'completion_tokens': 20, 'total_tokens': 4020}
        finish = 'tool_calls' if call else 'stop'
        if not payload.get('stream'):
            message = {'role': 'assistant', 'content': None,
                'tool_calls': [{key: value for key, value in call.items() if key != 'index'}]}
            if not call:
                message = {'role': 'assistant', 'content': reply}
            self.respond(json.dumps({**identity, 'object': 'chat.completion', 'usage': usage,
                'choices': [{'index': 0, 'message': message, 'finish_reason': finish}]}).encode())
            return
        identity['object'] = 'chat.completion.chunk'
        deltas = [{'role': 'assistant', 'tool_calls': [call]}] if call else [
            {'role': 'assistant', 'content': reply[:4]},
            {'content': reply[4:10]}, {'content': reply[10:]}]
        chunks = [{**identity, 'choices': [{'index': 0, 'delta': delta, 'finish_reason': None}]}
                  for delta in deltas]
        chunks.append({**identity, 'choices': [{'index': 0, 'delta': {},
            'finish_reason': finish}], 'usage': usage})
        packets = [('data: ' + json.dumps(chunk) + '\n\n').encode() for chunk in chunks]
        packets.append(b'data: [DONE]\n\n')
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.send_header('Content-Length', str(sum(map(len, packets))))
        self.end_headers()
        started = time.time()
        try:
            for packet in packets:
                self.wfile.write(packet)
                self.wfile.flush()
                # Slow real generation makes polling and late/buffered text visible.
                if current and not call and not summary:
                    time.sleep(.55)
        finally:
            if current and not call and not summary:
                with self.server.lock:
                    self.server.final_streams.append({'round': current, 'start': started, 'end': time.time()})

    def respond(self, data, status=200):
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)


@unittest.skipUnless(sys.platform == 'linux', 'requires the Linux Guest image')
class OpenCodeSSETest(unittest.TestCase):
    def run_rounds(self, prompts, tools=False, expect_gap=False, disconnect=False, compaction=False,
                   overflow=False, numeric_mcp=False):
        self.assertTrue(RUNNER.is_file(), 'run inside the built Guest image')
        fixture = FixtureServer(tools, compaction=compaction, overflow=overflow, numeric_mcp=numeric_mcp)
        server_thread = threading.Thread(target=fixture.serve_forever, daemon=True)
        server_thread.start()
        try:
            with tempfile.TemporaryDirectory(prefix='opencode-sse-') as directory:
                root = Path(directory)
                for name in ('home', 'workspace', 'state', 'runtime', 'observe'):
                    (root / name).mkdir()
                (root / 'input.json').write_text(json.dumps({'prompts': prompts}), encoding='utf-8')
                (root / 'driver.mjs').write_text(NODE_DRIVER, encoding='utf-8')
                (root / 'observe/sitecustomize.py').write_text(OBSERVE_HTTP, encoding='utf-8')
                config = {'enabled_providers': ['fixture'], 'provider': {'fixture': {
                    'npm': '@ai-sdk/openai-compatible', 'name': 'fixture',
                    'options': {'baseURL': f'http://127.0.0.1:{fixture.server_port}/v1',
                                'apiKey': 'local-fixture-only'},
                    'models': {'sse-regression': {'name': 'sse-regression',
                        'limit': {'context': 4096 if compaction else 1000000, 'output': 1024}}}}},
                    'permission': 'allow' if tools else 'deny'}
                if numeric_mcp:
                    (root / 'numeric_mcp.py').write_text(NUMERIC_MCP, encoding='utf-8')
                    config['mcp'] = {'numeric': {'type': 'local', 'enabled': True,
                        'command': ['/opt/office/venv/bin/python', str(root / 'numeric_mcp.py'),
                                    str(root / 'workspace')]}}
                # BuildSandboxExecEnv replaces the image PATH during real tasks.
                # Keep the office venv absent so this exercises the bridge's
                # explicit interpreter and cannot pass via Dockerfile ENV alone.
                env = {'PATH': '/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin',
                    'HOME': str(root / 'home'), 'LANG': 'C.UTF-8',
                    'PYTHONPATH': str(root / 'observe'),
                    'SSE_FIXTURE_REQUEST_LOG': str(root / 'requests.jsonl'),
                    'OPENCODE_CONFIG_CONTENT': json.dumps(config),
                    'OPENCODE_DISABLE_AUTOUPDATE': 'true', 'OPENCODE_DISABLE_MODELS_FETCH': '1',
                    'OPENCODE_DISABLE_DEFAULT_PLUGINS': 'true', 'DISABLE_TELEMETRY': '1'}
                if disconnect:
                    env['SSE_FIXTURE_DROP_STREAM'] = '1'
                process = subprocess.run(['node', str(root / 'driver.mjs'), str(root)],
                    cwd=root / 'workspace', env=env, capture_output=True, timeout=240)
                # Errors can contain the very large submitted text, so only a
                # scrubbed code is surfaced by acceptance rather than raw output.
                self.assertEqual(process.returncode, 0,
                    'real runner/bridge/OpenCode failed (process output withheld)')
                results = json.loads((root / 'result.json').read_text(encoding='utf-8'))
                requests = [json.loads(line) for line in
                            (root / 'requests.jsonl').read_text(encoding='utf-8').splitlines()]
                files = {path.name: path.read_text() for path in (root / 'workspace').glob('sse-round-*.txt')}
                if numeric_mcp:
                    for current in range(1, len(prompts) + 1):
                        received = json.loads((root / f'workspace/numeric-round-{current}.json').read_text())
                        self.assertEqual(received, dict(value=1e20 if current == 1 else -1e20,
                                                        fraction=0.125, round=current))
                deadline = time.monotonic() + 5
                while len(fixture.final_streams) < len(prompts) and time.monotonic() < deadline:
                    time.sleep(.02)
                with fixture.lock:
                    model_requests = list(fixture.requests)
                    streams = list(fixture.final_streams)
        finally:
            fixture.shutdown()
            fixture.server_close()
            server_thread.join(timeout=5)
        self.assertEqual(len(results), len(prompts))
        self.assertTrue(results[0]['threadId'])
        self.assertTrue(all(result['threadId'] == results[0]['threadId'] for result in results))
        for current, result in enumerate(results, start=1):
            expected = f'SSE_FINAL_ROUND_{current}'
            self.assertEqual(result['finalText'], expected)
            self.assertIn(result['stopReason'], ('stop', 'completed'))
            deltas = [event for event in result['events'] if event['kind'] == 'text_delta']
            self.assertEqual(''.join(event['text'] for event in deltas), expected,
                             'text duplicated or an earlier round was replayed; fixture metadata: '
                             + json.dumps({'round': current, 'events': deltas, 'requests': model_requests}))
            if not expect_gap:
                interval = next(stream for stream in streams if stream['round'] == current)
                self.assertTrue(any(event['at'] < interval['end'] - 1 for event in deltas),
                                'text was buffered until final result instead of streamed')
        collections = [request for request in requests if request['method'] == 'GET'
                       and re.fullmatch(r'/session/[^/]+/message', request['path'])]
        self.assertTrue(collections, 'request observer or final reconciliation did not run')
        self.assertTrue(all(request['limit'] and 0 < int(request['limit'][0]) <= 100
                            for request in collections), 'unbounded full-history read detected')
        if not expect_gap:
            for interval in streams:
                self.assertFalse(any(interval['start'] + .35 < request['at'] < interval['end'] - .8
                                     for request in collections),
                                 'normal SSE generation still polls message history')
        if disconnect:
            faults = [request for request in requests if request['method'] == 'FAULT']
            self.assertEqual(len(faults), len(prompts), 'native event fault was not exercised')
            for fault in faults:
                self.assertTrue(any(request['run'] == fault['run'] and request['at'] > fault['at']
                                    for request in collections), 'disconnect was not reconciled')
        retained = 'has_summary' if compaction else 'has_first_round'
        self.assertTrue(any(request['round'] == 2 and request[retained]
                            for request in model_requests), 'native continuation lost retained context')
        print('real OpenCode SSE regression passed: ' + json.dumps({
            'rounds': len(results), 'tools': tools, 'collection_reads': len(collections),
            'largest_user_bytes': max(request['user_bytes'] for request in model_requests)}), flush=True)
        return results, files, model_requests

    def test_live_events_tools_and_session_reuse(self):
        results, files, requests = self.run_rounds(['SSE_ROUND_1', 'SSE_ROUND_2'], tools=True)
        self.assertEqual(files, {'sse-round-1.txt': 'SSE_TOOL_ROUND_1_OK\n',
                                 'sse-round-2.txt': 'SSE_TOOL_ROUND_2_OK\n'})
        for current, result in enumerate(results, start=1):
            events = result['events']
            tool_results = [event for event in events if event['kind'] == 'tool_result']
            self.assertEqual(len(tool_results), 1, 'tool completion duplicated or omitted')
            self.assertTrue(tool_results[0]['ok'])
            self.assertIn(f'SSE_TOOL_ROUND_{current}_OK', tool_results[0]['output'])
            usage = [event for event in events if event['kind'] == 'usage']
            self.assertEqual(len(usage), 2, 'tool/final step usage duplicated or omitted')
            self.assertEqual(sum(event['inputTokens'] for event in usage), 200)
            self.assertEqual(sum(event['outputTokens'] for event in usage), 40)
            self.assertEqual(len([event for event in events if event['kind'] == 'step_end']), 2)
        self.assertEqual(len([request for request in requests if request['tool']]), 2)

    def test_single_large_message_and_accumulated_history(self):
        # This intentionally synthetic payload targets the old response cap;
        # it does not claim an actual provider accepts 17 MiB of semantic text.
        large = 'SSE_ROUND_1\n' + (' ' * (17 << 20)) + '\nLARGE_END'
        # The huge user-part SSE frame deliberately exceeds the frame guard.
        # Snapshot fallback is expected; only the ordinary fixture above must
        # remain live throughout. Exact final output remains mandatory here.
        _, _, requests = self.run_rounds([large, 'SSE_ROUND_2'], expect_gap=True)
        self.assertGreater(max(request['user_bytes'] for request in requests), 16 << 20)

    def test_real_event_disconnect_repairs_missing_generation(self):
        results, _, _ = self.run_rounds(['SSE_ROUND_1', 'SSE_ROUND_2'],
                                       expect_gap=True, disconnect=True)
        for result in results:
            self.assertEqual(len([event for event in result['events'] if event['kind'] == 'usage']), 1)

    def test_native_auto_compaction_keeps_summary_out_of_reply(self):
        results, files, requests = self.run_rounds(['SSE_ROUND_1', 'SSE_ROUND_2'],
                                                 tools=True, compaction=True)
        self.assertEqual(len([request for request in requests if request['summary']]), 1,
                         'real OpenCode automatic compaction was not triggered exactly once')
        self.assertEqual(files['sse-round-1.txt'], 'SSE_TOOL_ROUND_1_OK\n')
        self.assertEqual(files['sse-round-2.txt'], 'SSE_TOOL_ROUND_2_OK\n')
        self.assertEqual(len([event for event in results[0]['events'] if event['kind'] == 'usage']), 3)

    def test_provider_context_overflow_recovers_then_continues_same_session(self):
        # Native recovery may return a non-streaming replay response; exact
        # output/session reuse matter here, not the ordinary streaming timing.
        results, _, requests = self.run_rounds(
            ['SSE_ROUND_1', 'SSE_ROUND_2', 'SSE_ROUND_3'], compaction=True,
            overflow=True, expect_gap=True)
        self.assertEqual(sum(bool(request.get('overflow')) for request in requests), 1)
        self.assertEqual(sum(request['summary'] for request in requests), 1)
        self.assertTrue(any(request['round'] == 3 and request['has_summary'] for request in requests))
        self.assertFalse(any(event['kind'] == 'error' for result in results for event in result['events']))

    def test_real_mcp_large_integer_and_fraction_arguments_survive_snapshots(self):
        results, files, requests = self.run_rounds(
            ['SSE_ROUND_1', 'SSE_ROUND_2'], tools=True, numeric_mcp=True)
        self.assertEqual(files, {'sse-round-1.txt': 'SSE_TOOL_ROUND_1_OK\n',
                                'sse-round-2.txt': 'SSE_TOOL_ROUND_2_OK\n'})
        self.assertEqual(sum(request['tool'] for request in requests), 2)
        for current, result in enumerate(results, start=1):
            completed = [event for event in result['events'] if event['kind'] == 'tool_call'
                         and event.get('status') == 'completed']
            self.assertEqual(len(completed), 1)
            self.assertEqual(completed[0]['input'], dict(
                value=1e20 if current == 1 else -1e20, fraction=0.125, round=current))


if __name__ == '__main__':
    unittest.main()
