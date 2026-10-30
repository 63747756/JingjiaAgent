#!/usr/bin/env python3
"""Nexus defaults and native package-manager overrides; no package downloads."""
import configparser
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PYPI = 'http://mirrors.jingjiamicro.com:8080/repository/pypi-group/simple'
NPM = 'http://mirrors.jingjiamicro.com:8080/repository/npm-group/'
MANAGED_ROOT = '/opt/jingjiaagent/dependencies'
SOURCE_ENV = ('PIP_CONFIG_FILE', 'PIP_INDEX_URL', 'PIP_EXTRA_INDEX_URL', 'PIP_FIND_LINKS',
              'PIP_TRUSTED_HOST', 'NPM_CONFIG_USERCONFIG', 'NPM_CONFIG_REGISTRY', 'npm_config_registry',
              'NPM_CONFIG_GLOBALCONFIG', 'npm_config_globalconfig')


def clean_environment(home):
    # Isolate each fixture from the test runner's own package-manager settings.
    # Production does not clear these variables.
    env = {key: value for key, value in os.environ.items()
           if not key.lower().startswith(('pip_', 'npm_config_'))}
    env.update(HOME=str(home), XDG_CONFIG_HOME=str(home / '.config'))
    return env


def pip_options(python, environment, cwd, arguments=()):
    # Parse real pip options without contacting an index or installing a package.
    script = ('import json,sys;from pip._internal.commands import create_command;'
              'options,args=create_command("install").parse_args(sys.argv[1:]);'
              'print(json.dumps({"index":options.index_url,"extra":options.extra_index_urls,'
              '"find_links":options.find_links}))')
    result = subprocess.run([str(python), '-c', script, *arguments], cwd=cwd, env=environment,
                            check=True, capture_output=True, text=True, timeout=15)
    return json.loads(result.stdout)


class DependencyConfigTests(unittest.TestCase):
    def test_nexus_is_a_global_default(self):
        pip = configparser.ConfigParser()
        pip.read(ROOT / 'dependencies/pip.conf', encoding='utf-8')
        self.assertEqual(pip['global']['index-url'], PYPI)
        self.assertEqual(pip['global']['trusted-host'], 'mirrors.jingjiamicro.com')
        npm = dict(line.split('=', 1) for line in (ROOT / 'dependencies/npmrc').read_text().splitlines() if line)
        self.assertEqual(npm['registry'], NPM)
        self.assertEqual(npm['manage-package-manager-versions'], 'false')

    @unittest.skipUnless(os.environ.get('JINGJIAAGENT_DEPENDENCY_IMAGE_READY') == '1',
                         'Requires the rebuilt Guest image')
    def test_installed_image_uses_native_global_config_without_source_env(self):
        installed = Path(MANAGED_ROOT)
        for name in ('pip.conf', 'npmrc', 'README.md'):
            self.assertEqual((installed / name).read_bytes(), (ROOT / 'dependencies' / name).read_bytes())
        self.assertEqual(Path('/usr/local/bin/agent-compose-runtime').read_bytes(),
                         (ROOT / 'guest-entry.sh').read_bytes())
        self.assertEqual(Path('/etc/pip.conf').read_bytes(), (installed / 'pip.conf').read_bytes())
        for key in SOURCE_ENV:
            self.assertNotIn(key, os.environ, key + ' must not override project configuration')
        with tempfile.TemporaryDirectory(prefix='jingjiaagent-image-defaults-') as directory:
            home = Path(directory)
            environment = clean_environment(home)
            self.assertEqual(pip_options(sys.executable, environment, home)['index'], PYPI)
            global_config = subprocess.check_output(['npm', 'config', 'get', 'globalconfig'],
                                                    cwd=home, env=environment, text=True).strip()
            self.assertEqual(Path(global_config).read_bytes(), (installed / 'npmrc').read_bytes())
            opencode_global = Path(shutil.which('opencode')).resolve().parent.parent / 'etc/npmrc'
            self.assertEqual(opencode_global.read_bytes(), (installed / 'npmrc').read_bytes())
            for manager in ('npm', 'pnpm'):
                registry = subprocess.check_output([manager, 'config', 'get', 'registry'],
                                                   cwd=home, env=environment, text=True).strip()
                self.assertEqual(registry, NPM)


@unittest.skipUnless(os.name == 'posix' and shutil.which('node') and shutil.which('npm') and shutil.which('pnpm'),
                     'Guest Linux package-manager contract')
class DependencyRuntimeTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='jingjiaagent-dependencies-')
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.project = self.directory / 'project'
        self.project.mkdir()
        (self.project / 'package.json').write_text('{"name":"dependency-test","version":"1.0.0"}')
        self.home = self.directory / 'home'
        self.home.mkdir()
        self.environment = clean_environment(self.home)

    def config(self, manager, key, environment=None, arguments=()):
        result = subprocess.run([manager, 'config', 'get', key, *arguments], cwd=self.project,
                                env=self.environment if environment is None else environment,
                                check=True, capture_output=True, text=True, timeout=15)
        return result.stdout.strip()

    def test_unconfigured_package_managers_use_nexus(self):
        self.assertEqual(pip_options(sys.executable, self.environment, self.project)['index'], PYPI)
        for manager in ('npm', 'pnpm'):
            self.assertEqual(self.config(manager, 'registry'), NPM)

    def test_npm_and_pnpm_preserve_user_project_and_scoped_registries(self):
        (self.home / '.npmrc').write_text('registry=https://user.example.invalid/npm/\n')
        for manager in ('npm', 'pnpm'):
            self.assertEqual(self.config(manager, 'registry'), 'https://user.example.invalid/npm/')
        (self.project / '.npmrc').write_text('registry=https://project.example.invalid/npm/\n'
                                           '@example:registry=https://scope.example.invalid/npm/\n')
        for manager in ('npm', 'pnpm'):
            self.assertEqual(self.config(manager, 'registry'), 'https://project.example.invalid/npm/')
            self.assertEqual(self.config(manager, '@example:registry'), 'https://scope.example.invalid/npm/')
            environment = dict(self.environment, NPM_CONFIG_REGISTRY='https://env.example.invalid/npm/')
            self.assertEqual(self.config(manager, 'registry', environment), 'https://env.example.invalid/npm/')
            if manager == 'npm':  # pnpm config get does not accept install's --registry option.
                self.assertEqual(self.config(manager, 'registry', environment,
                                             ['--registry=https://cli.example.invalid/npm/']),
                                 'https://cli.example.invalid/npm/')

    def test_pip_preserves_user_venv_explicit_config_environment_and_cli(self):
        user_config = self.home / '.config/pip/pip.conf'
        user_config.parent.mkdir(parents=True)
        user_config.write_text('[global]\nindex-url=https://user.example.invalid/simple\n')
        self.assertEqual(pip_options(sys.executable, self.environment, self.project)['index'],
                         'https://user.example.invalid/simple')
        venv = self.directory / 'venv'
        subprocess.run(['/usr/bin/python3', '-m', 'venv', str(venv)], check=True,
                       env=self.environment, capture_output=True, timeout=30)
        python = venv / 'bin/python'
        (venv / 'pip.conf').write_text('[global]\nindex-url=https://venv.example.invalid/simple\n'
                                      '[install]\nextra-index-url=https://extra.example.invalid/simple\n')
        options = pip_options(python, self.environment, self.project)
        self.assertEqual(options['index'], 'https://venv.example.invalid/simple')
        self.assertEqual(options['extra'], ['https://extra.example.invalid/simple'])
        explicit = self.project / 'pip.conf'
        explicit.write_text('[global]\nindex-url=https://explicit.example.invalid/simple\n')
        environment = dict(self.environment, PIP_CONFIG_FILE=str(explicit))
        self.assertEqual(pip_options(python, environment, self.project)['index'],
                         'https://explicit.example.invalid/simple')
        environment['PIP_INDEX_URL'] = 'https://env.example.invalid/simple'
        self.assertEqual(pip_options(python, environment, self.project)['index'],
                         'https://env.example.invalid/simple')
        self.assertEqual(pip_options(python, environment, self.project,
                                     ['--index-url=https://cli.example.invalid/simple'])['index'],
                         'https://cli.example.invalid/simple')

    def test_pip_project_root_config_requires_explicit_selection(self):
        project_config = self.project / 'pip.conf'
        project_config.write_text('[global]\nindex-url=https://project.example.invalid/simple\n')
        self.assertEqual(pip_options(sys.executable, self.environment, self.project)['index'], PYPI)
        environment = dict(self.environment, PIP_CONFIG_FILE=str(project_config))
        self.assertEqual(pip_options(sys.executable, environment, self.project)['index'],
                         'https://project.example.invalid/simple')

    @unittest.skipUnless(os.environ.get('JINGJIAAGENT_DEPENDENCY_NETWORK_NONE') == '1',
                         'Run this native OpenCode probe with --network none')
    def test_opencode_bootstrap_respects_native_registry_precedence(self):
        self.assertIsNotNone(shutil.which('opencode'), 'requires the Guest OpenCode binary')
        deadline = time.monotonic() + 25
        for mode in ('default', 'user', 'local', 'environment'):
            with self.subTest(mode=mode):
                root = self.directory / mode
                home, workspace = root / 'home', root / 'workspace'
                home.mkdir(parents=True)
                workspace.mkdir()
                plugin = root / 'fixture.mjs'
                plugin.write_text('export const Fixture=async()=>({});')
                config = root / 'opencode.json'
                config.write_text(json.dumps({'$schema': 'https://opencode.ai/config.json',
                                              'plugin': [str(plugin)]}))
                environment = {key: value for key, value in clean_environment(home).items()
                               if not key.startswith('OPENCODE_')}
                environment.update(XDG_DATA_HOME=str(home / '.local/share'),
                                   XDG_STATE_HOME=str(home / '.local/state'),
                                   XDG_CACHE_HOME=str(home / '.cache'), OPENCODE_CONFIG=str(config),
                                   OPENCODE_DISABLE_DEFAULT_PLUGINS='true', OPENCODE_DISABLE_MODELS_FETCH='1',
                                   OPENCODE_DISABLE_AUTOUPDATE='true', npm_config_offline='true')
                expected = NPM
                if mode in ('user', 'local', 'environment'):
                    (home / '.npmrc').write_text('registry=https://user.example.invalid/npm/\n')
                    expected = 'https://user.example.invalid/npm/'
                if mode in ('local', 'environment'):
                    local = home / '.config/opencode'
                    local.mkdir(parents=True)
                    (local / '.npmrc').write_text('registry=https://local.example.invalid/npm/\n')
                    expected = 'https://local.example.invalid/npm/'
                if mode == 'environment':
                    environment['NPM_CONFIG_REGISTRY'] = 'https://env.example.invalid/npm/'
                    expected = 'https://env.example.invalid/npm/'
                remaining = deadline - time.monotonic()
                self.assertGreater(remaining, 0, 'OpenCode package-source probe exceeded 25 seconds')
                try:
                    result = subprocess.run(['opencode', 'debug', 'config', '--print-logs', '--log-level', 'DEBUG'],
                                            cwd=workspace, env=environment, capture_output=True, text=True,
                                            timeout=min(10, remaining))
                except subprocess.TimeoutExpired:
                    self.fail('OpenCode package-source probe timed out')
                self.assertEqual(result.returncode, 0, 'OpenCode config loading failed (logs withheld)')
                # Offline mode records the selected URL but performs no network request/download.
                requests = re.findall(r'request to ([^ ]+)', result.stderr)
                self.assertEqual(requests, [expected + '@opencode-ai%2fplugin'])

    def test_runtime_entry_preserves_explicit_environment_for_agent_and_tool_child(self):
        keys = ['PIP_INDEX_URL', 'PIP_EXTRA_INDEX_URL', 'PIP_CONFIG_FILE', 'PIP_TARGET',
                'NPM_CONFIG_REGISTRY', 'NPM_CONFIG_USERCONFIG', 'npm_config_prefix']
        expression = 'Object.fromEntries(' + json.dumps(keys) + '.map(key=>[key,process.env[key]]))'
        probe = self.directory / 'probe.cjs'
        probe.write_text('const cp=require("node:child_process");'
                         'const child=cp.spawnSync(process.execPath,["-e",'
                         + json.dumps('process.stdout.write(JSON.stringify(' + expression + '))')
                         + '],{encoding:"utf8"});'
                         'if(child.status!==0)process.exit(1);'
                         'process.stdout.write(JSON.stringify({agent:' + expression + ',tool:JSON.parse(child.stdout)}));')
        entry = self.directory / 'agent-compose-runtime'
        entry.write_text((ROOT / 'guest-entry.sh').read_text()
                         .replace('/opt/agent-compose-runtime/dist/cli.js', str(probe)))
        expected = dict(PIP_INDEX_URL='https://project.example.invalid/simple',
                        PIP_EXTRA_INDEX_URL='https://extra.example.invalid/simple',
                        PIP_CONFIG_FILE=str(self.project / 'pip.conf'), PIP_TARGET=str(self.project / 'python'),
                        NPM_CONFIG_REGISTRY='https://project.example.invalid/npm/',
                        NPM_CONFIG_USERCONFIG=str(self.home / '.npmrc'), npm_config_prefix=str(self.project / 'node'))
        environment = dict(self.environment, **expected)
        result = subprocess.run(['sh', str(entry), 'prompt'], check=True, cwd=self.project,
                                env=environment, capture_output=True, text=True, timeout=20)
        values = json.loads(result.stdout)
        self.assertEqual(values['agent'], expected)
        self.assertEqual(values['tool'], expected)


if __name__ == '__main__':
    unittest.main()
