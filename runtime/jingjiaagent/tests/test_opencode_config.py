"""Native managed-config and skill discovery regression, with --network none.

Fixtures use local files and native --pure inspection. No model or package
downloads are needed to verify the configuration layers and per-run lifetime.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
RUNNER = Path('/opt/agent-compose-runtime/dist/runners/opencode.js')
MANAGED = Path('/etc/opencode/opencode.json')
NODE_DRIVER = r"""
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import { spawnSync } from 'node:child_process';
import { OpenCodeRunner } from '/opt/agent-compose-runtime/dist/runners/opencode.js';
const root = process.argv[2];
const writer = {write(){}, line(){}, transcript(){ return ''; }};
const make = policy => new OpenCodeRunner({home: path.join(root, 'home'),
  workspace: path.join(root, 'workspace'), sessionRoot: path.join(root, 'state'),
  runtimeRoot: path.join(root, 'runtime'), systemContext: policy}, writer);
const previous = new Set(await fs.readdir(os.tmpdir()));
const parentEnv = {config: process.env.OPENCODE_CONFIG, inline: process.env.OPENCODE_CONFIG_CONTENT,
  directory: process.env.OPENCODE_CONFIG_DIR,
  instructions: process.env.JINGJIAAGENT_SYSTEM_INSTRUCTIONS};
const first = make('FIRST_RUN_POLICY');
const second = make('SECOND_RUN_POLICY');
const result = {};
try {
  const configured = await Promise.allSettled([first.environment(), second.environment()]);
  const failed = configured.find(item => item.status === 'rejected');
  if (failed) throw failed.reason;
  const [a, b] = configured.map(item => item.value);
  result.paths = [a.JINGJIAAGENT_SYSTEM_INSTRUCTIONS, b.JINGJIAAGENT_SYSTEM_INSTRUCTIONS];
  result.modes = await Promise.all(result.paths.map(async file => (await fs.stat(file)).mode & 0o777));
  result.directoryModes = await Promise.all(result.paths.map(async file => (await fs.stat(path.dirname(file))).mode & 0o777));
  result.env = [a, b].map(env => ({config: env.OPENCODE_CONFIG, inline: env.OPENCODE_CONFIG_CONTENT,
    directory: env.OPENCODE_CONFIG_DIR,
    external: env.OPENCODE_DISABLE_EXTERNAL_SKILLS}));
  if (process.env.FIXTURE_OPERATION?.startsWith('verify-')) {
    const candidate = {...a};
    if (process.env.FIXTURE_OPERATION === 'verify-missing-env') delete candidate.JINGJIAAGENT_SYSTEM_INSTRUCTIONS;
    await first.verifySystemInstructions(candidate);
    result.verified = true;
  }
  const command = process.env.FIXTURE_OPERATION === 'skills' ? 'skill' : 'config';
  result.native = [a, b].map(env => {
    const native = spawnSync('opencode', ['debug', command, '--pure'], {
      cwd: path.join(root, 'workspace'), env, encoding: 'utf8', timeout: 10000,
    });
    if (native.status !== 0) throw new Error(`native ${command} load failed (${native.status})`);
    return JSON.parse(native.stdout);
  });
  result.policies = await Promise.all(result.paths.map(file => fs.readFile(file, 'utf8')));
  await first.cleanupSystemInstructions();
  result.secondSurvived = !!(await fs.stat(b.JINGJIAAGENT_SYSTEM_INSTRUCTIONS));
} catch (error) {
  result.error = error.message;
} finally {
  await first.cleanupSystemInstructions();
  await second.cleanupSystemInstructions();
}
result.parentEnvUnchanged = JSON.stringify(parentEnv) === JSON.stringify({config: process.env.OPENCODE_CONFIG,
  inline: process.env.OPENCODE_CONFIG_CONTENT, directory: process.env.OPENCODE_CONFIG_DIR,
  instructions: process.env.JINGJIAAGENT_SYSTEM_INSTRUCTIONS});
result.remainingPolicies = (await fs.readdir(os.tmpdir())).filter(name => name.startsWith('agent-compose-opencode-') && !previous.has(name));
console.log(JSON.stringify(result));
"""


@unittest.skipUnless(sys.platform == 'linux', 'requires the Linux Guest image')
class OpenCodeConfigTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(RUNNER.is_file(), 'run inside the built Guest image')
        self.directory = tempfile.TemporaryDirectory(prefix='opencode-config-')
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        for name in ('home', 'base', 'workspace', 'state', 'runtime'):
            (self.root / name).mkdir()
        self.base = self.root / 'base/opencode.jsonc'
        (self.root / 'driver.mjs').write_text(NODE_DRIVER, encoding='utf-8')
        self.env = {key: value for key, value in os.environ.items()
                    if not key.startswith('OPENCODE_') and key != 'JINGJIAAGENT_SYSTEM_INSTRUCTIONS'}
        self.env.update(HOME=str(self.root / 'home'), XDG_CONFIG_HOME=str(self.root / 'home/.config'),
                        XDG_DATA_HOME=str(self.root / 'home/.local/share'),
                        XDG_STATE_HOME=str(self.root / 'home/.local/state'),
                        XDG_CACHE_HOME=str(self.root / 'home/.cache'),
                        OPENCODE_CONFIG=str(self.base), OPENCODE_DISABLE_DEFAULT_PLUGINS='true',
                        OPENCODE_DISABLE_MODELS_FETCH='1', OPENCODE_DISABLE_AUTOUPDATE='true',
                        npm_config_offline='true')
        self.tracked = {}

    def write(self, path, text):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding='utf-8')
        self.tracked[path] = path.read_bytes()
        return path

    def write_config(self, path, config):
        # All files are JSONC with comments and trailing commas; the runner must
        # leave native parsing and relative expansion to OpenCode.
        text = json.dumps({'$schema': 'https://opencode.ai/config.json', **config}, indent=2)
        return self.write(path, '// native JSONC fixture\n' + text[:-1] + ',\n}\n')

    def run_driver(self, **options):
        completed = subprocess.run(['node', str(self.root / 'driver.mjs'), str(self.root)],
                                   cwd=self.root / 'workspace', env=self.env, capture_output=True,
                                   text=True, timeout=45, **options)
        self.assertEqual(completed.returncode, 0, 'fixture runner failed (output withheld)')
        result = json.loads(completed.stdout)
        for path, content in self.tracked.items():
            self.assertEqual(path.read_bytes(), content, 'original native configuration or reference changed')
        self.assertTrue(result['parentEnvUnchanged'])
        self.assertEqual(result['remainingPolicies'], [])
        self.assertEqual(sorted(p.name for p in self.base.parent.iterdir()),
                         sorted(p.name for p in self.tracked if p.parent == self.base.parent))
        return result

    def test_official_managed_hook_only_adds_instructions(self):
        self.assertEqual(MANAGED.read_bytes(), (ROOT / 'opencode/managed.json').read_bytes())
        config = json.loads(MANAGED.read_text())
        self.assertEqual(set(config), {'$schema', 'instructions'})
        self.assertEqual(config['instructions'], ['{env:JINGJIAAGENT_SYSTEM_INSTRUCTIONS}'])
        self.assertEqual(MANAGED.parent.stat().st_mode & 0o777, 0o755)
        self.assertEqual(MANAGED.stat().st_mode & 0o777, 0o444)

    def test_all_native_config_layers_relative_references_and_run_isolation(self):
        global_dir = self.root / 'home/.config/opencode'
        self.write(global_dir / 'global.txt', 'GLOBAL_RELATIVE_PROMPT')
        self.write_config(global_dir / 'opencode.jsonc', {
            'agent': {'global-fixture': {'prompt': '{file:./global.txt}'}},
            'instructions': ['global-rule.md'], 'permission': {'read': 'allow'}})
        self.write(self.root / 'base/prompt.txt', 'RELATIVE_AGENT_PROMPT')
        self.write(self.root / 'base/key.txt', 'fixture-key-only')
        plugin = self.write(self.root / 'base/plugin.mjs', 'export const Fixture=async()=>({});')
        self.write_config(self.base, {
            'agent': {'build': {'prompt': '{file:./prompt.txt}'}},
            'provider': {'fixture': {'options': {'apiKey': '{file:./key.txt}'}}},
            'plugin': ['./plugin.mjs'], 'instructions': ['base-rule.md'],
            'permission': {'bash': 'ask'}, 'skills': {'paths': ['/fixture/selected-skills']},
            'mcp': {'fixture': {'type': 'remote', 'url': 'http://fixture.invalid/mcp', 'enabled': False}},
        })
        self.write(self.root / 'workspace/project.txt', 'PROJECT_RELATIVE_PROMPT')
        self.write_config(self.root / 'workspace/opencode.jsonc', {
            'agent': {'project-fixture': {'prompt': '{file:./project.txt}'}},
            'instructions': ['project-rule.md']})
        extra_dir = self.root / 'extra-config'
        self.write_config(extra_dir / 'opencode.jsonc', {'instructions': ['directory-rule.md']})
        self.env['OPENCODE_CONFIG_DIR'] = str(extra_dir)
        self.env['OPENCODE_CONFIG_CONTENT'] = ('{// inline JSONC\n"instructions":["inline-rule.md"],'
                                               '"permission":{"edit":"deny"},}')
        result = self.run_driver()
        self.assertNotIn('error', result)
        self.assertEqual(result['policies'], ['FIRST_RUN_POLICY', 'SECOND_RUN_POLICY'])
        self.assertNotEqual(*result['paths'])
        self.assertEqual(result['modes'], [0o600, 0o600])
        self.assertEqual(result['directoryModes'], [0o700, 0o700])
        self.assertTrue(result['secondSurvived'])
        for environment, instruction, config in zip(result['env'], result['paths'], result['native']):
            self.assertEqual(environment['config'], str(self.base))
            self.assertEqual(environment['inline'], self.env['OPENCODE_CONFIG_CONTENT'])
            self.assertEqual(environment['directory'], str(extra_dir))
            self.assertEqual(config['agent']['global-fixture']['prompt'], 'GLOBAL_RELATIVE_PROMPT')
            self.assertEqual(config['agent']['build']['prompt'], 'RELATIVE_AGENT_PROMPT')
            self.assertEqual(config['agent']['project-fixture']['prompt'], 'PROJECT_RELATIVE_PROMPT')
            self.assertEqual(config['provider']['fixture']['options']['apiKey'], 'fixture-key-only')
            self.assertIn(plugin.as_uri(), config['plugin'])
            for rule in ('global-rule.md', 'base-rule.md', 'project-rule.md', 'directory-rule.md',
                         'inline-rule.md', instruction):
                self.assertEqual(config['instructions'].count(rule), 1)
            self.assertEqual(config['permission'], {'read': 'allow', 'bash': 'ask', 'edit': 'deny'})
            self.assertEqual(config['skills']['paths'], ['/fixture/selected-skills'])
            self.assertFalse(config['mcp']['fixture']['enabled'])

    def test_native_config_error_cleans_run_files(self):
        self.write_config(self.base, {'agent': {'build': {'prompt': '{file:missing.txt}'}}})
        result = self.run_driver()
        self.assertIn('native config load failed', result['error'])

    def test_cli_preflight_rejects_missing_managed_hook_or_run_environment(self):
        self.write_config(self.base, {})
        no_hook = self.root / 'empty-managed'
        no_hook.mkdir()
        for operation in ('verify-missing-hook', 'verify-missing-env'):
            with self.subTest(operation=operation):
                self.env['FIXTURE_OPERATION'] = operation
                if operation == 'verify-missing-hook':
                    self.env['OPENCODE_TEST_MANAGED_CONFIG_DIR'] = str(no_hook)
                else:
                    self.env.pop('OPENCODE_TEST_MANAGED_CONFIG_DIR', None)
                result = self.run_driver()
                self.assertEqual(result.get('error'), 'OpenCode system instructions are not active')
                self.assertNotIn('verified', result)

    @unittest.skipUnless(os.geteuid() == 0, 'readonly fixture drops privileges from Guest root')
    def test_readonly_base_directory_succeeds_without_writes(self):
        self.write_config(self.base, {'instructions': ['readonly-rule.md']})
        for directory, names, files in os.walk(self.root):
            os.chown(directory, 65534, 65534)
            for name in files:
                os.chown(Path(directory) / name, 65534, 65534)
        self.base.parent.chmod(0o555)
        self.base.chmod(0o444)
        result = self.run_driver(user=65534, group=65534)
        self.assertNotIn('error', result)
        self.assertEqual(self.base.parent.stat().st_mode & 0o777, 0o555)
        for instruction, config in zip(result['paths'], result['native']):
            self.assertIn('readonly-rule.md', config['instructions'])
            self.assertIn(instruction, config['instructions'])

    def test_platform_explicit_and_project_skills_preserve_external_switch(self):
        platform = self.root / 'home/.codingmatrix/project-tpl/.ai-ready/skills'
        custom = self.root / 'workspace/custom-skills'
        native = self.root / 'workspace/.opencode/skills'
        external = self.root / 'home/.agents/skills'
        for folder, name in ((platform, 'platform-fixture'), (custom, 'custom-fixture'),
                             (native, 'project-fixture'), (external, 'external-fixture')):
            self.write(folder / name / 'SKILL.md',
                       f'---\nname: {name}\ndescription: Local skill fixture\n---\nFixture body.\n')
        self.write_config(self.base, {'skills': {'paths': [str(platform), str(custom)]}})
        self.env['FIXTURE_OPERATION'] = 'skills'
        for disabled in ('false', 'true'):
            with self.subTest(disable_external=disabled):
                self.env['OPENCODE_DISABLE_EXTERNAL_SKILLS'] = disabled
                result = self.run_driver()
                self.assertNotIn('error', result)
                for environment, skills in zip(result['env'], result['native']):
                    self.assertEqual(environment['external'], disabled)
                    names = {item['name'] for item in skills}
                    self.assertTrue({'platform-fixture', 'custom-fixture', 'project-fixture'} <= names)
                    self.assertEqual('external-fixture' in names, disabled == 'false')


if __name__ == '__main__':
    unittest.main(verbosity=2)
