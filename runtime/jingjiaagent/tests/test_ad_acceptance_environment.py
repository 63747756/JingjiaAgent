"""Verify that dedicated acceptance preparation cannot rewrite production state."""
import json
import os
import pathlib
import tempfile
import unittest
from unittest import mock

import ad_acceptance_environment as acceptance
import prepare_linux_web


class AcceptancePreparationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='jingjiaagent-ad-env-')
        self.addCleanup(self.temporary.cleanup)
        self.root = pathlib.Path(self.temporary.name)
        self.state = self.root / '.state/ad-acceptance'
        production = self.root / '.state/linux-web'
        production.mkdir(parents=True)
        self.model = production / 'model.json'
        self.model.write_text(json.dumps({'model': 'fixture', 'base_url': 'https://fixture.invalid/v1', 'api_key': 'fixture-only'}))
        (production / 'original').write_bytes(b'preserve-production')
        self.original = {path.relative_to(self.root): path.read_bytes() for path in production.iterdir()}
        self.compose = (acceptance.ROOT / 'compose.web.yaml').read_text(encoding='utf-8')
        (self.root / 'compose.web.yaml').write_text(self.compose, encoding='utf-8')
        self.root_patch = mock.patch.object(acceptance, 'ROOT', self.root)
        self.state_patch = mock.patch.object(acceptance, 'STATE', self.state)
        self.root_patch.start()
        self.state_patch.start()
        self.addCleanup(self.root_patch.stop)
        self.addCleanup(self.state_patch.stop)
        # No real Docker inventory or existing private state is touched in tests.
        self.storage_patch = mock.patch('linux_web_security.require_empty_deployment')
        self.storage = self.storage_patch.start()
        self.addCleanup(self.storage_patch.stop)

    def test_private_fixture_retains_existing_material_and_production_configuration(self):
        acceptance.prepare_private_fixture(self.model)
        before = {path: path.read_bytes() for path in (self.state / 'fixture').iterdir()}
        acceptance.prepare_private_fixture(self.model)
        self.assertEqual(before, {path: path.read_bytes() for path in (self.state / 'fixture').iterdir()})
        for name, contents in self.original.items():
            self.assertEqual((self.root / name).read_bytes(), contents)
        self.assertEqual(json.loads((self.state / 'model.json').read_text())['api_key'], 'fixture-only')

    def test_stack_override_uses_separate_ports_project_and_private_paths(self):
        captured = []
        def generator(root):
            self.assertEqual(root, self.root)
            captured.append(dict(os.environ))
            acceptance.private(self.state / 'compose.env', 'JINGJIAAGENT_WEB_STATE_DIRECTORY=.state/ad-acceptance\n')
            acceptance.private(self.state / 'nginx.conf', 'server { listen 47424; }\nserver { listen 47425; }\n')
            acceptance.private(self.state / 'config/server/config.yaml', {
                'server': {'base_url': 'http://127.0.0.1:47424'},
                'object_storage': {'access_endpoint': 'http://127.0.0.1:47424/oss'},
                'runtime': {'preview': {'base_url': 'http://localhost:47425'}}})
        environment_before = dict(os.environ)
        with mock.patch.object(prepare_linux_web, 'main', side_effect=generator), \
             mock.patch.object(acceptance.subprocess, 'check_output', return_value=json.dumps([{'Id': 'sha256:'+'a'*64}]).encode()), \
             mock.patch.object(acceptance, 'require_security_config'):
            acceptance.prepare_stack(self.model)
        self.assertEqual(dict(os.environ), environment_before)
        self.assertEqual(captured[0]['JINGJIAAGENT_RUNTIME_WEB_PROJECT'], 'jingjiaagent-ad-acceptance')
        self.assertEqual(captured[0]['JINGJIAAGENT_RUNTIME_WEB_STATE_DIRECTORY'], str(self.state))
        result = (self.state / 'compose.web.yaml').read_text(encoding='utf-8')
        for endpoint in ('47426:47424', '47427:47425', '47428:7410', '47429:7411'):
            self.assertIn(endpoint, result)
        self.assertEqual((self.root / 'compose.web.yaml').read_text(encoding='utf-8'), self.compose)
        config = json.loads((self.state / 'config/server/config.yaml').read_text())
        self.assertEqual(config['server']['base_url'], 'http://127.0.0.1:47426')
        self.assertEqual(config['object_storage']['access_endpoint'], 'http://127.0.0.1:47426/oss')
        self.assertEqual(config['runtime']['preview']['base_url'], 'http://localhost:47427')
        self.assertIn('listen 47424; listen 47426;', (self.state / 'nginx.conf').read_text())
        for name, contents in self.original.items():
            self.assertEqual((self.root / name).read_bytes(), contents)

    def test_model_outside_ignored_state_is_rejected(self):
        outside = self.root / 'model.json'
        outside.write_text(self.model.read_text())
        with self.assertRaisesRegex(SystemExit, 'ignored local model'):
            acceptance.prepare_private_fixture(outside)

    def test_missing_key_stops_before_fixture_model_or_endpoint_writes(self):
        acceptance.prepare_private_fixture(self.model)
        (self.state / 'credentials.json').write_bytes(b'existing-deployment')
        (self.state / 'ad-secret.key').unlink()
        before = {path: path.read_bytes() for path in self.root.rglob('*') if path.is_file()}
        with mock.patch.object(prepare_linux_web, 'main') as generator, \
             self.assertRaisesRegex(SystemExit, 'restore the original'):
            acceptance.prepare_stack(self.model)
        generator.assert_not_called()
        self.assertEqual(before, {path: path.read_bytes() for path in self.root.rglob('*') if path.is_file()})

    def test_existing_project_volume_without_state_rejects_fixture_initialization(self):
        self.storage.side_effect = SystemExit('Existing deployment containers or data volumes')
        with self.assertRaisesRegex(SystemExit, 'Existing deployment'):
            acceptance.prepare_private_fixture(self.model)
        self.assertFalse(self.state.exists())

    def test_initial_directory_fixture_without_business_data_is_supported(self):
        (self.state / 'fixture').mkdir(parents=True)
        (self.state / 'fixture/directory.json').write_text('{}')
        (self.state / 'fixture/users.json').write_text('{}')
        acceptance.prepare_private_fixture(self.model)
        self.assertEqual(len((self.state / 'ad-secret.key').read_bytes()), 32)
        self.storage.assert_called_once_with(acceptance.PROJECT)


if __name__ == '__main__':
    unittest.main()
