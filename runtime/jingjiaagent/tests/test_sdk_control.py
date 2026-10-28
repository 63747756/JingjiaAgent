"""Exercise installed SDK controls in the Guest, without a model or network.

Run with Docker --rm --network none. The execution service PATH deliberately
omits the office venv; the Node helper must still start its Python dependencies.
"""
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SDK = Path('/opt/agent-compose-runtime/dist/jingjiaagent-sdk.js')
EXECUTION_PATH = ('/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:'
                  '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin')
NODE_DRIVER = r"""
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { randomUUID } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { sdkDecision, claudeToolDecision } from '/opt/agent-compose-runtime/dist/jingjiaagent-sdk.js';

const [provider, scenario] = process.argv.slice(2);
const task = randomUUID();
const run = `sdk-control-fixture-${randomUUID()}`;
process.env.JINGJIAAGENT_TASK_ID = task;
process.env.AGENT_COMPOSE_RUN_ID = run;
const policies = '/data/state/jingjiaagent-policies';
const interactions = '/data/state/jingjiaagent-interactions';
fs.mkdirSync(policies, { recursive: true });
fs.writeFileSync(`${policies}/${task}.json`, JSON.stringify({
  enabled: scenario === 'auto', revision: 1,
}));

const controller = new AbortController();
const question = scenario === 'question';
const tool = question ? 'AskUserQuestion' : 'Bash';
const input = question
  ? { questions: [{ question: 'Choose the output format', options: [{ label: 'MD' }] }] }
  : { command: 'pwd' };
const answers = [[question ? 'MD' : scenario === 'deny' ? '拒绝' : '允许一次']];
const events = [];
const emit = event => {
  events.push(event);
  if (event.name !== 'question') return;
  assert.equal(event.input.run_id, run);
  assert.equal(event.input.request_kind, question ? 'question' : 'permission');
  if (scenario === 'cancel') { controller.abort(); return; }
  if (scenario === 'auto') return;
  const request = Object.fromEntries(['run_id', 'session_id', 'request_id', 'request_kind']
    .map(key => [key, event.input[key]]));
  Object.assign(request, { answers, cancelled: false });
  // Answer through the installed backend control adapter, including its
  // correlation checks and durable consumption receipt, rather than mock files.
  const reply = spawnSync('/usr/bin/python3', [
    '/opt/agent-compose-runtime/jingjiaagent-interaction.py',
    Buffer.from(JSON.stringify(request)).toString('base64'),
  ], { encoding: 'utf8', timeout: 6000 });
  assert.equal(reply.status, 0, reply.stderr);
  assert.deepEqual(JSON.parse(reply.stdout), { data: { success: true } });
};

try {
  if (provider === 'claude') {
    const result = await claudeToolDecision(tool, input,
      { signal: controller.signal, toolUseID: 'fixture-tool' }, emit);
    assert.equal(result.behavior, ['deny', 'cancel'].includes(scenario) ? 'deny' : 'allow');
    assert.equal(result.toolUseID, 'fixture-tool');
    if (scenario === 'cancel') assert.equal(result.interrupt, true);
    if (question) assert.deepEqual(result.updatedInput.answers,
      { 'Choose the output format': 'MD' });
  } else if (scenario === 'cancel') {
    await assert.rejects(sdkDecision(provider, tool, input, 'fixture-tool', controller.signal, emit),
      /native control canceled/);
  } else {
    const result = await sdkDecision(provider, tool, input, 'fixture-tool', controller.signal, emit);
    assert.deepEqual(result.answers, answers);
    assert.equal(result.cancelled, false);
  }
  assert.equal(events.filter(event => event.name === 'question').length, 1);
  assert.equal(events.filter(event => event.name === 'jingjiaagent_permission_reply').length,
    scenario === 'auto' ? 1 : 0);
  console.log(`${provider} ${scenario}: passed`);
} finally {
  fs.rmSync(`${interactions}/${run}`, { recursive: true, force: true });
  for (const suffix of ['.json', '.policy.json']) fs.rmSync(`${interactions}/${run}${suffix}`, { force: true });
  for (const suffix of ['.json', '.lock']) fs.rmSync(`${policies}/${task}${suffix}`, { force: true });
}
"""


@unittest.skipUnless(sys.platform == 'linux', 'requires the Linux Guest image')
class SDKControlIntegrationTest(unittest.TestCase):
    def test_real_sdk_permissions_questions_and_cancellation(self):
        self.assertTrue(SDK.is_file(), 'run this regression inside the built Guest image')
        with tempfile.TemporaryDirectory(prefix='sdk-control-fixture-') as directory:
            driver = Path(directory) / 'driver.mjs'
            driver.write_text(NODE_DRIVER, encoding='utf-8')
            for provider in ('codex', 'claude'):
                for scenario in ('allow', 'deny', 'auto', 'question', 'cancel'):
                    with self.subTest(provider=provider, scenario=scenario):
                        result = subprocess.run(['node', str(driver), provider, scenario],
                            env={'PATH': EXECUTION_PATH, 'HOME': directory, 'LANG': 'C.UTF-8'},
                            capture_output=True, text=True, encoding='utf-8', timeout=20)
                        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                        self.assertIn(f'{provider} {scenario}: passed', result.stdout)
                        print(result.stdout.strip(), flush=True)


if __name__ == '__main__':
    unittest.main()
