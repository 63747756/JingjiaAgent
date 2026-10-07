"""Offline deployment contracts. No Docker, live services or credentials used.

Run: python3 -m unittest discover -s runtime/jingjiaagent -p 'test_linux_web_security.py' -v
Requires the generator's cryptography dependency and PyYAML for Compose parsing.
"""
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

import yaml

import prepare_linux_web
from build_metadata import labels
from linux_web_security import (SECURITY_VERSION, check_existing_networks,
                                redis_password, require_security_config,
                                validate_existing_networks, validate_runtime_image)


ROOT = Path(__file__).resolve().parent
LOCK = {'repository': 'https://github.com/chaitin/agent-compose', 'commit': 'c'*40, 'patch_revision': 17, 'guest_patch_revision': 20, 'backend_patch_revision': 1, 'frontend_patch_revision': 1}
FAKE_PASSWORD = 'fixture-only-not-a-real-secret-0123456789'


def image(revision, commit=LOCK['commit'], component=None):
    component = component or ('daemon' if revision == 17 else 'guest')
    data = labels(component, LOCK, ('a'*40, 'b'*64))
    data['jingjiaagent.image.revision'] = str(revision)
    data['org.opencontainers.image.version'] = 'p'+str(revision)
    data['jingjiaagent.upstream.commit'] = commit
    return {'Id': 'sha256:'+'0'*64, 'Config': {'Labels': data}}


class ImageRevisionTests(unittest.TestCase):
    def test_split_daemon_and_guest_revisions(self):
        validate_runtime_image('daemon', image(17), LOCK)
        validate_runtime_image('guest', image(20), LOCK)

    def test_swapped_and_missing_revisions_fail(self):
        for name, item in [('daemon', image(20)), ('guest', image(17)),
                           ('guest', {'Config': {'Labels': None}}), ('daemon', {})]:
            with self.subTest(name=name, item=item), self.assertRaises(SystemExit):
                validate_runtime_image(name, item, LOCK)

    def test_missing_guest_revision_is_rejected(self):
        with self.assertRaises(SystemExit):
            validate_runtime_image('guest', image(17), {k: v for k, v in LOCK.items() if k != 'guest_patch_revision'})

    def test_wrong_upstream_fails(self):
        with self.assertRaises(SystemExit):
            validate_runtime_image('guest', image(20, 'wrong-commit'), LOCK)

    def test_source_identity_and_provenance_are_required(self):
        for label, value in [('org.opencontainers.image.source', 'https://other.example'),
                             ('jingjiaagent.upstream.source', 'https://other.example'),
                             ('org.opencontainers.image.revision', LOCK['commit'][:7]),
                             ('jingjiaagent.source.tree.sha256', 'unrecorded')]:
            with self.subTest(label=label), self.assertRaises(SystemExit):
                candidate = image(20)
                candidate['Config']['Labels'][label] = value
                validate_runtime_image('guest', candidate, LOCK)

    def test_current_lock_matches_dockerfile_and_rejects_previous_guest(self):
        lock = json.loads((ROOT / 'source.lock.json').read_text())
        self.assertIn('ARG JINGJIAAGENT_RUNTIME_PATCH=' + str(lock['guest_patch_revision']),
                      (ROOT / 'Dockerfile.guest').read_text())
        self.assertIn('ARG JINGJIAAGENT_RUNTIME_PATCH=' + str(lock['patch_revision']),
                      (ROOT / 'Dockerfile.daemon').read_text())
        validate_runtime_image('guest', image(lock['guest_patch_revision'], lock['commit']), lock)
        with self.assertRaises(SystemExit):
            validate_runtime_image('guest', image(20, lock['commit']), lock)


class ComposeNetworkTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.compose = yaml.safe_load((ROOT / 'compose.web.yaml').read_text())
        cls.services = cls.compose['services']

    def networks(self, service):
        definition = self.services[service]
        if definition.get('network_mode', '').startswith('service:'):
            return self.networks(definition['network_mode'].split(':', 1)[1])
        return set(definition['networks'])

    def test_daemon_has_exactly_one_custom_network(self):
        # Upstream sorts names and selects the first custom network. Providing
        # exactly one makes selection deterministic for every project name.
        self.assertEqual(self.networks('runtime'), {'sandbox'})
        self.assertNotIn('network_mode', self.services['runtime'])
        self.assertEqual(self.compose['networks']['sandbox']['driver'], 'bridge')
        self.assertFalse(self.compose['networks']['sandbox'].get('internal', False))

    def test_guest_has_no_direct_path_to_any_business_store(self):
        for store in ('redis', 'postgres', 'storage', 'clickhouse'):
            with self.subTest(store=store):
                self.assertEqual(self.networks(store), {'business'})
                self.assertFalse(self.networks(store) & self.networks('runtime'))
        self.assertTrue(self.compose['networks']['business']['internal'])
        self.assertNotIn('default', self.compose['networks'])
        self.assertNotIn('ports', self.services['redis'])
        self.assertNotIn('ports', self.services['clickhouse'])
        self.assertNotIn('ports', self.services['postgres'])
        self.assertNotIn('ports', self.services['storage'])
        self.assertEqual(self.services['storage']['healthcheck']['test'],
                         ['CMD','curl','-fsS','http://127.0.0.1:9000/minio/health/ready'])
        self.assertEqual(self.services['backend']['depends_on']['storage']['condition'], 'service_healthy')

    def test_required_application_paths_remain_connected(self):
        for left, right in [('backend', 'redis'), ('backend', 'postgres'), ('backend', 'storage'),
                            ('backend', 'clickhouse'), ('backend', 'runtime-proxy'),
                            ('runtime-proxy', 'runtime'), ('web', 'runtime'), ('web', 'storage')]:
            with self.subTest(path=(left, right)):
                self.assertTrue(self.networks(left) & self.networks(right))
        self.assertEqual(self.networks('backend'), {'sandbox', 'business'})
        self.assertNotIn('aliases:', (ROOT / 'compose.web.yaml').read_text())
        self.assertEqual(self.services['runtime']['environment']['AGENT_COMPOSE_RUNTIME_BASE_URL'], 'http://runtime:7410')
        self.assertNotIn('redis_password', self.services['runtime']['secrets'])

    def test_redis_auth_and_healthcheck_use_private_file(self):
        redis = self.services['redis']
        self.assertIn('redis_password', redis['secrets'])
        self.assertIn('--requirepass "$$(cat /run/secrets/redis_password)"', redis['command'][0])
        self.assertIn('docker-entrypoint.sh redis-server', redis['command'][0])
        check = redis['healthcheck']['test'][1]
        self.assertIn('REDISCLI_AUTH=', check)
        self.assertIn('grep -qx PONG', check)
        self.assertTrue(self.compose['secrets']['redis_password']['file'].endswith('/redis.password'))

    def test_ad_key_is_backend_only_and_fixture_is_not_in_production(self):
        backend_secret = {'source': 'ad_secret_key', 'target': 'ad-secret-key-source'}
        self.assertIn(backend_secret, self.services['backend']['secrets'])
        self.assertEqual(self.services['backend']['environment']['JINGJIAAGENT_AD_SECRET_KEY_FILE'],
                         '/run/secrets/ad-secret-key')
        self.assertTrue(self.compose['secrets']['ad_secret_key']['file'].endswith('/ad-secret.key'))
        for name, service in self.services.items():
            if name == 'backend':
                continue
            self.assertNotIn('ad_secret_key', str(service.get('secrets', [])))
            self.assertNotIn('JINGJIAAGENT_AD_', str(service.get('environment', {})))
        self.assertNotIn('ad-fixture', self.services)
        fixture = yaml.safe_load((ROOT / 'tests/compose.ad-fixture.yaml').read_text())
        self.assertEqual(set(fixture['services']['ad-fixture']['networks']), {'ad-test'})
        self.assertNotIn('ports', fixture['services']['ad-fixture'])
        self.assertTrue(fixture['networks']['ad-test']['internal'])

    def test_healthcheck_rejects_noauth_even_when_cli_exits_zero(self):
        check = self.services['redis']['healthcheck']['test'][1].replace('$$', '$')
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            secret = directory / 'password'
            secret.write_text(FAKE_PASSWORD)
            check = check.replace('/run/secrets/redis_password', str(secret))
            cli = directory / 'redis-cli'
            cli.write_text('#!/bin/sh\nprintf "%s\\n" "$FAKE_REDIS_REPLY"\n')
            cli.chmod(0o700)
            for reply, expected in [('PONG', 0), ('NOAUTH Authentication required.', 1),
                                    ('WRONGPASS invalid username-password pair', 1)]:
                with self.subTest(reply=reply):
                    result = subprocess.run(['sh', '-ec', check], env=dict(os.environ,
                        PATH=str(directory) + os.pathsep + os.environ['PATH'], FAKE_REDIS_REPLY=reply),
                        capture_output=True)
                    self.assertEqual(result.returncode, expected)


class MigrationTests(unittest.TestCase):
    def test_new_project_is_allowed(self):
        validate_existing_networks('fixture', [], [])

    def test_current_topology_is_allowed(self):
        validate_existing_networks('fixture', [self.container('runtime', ['sandbox']),
            self.container('backend', ['sandbox', 'business']), self.container('redis', ['business'])], [])

    @staticmethod
    def container(service, networks):
        return {'Config': {'Labels': {'com.docker.compose.service': service}},
                'NetworkSettings': {'Networks': {'fixture_' + name: {} for name in networks}}}

    def test_legacy_or_multihomed_daemon_is_never_silently_recreated(self):
        for service, networks in [('runtime', ['default']), ('runtime', ['business', 'sandbox']),
                                  ('redis', ['sandbox']), ('backend', ['default'])]:
            with self.subTest(service=service, networks=networks), self.assertRaises(SystemExit):
                validate_existing_networks('fixture', [self.container(service, networks)], [])

    def test_orphan_guest_on_legacy_network_blocks_start(self):
        with self.assertRaisesRegex(SystemExit, 'possibly existing Guests'):
            validate_existing_networks('fixture', [], [{'Name': 'fixture_default', 'Containers': {'guest': {}}}])

    def test_stopped_guest_on_legacy_network_blocks_start_even_without_active_endpoints(self):
        guest={'Config':{'Labels':{'agent-compose.sandbox_id':'private-fixture'}},
               'State':{'Running':False},'NetworkSettings':{'Networks':{'fixture_default':{}}}}
        with self.assertRaisesRegex(SystemExit,'including stopped Guests'):
            validate_existing_networks('fixture',[guest],[{'Name':'fixture_default','Containers':{}}])
        guest['NetworkSettings']['Networks']={'fixture_sandbox':{}}
        validate_existing_networks('fixture',[guest],[{'Name':'fixture_sandbox','Containers':{}}])
        guest['NetworkSettings']['Networks']={'another_project_default':{}}
        validate_existing_networks('fixture',[guest],[{'Name':'fixture_sandbox','Containers':{}}])

    def test_read_only_preflight_does_not_change_docker_state(self):
        calls = []
        def fake(command, **kwargs):
            calls.append(command)
            return ''
        check_existing_networks('fixture', fake)
        self.assertEqual(calls, [['docker', 'ps', '-aq', '--filter', 'label=com.docker.compose.project=fixture'],
            ['docker', 'network', 'ls', '-q', '--filter', 'label=com.docker.compose.project=fixture']])

    def test_missing_invalid_and_injectable_passwords_fail(self):
        for password in (None, '', 'short', 'a' * 40 + '\n', 'a' * 40 + '$', ['not-a-string']):
            with self.subTest(password=password), self.assertRaises(SystemExit):
                redis_password({'redis_password': password})


class PrepareContractTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.state = self.root / '.state/linux-web'
        self.state.mkdir(parents=True)
        self.keys = {'postgres_password': 'test-postgres', 'storage_user': 'test-storage',
                     'storage_password': 'test-storage-password', 'redis_password': FAKE_PASSWORD,
                     'mcp_token': 'test-mcp', 'clickhouse_password': 'test-clickhouse'}
        for name, content in {
            'credentials.json': self.keys, 'web-account.json': {'email': 'fixture@example.invalid', 'password': 'fake'},
            'web-node.json': {'id': '2dcc2e22-6cce-4a63-8dfa-07b1d8681f58'},
        }.items():
            (self.state / name).write_text(json.dumps(content))
        (self.root / '.state/model.json').write_text(json.dumps({'model': 'fake-model', 'api_key': 'fake'}))
        (self.state / 'runtime.crt').write_text('fake-test-certificate')
        (self.state / 'daemon.token').write_text('fake-test-token')
        (self.state / 'payload.key').write_bytes(b'x' * 32)
        (self.state / 'ad-secret.key').write_bytes(b'a' * 32)
        (self.root / 'source.lock.json').write_text(json.dumps(LOCK))
        self.environment = mock.patch.dict(os.environ, {}, clear=True)
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def prepare(self):
        def inspect(command):
            self.assertEqual(command[:3], ['docker', 'image', 'inspect'])
            for component in ('daemon','guest','backend','frontend'):
                if command[3].startswith('jingjiaagent-'+component+':'):
                    return json.dumps([image(20 if component == 'guest' else 17 if component == 'daemon' else 1, component=component)])
            return json.dumps([image(1)])
        with mock.patch.object(prepare_linux_web.subprocess, 'check_output', side_effect=inspect), \
             mock.patch.object(prepare_linux_web.secrets, 'token_urlsafe', side_effect=AssertionError('No real secrets')), \
             mock.patch.object(prepare_linux_web.secrets, 'token_bytes', side_effect=AssertionError('No real secrets')), \
             mock.patch.object(prepare_linux_web.secrets, 'token_hex', side_effect=AssertionError('No real secrets')), \
             contextlib.redirect_stdout(io.StringIO()):
            prepare_linux_web.main(self.root)

    def test_generator_accepts_split_images_and_shares_redis_password(self):
        self.prepare()
        cfg = json.loads((self.state / 'config/server/config.yaml').read_text())
        self.assertEqual(cfg['redis'], {'host': 'redis', 'port': 6379, 'pass': FAKE_PASSWORD})
        self.assertEqual((self.state / 'redis.password').read_bytes(), (FAKE_PASSWORD + '\n').encode('utf-8'))
        if os.name == 'posix':
            self.assertEqual((self.state / 'redis.password').stat().st_mode & 0o777, 0o600)
        self.assertNotIn(FAKE_PASSWORD, (self.state / 'compose.env').read_text())
        require_security_config(self.state)
        self.assertEqual((self.state / 'daemon.token').read_text(), 'fake-test-token')
        self.assertEqual((self.state / 'payload.key').read_bytes(), b'x' * 32)
        self.assertEqual((self.state / 'ad-secret.key').read_bytes(), b'a' * 32)
        self.assertEqual(cfg['ad']['secret_key_file'], '/run/secrets/ad-secret-key')
        self.prepare()  # A repeated prepare retains the same identities/password.
        self.assertEqual(json.loads((self.state / 'credentials.json').read_text()), self.keys)
        self.assertEqual((self.state / 'redis.password').read_bytes(), (FAKE_PASSWORD + '\n').encode('utf-8'))

    def test_ad_key_is_generated_once_and_never_replaced(self):
        from linux_web_security import ensure_ad_secret_key
        key_path = self.state / 'ad-secret.key'
        key_path.unlink()
        with mock.patch('linux_web_security.secrets.token_bytes', return_value=b'n' * 32):
            ensure_ad_secret_key(self.state)
        self.assertEqual(key_path.read_bytes(), b'n' * 32)
        if os.name == 'posix':
            self.assertEqual(key_path.stat().st_mode & 0o777, 0o600)
        with mock.patch('linux_web_security.secrets.token_bytes', side_effect=AssertionError('No rotation')):
            ensure_ad_secret_key(self.state)
        self.assertEqual(key_path.read_bytes(), b'n' * 32)
        key_path.write_bytes(b'invalid')
        with self.assertRaisesRegex(SystemExit, 'restore the original key'):
            ensure_ad_secret_key(self.state)
        self.assertEqual(key_path.read_bytes(), b'invalid')

    def test_start_rejects_missing_ad_key_and_inconsistent_mount(self):
        self.prepare()
        (self.state / 'ad-secret.key').unlink()
        with self.assertRaisesRegex(SystemExit, 'AD secret configuration'):
            require_security_config(self.state)
        (self.state / 'ad-secret.key').write_bytes(b'a' * 32)
        path = self.state / 'config/server/config.yaml'
        cfg = json.loads(path.read_text())
        cfg['ad']['secret_key_file'] = '/tmp/untrusted-key'
        path.write_text(json.dumps(cfg))
        with self.assertRaisesRegex(SystemExit, 'AD secret configuration differs'):
            require_security_config(self.state)

    def test_legacy_prepare_stops_before_rewriting_private_state(self):
        del self.keys['redis_password']
        (self.state / 'credentials.json').write_text(json.dumps(self.keys))
        before = {p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        with self.assertRaisesRegex(SystemExit, 'not rotated automatically'):
            self.prepare()
        self.assertEqual(before, {p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()})

    def test_stale_installer_bundle_is_rejected_before_backend_configuration(self):
        node = json.loads((self.state / 'web-node.json').read_text())
        node.update(owner_id='a'*8+'-'+ 'a'*4+'-'+ 'a'*4+'-'+ 'a'*4+'-'+ 'a'*12,
                    team_id='b'*8+'-'+ 'b'*4+'-'+ 'b'*4+'-'+ 'b'*4+'-'+ 'b'*12)
        (self.state / 'web-node.json').write_text(json.dumps(node))
        bundle = self.state / 'config/server/installation-bundle/manifest.json'
        bundle.parent.mkdir(parents=True)
        valid = {'product': 'jingjiaagent', 'daemon_image': 'sha256:'+'0'*64,
                 'guest_image': 'sha256:'+'0'*64, 'daemon_revision': 17,
                 'guest_revision': 20, 'upstream_commit': LOCK['commit']}
        for field, wrong in [('guest_revision', 17), ('daemon_revision', 20),
                             ('guest_image', 'sha256:'+'1'*64), ('product', 'other')]:
            with self.subTest(field=field):
                bundle.write_text(json.dumps(dict(valid, **{field: wrong})))
                with mock.patch.dict(os.environ, {'JINGJIAAGENT_RUNTIME_WEB_ENABLE_INSTALLER': '1'}), \
                     self.assertRaisesRegex(SystemExit, 'bundle differs from the selected images'):
                    self.prepare()
                self.assertFalse((self.state / 'config/server/config.yaml').exists())

    def test_guest_callbacks_and_preview_remain_compatible(self):
        self.prepare()
        cfg = json.loads((self.state / 'config/server/config.yaml').read_text())
        self.assertEqual(cfg['runtime']['mcp_url'], 'http://backend:47424/mcp')
        self.assertEqual(cfg['llm_proxy']['base_url'], 'http://backend:47424')
        self.assertEqual(cfg['object_storage']['agent_access_endpoint'], 'http://backend:47596')
        self.assertEqual(cfg['runtime']['nodes'][0]['url'], 'https://runtime-proxy:7411')
        nginx = (self.state / 'nginx.conf').read_text()
        self.assertIn('listen 47425;', nginx)
        self.assertIn('proxy_pass http://127.0.0.1:8889;', nginx)
        self.assertIn('proxy_set_header Upgrade $http_upgrade;', nginx)

    def test_explicit_external_guest_routes_are_retained(self):
        with mock.patch.dict(os.environ, {
                'JINGJIAAGENT_RUNTIME_WEB_GUEST_BASE_URL': 'https://application.example.test/',
                'JINGJIAAGENT_RUNTIME_WEB_GUEST_STORAGE_URL': 'https://assets.example.test/'}):
            self.prepare()
        cfg = json.loads((self.state / 'config/server/config.yaml').read_text())
        self.assertEqual(cfg['runtime']['mcp_url'], 'https://application.example.test/mcp')
        self.assertEqual(cfg['llm_proxy']['base_url'], 'https://application.example.test')
        self.assertEqual(cfg['object_storage']['agent_access_endpoint'], 'https://assets.example.test')
        self.assertEqual(cfg['object_storage']['access_endpoint'], 'http://127.0.0.1:47424/oss')

    def test_object_gateway_is_signed_read_only_and_preserves_signature_inputs(self):
        self.prepare()
        nginx = (self.state / 'nginx.conf').read_text()
        gateway = nginx[nginx.index('listen 47596;'):]
        self.assertIn('if ($request_method !~ ^(GET|HEAD)$) { return 405; }', gateway)
        self.assertIn('if ($agent_storage_signed = 0) { return 403; }', gateway)
        self.assertIn('default 0;', nginx)
        self.assertIn('X-Amz-Signature=', nginx)
        self.assertIn('proxy_pass http://storage:9000;', gateway)
        self.assertIn('proxy_set_header Host $http_host;', gateway)
        self.assertIn('proxy_set_header Authorization "";', gateway)
        self.assertIn('access_log off;', gateway)
        self.assertNotIn('rewrite ', gateway)
        self.assertNotIn('9001', gateway)

    def test_browser_gateway_preserves_signed_host_and_removes_only_unsigned_prefix(self):
        self.prepare()
        nginx=(self.state/'nginx.conf').read_text()
        browser=nginx[nginx.index('location ^~ /oss/ {'):nginx.index('location ~ ^/(api/')]
        self.assertIn('^(GET|HEAD|PUT)$',browser)
        self.assertIn('if ($browser_storage_allowed = 0) { return 403; }',browser)
        self.assertIn('~^/oss/jingjiaagent/(avatar|spec|repo)/ 1;',nginx)
        self.assertIn('~^(GET|HEAD):1:[01]$ 1;',nginx)
        self.assertIn('~^(GET|HEAD|PUT):[01]:1$ 1;',nginx)
        self.assertIn('proxy_pass http://storage:9000/;',browser)
        self.assertIn('proxy_set_header Host $http_host;',browser)
        self.assertIn('proxy_set_header Cookie "";',browser)
        self.assertIn('proxy_set_header Authorization "";',browser)
        self.assertNotIn('9001',nginx)

    def test_start_rejects_crlf_password_that_text_mode_would_hide(self):
        self.prepare()
        (self.state/'redis.password').write_bytes((FAKE_PASSWORD+'\r\n').encode('utf-8'))
        with self.assertRaises(SystemExit):require_security_config(self.state)

    def test_start_rejects_missing_marker_or_mismatched_redis_secret(self):
        with self.assertRaises(SystemExit):
            require_security_config(self.state)
        self.prepare()
        (self.state / 'redis.password').write_text('different-test-secret')
        with self.assertRaises(SystemExit):
            require_security_config(self.state)
        (self.state / 'redis.password').write_text(FAKE_PASSWORD)
        (self.state / 'network-security.json').write_text(json.dumps({'version': SECURITY_VERSION + 1}))
        with self.assertRaises(SystemExit):
            require_security_config(self.state)


if __name__ == '__main__':
    unittest.main()
