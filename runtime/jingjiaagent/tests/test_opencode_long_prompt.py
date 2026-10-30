"""Run inside the built Guest with --network none; no external model is used.

Exercises the installed Node runner, Python bridge and OpenCode binary together.
Only hashes, sizes and fixture results leave the process, never prompt contents.
"""
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import unittest


RUNNER = Path('/opt/agent-compose-runtime/dist/runners/opencode.js')
REPLY = 'LONG_PROMPT_ACCEPTED'
NODE_DRIVER = r"""
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import { OpenCodeRunner } from '/opt/agent-compose-runtime/dist/runners/opencode.js';
const root = process.argv[2];
const input = JSON.parse(await fs.readFile(path.join(root, 'input.json'), 'utf8'));
const stateRoot = path.join(root, 'state');
const results = [];
for (const prompt of input.prompts) {
  const previousConfigs = new Set(await fs.readdir(os.tmpdir()));
  let transcript = '';
  const writer = {write: text => { transcript += text; },
    line: text => { transcript += text + '\n'; }, transcript: () => transcript};
  const runner = new OpenCodeRunner({provider: 'opencode', stateRoot,
    sessionRoot: stateRoot, workspace: path.join(root, 'workspace'),
    home: path.join(root, 'home'), runtimeRoot: path.join(root, 'runtime'),
    systemContext: input.systemContext, model: 'fixture/long-prompt'}, writer);
  const result = await runner.runPrompt(prompt);
  const uncleanedConfigs = (await fs.readdir(os.tmpdir())).filter(name =>
    name.startsWith('agent-compose-opencode-') && !previousConfigs.has(name));
  uncleanedConfigs.push(...(await fs.readdir(path.join(root, 'home', '.config', 'opencode')).catch(() => []))
    .filter(name => name.startsWith('.agent-compose-opencode-')));
  results.push({threadId: result.threadId, finalText: result.finalText,
    stopReason: result.stopReason, uncleanedConfigs});
}
await fs.writeFile(path.join(root, 'result.json'), JSON.stringify(results));
"""


def message_text(message):
    content = message.get('content', '')
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return ''.join(part.get('text', '') for part in content
                       if isinstance(part, dict) and part.get('type') == 'text')
    return ''


class ModelFixture(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self):
        super().__init__(('127.0.0.1', 0), ModelHandler)
        self.requests = []
        self.lock = threading.Lock()


class ModelHandler(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *_args):
        pass

    def respond(self, data, content_type='application/json', status=200):
        self.send_response(status)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        size = int(self.headers.get('Content-Length', '0'))
        if self.path != '/v1/chat/completions' or not 0 < size <= 8 << 20:
            return self.respond(b'{"error":"unexpected_fixture_request"}', status=400)
        payload = json.loads(self.rfile.read(size))
        users = [message_text(message) for message in payload.get('messages', [])
                 if message.get('role') == 'user']
        system = '\n'.join(message_text(message) for message in payload.get('messages', [])
                           if message.get('role') in ('system', 'developer'))
        with self.server.lock:
            self.server.requests.append(dict(users=users, system=system))
        identity = {'id': 'fixture-' + str(time.time_ns()), 'created': int(time.time()),
                    'model': 'long-prompt'}
        usage = {'prompt_tokens': 100, 'completion_tokens': 10, 'total_tokens': 110}
        if payload.get('stream'):
            identity['object'] = 'chat.completion.chunk'
            chunks = [
                {**identity, 'choices': [{'index': 0, 'delta': {'role': 'assistant',
                    'content': REPLY}, 'finish_reason': None}]},
                {**identity, 'choices': [{'index': 0, 'delta': {},
                    'finish_reason': 'stop'}], 'usage': usage},
            ]
            data = (''.join('data: ' + json.dumps(chunk) + '\n\n' for chunk in chunks)
                    + 'data: [DONE]\n\n').encode('utf-8')
            return self.respond(data, 'text/event-stream')
        response = {**identity, 'object': 'chat.completion', 'usage': usage,
                    'choices': [{'index': 0, 'message': {'role': 'assistant',
                        'content': REPLY}, 'finish_reason': 'stop'}]}
        self.respond(json.dumps(response).encode('utf-8'))


@unittest.skipUnless(sys.platform == 'linux', 'requires the Linux Guest image')
class OpenCodeLongPromptTest(unittest.TestCase):
    def test_real_runner_transmits_long_text_and_reuses_session(self):
        for mode in ('native_bridge', 'cli_fallback'):
            with self.subTest(mode=mode):
                self.check_rounds(mode)

    def check_rounds(self, mode):
        self.assertTrue(RUNNER.is_file(), 'run this regression inside the built Guest image')
        # Every component is independently larger than Linux's single-argument
        # limit, including Unicode and shell-sensitive characters. Files and
        # stdin carry the payload; even the test launcher has only short args.
        system_context = ('SYSTEM_CONTEXT "quoted" \'literal\' $HOME `value` \\\n' * 3500)
        prompts = [
            'ASCII_BEGIN\n' + ('ascii "quoted" \'literal\' $() `value` \\\n' * 4500) + 'ASCII_END',
            '中文开始\n' + ('中文长文本、引号“内容”\" \'值\'、换行和反斜杠\\\n' * 3500) + '中文结束',
        ]
        for value in [system_context, *prompts]:
            self.assertGreater(len(value.encode('utf-8')), 128 * 1024)
        expected = prompts
        fixture = ModelFixture()
        server_thread = threading.Thread(target=fixture.serve_forever, daemon=True)
        server_thread.start()
        try:
            with tempfile.TemporaryDirectory(prefix='opencode-long-prompt-') as directory:
                root = Path(directory)
                for name in ('home', 'workspace', 'state', 'runtime'):
                    (root / name).mkdir()
                (root / 'input.json').write_text(json.dumps({
                    'systemContext': system_context, 'prompts': prompts}, ensure_ascii=False),
                    encoding='utf-8')
                driver = root / 'driver.mjs'
                driver.write_text(NODE_DRIVER, encoding='utf-8')
                config = {'agent': {'title': {'disable': True}},
                    'enabled_providers': ['fixture'], 'provider': {'fixture': {
                    'npm': '@ai-sdk/openai-compatible', 'name': 'fixture',
                    'options': {'baseURL': f'http://127.0.0.1:{fixture.server_port}/v1',
                                'apiKey': 'local-fixture-only'},
                    'models': {'long-prompt': {'name': 'long-prompt',
                        'limit': {'context': 1000000, 'output': 1024}}}}},
                    'permission': 'deny'}
                env = {'PATH': os.environ.get('PATH', '/usr/local/bin:/usr/bin:/bin'),
                    'HOME': str(root / 'home'), 'LANG': 'C.UTF-8',
                    'OPENCODE_CONFIG_CONTENT': json.dumps(config),
                    'OPENCODE_DISABLE_AUTOUPDATE': 'true', 'OPENCODE_DISABLE_MODELS_FETCH': '1',
                    'OPENCODE_DISABLE_DEFAULT_PLUGINS': 'true', 'DISABLE_TELEMETRY': '1'}
                if mode == 'native_bridge':
                    env['AGENT_COMPOSE_RUN_ID'] = 'long-prompt-' + str(os.getpid())
                process = subprocess.run(['node', str(driver), str(root)], cwd=root / 'workspace',
                    env=env, capture_output=True, timeout=180)
                # Do not reflect process output: errors may contain submitted text.
                self.assertEqual(process.returncode, 0,
                    'installed Node runner or native bridge failed (output intentionally withheld)')
                results = json.loads((root / 'result.json').read_text(encoding='utf-8'))
                self.assertEqual(len(results), 2)
                self.assertTrue(results[0]['threadId'])
                self.assertEqual(results[0]['threadId'], results[1]['threadId'])
                for result in results:
                    self.assertEqual(result['uncleanedConfigs'], [], 'Run config outlived the native server')
                    self.assertTrue(result['finalText'] == REPLY,
                                    'runner did not return the fixture response')
                    self.assertIn(result['stopReason'], ('stop', 'completed'))
                with fixture.lock:
                    requests = list(fixture.requests)
                for index, text in enumerate(expected):
                    self.assertTrue(any(text in request['users'] for request in requests),
                        f'round {index + 1} missing exact UTF-8 user prompt')
                main = [request for request in requests if any(text in request['users'] for text in expected)]
                self.assertTrue(main)
                for request in main:
                    self.assertEqual(request['system'].count(system_context), 1,
                                     'system instructions missing or duplicated in the model system channel')
                    self.assertTrue(all(system_context not in user for user in request['users']),
                                    'system instructions leaked into a user message')
                    self.assertIn('You are', request['system'].replace(system_context, ''),
                                  'native OpenCode baseline was not retained')
                self.assertTrue(any(expected[0] in request['users'] and expected[1] in request['users']
                                    for request in requests),
                    'continued model request did not preserve the first user message')
                summary = [{'bytes': len(text.encode('utf-8')),
                            'sha256': hashlib.sha256(text.encode('utf-8')).hexdigest()}
                           for text in expected]
                print(f'real OpenCode {mode} UTF-8 and session regression passed: '
                      + json.dumps(summary), flush=True)
        finally:
            fixture.shutdown()
            fixture.server_close()
            server_thread.join(timeout=5)


if __name__ == '__main__':
    unittest.main()
