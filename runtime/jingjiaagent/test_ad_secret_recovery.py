"""Isolated key-loss contracts. No live deployment, volume or credentials used."""
import base64
import contextlib
import io
import json
import os
from pathlib import Path
import runpy
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

import build_metadata
import prepare_linux_web
from linux_web_security import ensure_ad_secret_key, prepare_ad_secret_key, require_empty_deployment

ROOT = Path(__file__).resolve().parent


class ADSecretRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='jingjiaagent-ad-key-')
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.state = self.root / '.state/linux-web'

    def snapshot(self):
        return {path.relative_to(self.root): path.read_bytes()
                for path in self.root.rglob('*') if path.is_file()}

    def test_fresh_empty_data_generates_once_and_repeated_prepare_retains_key(self):
        with mock.patch('linux_web_security.subprocess.check_output', return_value='') as inventory, \
             mock.patch('linux_web_security.secrets.token_bytes', return_value=b'n' * 32) as random:
            path = prepare_ad_secret_key(self.state, 'fixture')
            self.assertEqual(path.read_bytes(), b'n' * 32)
            (self.state / 'credentials.json').write_text('existing-state')
            prepare_ad_secret_key(self.state, 'fixture')
        self.assertEqual(inventory.call_count, 2)  # One container and one volume inventory.
        random.assert_called_once_with(32)
        if os.name == 'posix':
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_validate_only_never_initializes_missing_key(self):
        with self.assertRaisesRegex(SystemExit, 'restore the original'):
            ensure_ad_secret_key(self.state)
        self.assertFalse(self.state.exists())

    @unittest.skipUnless(os.name == 'posix', 'Linux first-install generator contract')
    def test_linux_first_prepare_and_repeat_preserve_key_and_full_configuration(self):
        lock = build_metadata.load_lock()
        (self.root / 'source.lock.json').write_text(json.dumps(lock))
        (self.root / '.state').mkdir()
        (self.root / '.state/model.json').write_text(json.dumps({'model': 'fixture', 'api_key': 'fixture-only'}))
        inventories = []
        def docker(args, **kwargs):
            if args[:3] != ['docker', 'image', 'inspect']:
                inventories.append(args)
                return ''
            data = {}
            for component in ('daemon', 'guest', 'backend', 'frontend'):
                if args[3] == build_metadata.image_tag(component, lock):
                    data = build_metadata.labels(component, lock, ('a'*40, 'b'*64))
            return json.dumps([{'Id': 'sha256:'+'0'*64, 'Config': {'Labels': data}}])
        with mock.patch.dict(os.environ, {'JINGJIAAGENT_RUNTIME_WEB_PROJECT': 'fixture'}, clear=True), \
             mock.patch('linux_web_security.subprocess.check_output', side_effect=docker), \
             mock.patch('linux_web_security.secrets.token_bytes', return_value=b'n'*32), \
             mock.patch('linux_web_security.secrets.token_urlsafe', return_value='f'*48), \
             mock.patch('linux_web_security.secrets.token_hex', return_value='a'*16), \
             contextlib.redirect_stdout(io.StringIO()):
            prepare_linux_web.main(self.root)
            before = self.snapshot()
            prepare_linux_web.main(self.root)
        self.assertEqual(before, self.snapshot())
        self.assertEqual((self.state / 'ad-secret.key').read_bytes(), b'n'*32)
        self.assertEqual(len(inventories), 2)

    def test_existing_config_missing_key_fails_before_any_write(self):
        self.state.mkdir(parents=True)
        (self.state / 'config.yaml').write_bytes(b'existing-config')
        before = self.snapshot()
        with mock.patch('linux_web_security.subprocess.check_output') as inventory, \
             self.assertRaisesRegex(SystemExit, 'restore the original key'):
            prepare_ad_secret_key(self.state, 'fixture')
        inventory.assert_not_called()
        self.assertEqual(before, self.snapshot())

    def test_existing_stopped_container_or_restored_volume_without_state_fails(self):
        for containers, volumes in [('arbitrary-name|fixture\n', ''),
                                    ('fixture-backend-1|\n', ''),
                                    ('fixture_postgres_1|\n', ''),
                                    ('fixture-runtime-web-redis|\n', ''),
                                    ('', 'arbitrary-name|fixture\n'),
                                    ('', 'fixture_postgres-data|\n')]:
            with self.subTest(containers=containers, volumes=volumes), \
                 mock.patch('linux_web_security.subprocess.check_output', side_effect=[containers, volumes]), \
                 self.assertRaisesRegex(SystemExit, 'Existing deployment'):
                prepare_ad_secret_key(self.state, 'fixture')
            self.assertFalse(self.state.exists())

    def test_unrelated_projects_and_build_caches_do_not_prevent_fresh_install(self):
        with mock.patch('linux_web_security.subprocess.check_output', side_effect=[
                'fixture-ad-acceptance-backend-1|fixture-ad-acceptance\n'
                'fixture-web-dev-web-1|fixture-web-dev\n',
                'fixture-go-build-cache|\nfixture-go-modules|\n'
                'fixture-ad-acceptance_postgres-data|fixture-ad-acceptance\n'
                'fixture_postgres-data|another-project\n']):
            require_empty_deployment('fixture')

    def test_unavailable_or_invalid_docker_inventory_fails_without_state(self):
        for outcome in (OSError('Docker unavailable'), subprocess.CalledProcessError(1, ['docker']),
                        ['malformed', ''], ['', 'malformed']):
            with self.subTest(outcome=outcome), \
                 mock.patch('linux_web_security.subprocess.check_output', side_effect=outcome), \
                 self.assertRaisesRegex(SystemExit, 'Cannot verify empty'):
                prepare_ad_secret_key(self.state, 'fixture')
            self.assertFalse(self.state.exists())

    def test_restore_original_key_decrypts_existing_cipher_without_rewriting_data(self):
        self.state.mkdir(parents=True)
        original = b'k' * 32
        nonce = b'n' * 12
        ciphertext = b'v1:' + base64.urlsafe_b64encode(nonce + AESGCM(original).encrypt(
            nonce, b'fixture-query-password', b'jingjiaagent:secretbox:v1')).rstrip(b'=')
        database = self.state / 'database-backup.fixture'
        database.write_bytes(ciphertext)
        with self.assertRaisesRegex(SystemExit, 'restore the original'):
            prepare_ad_secret_key(self.state, 'fixture')
        path = self.state / 'ad-secret.key'
        path.write_bytes(original)
        path.chmod(0o600)
        with mock.patch('linux_web_security.secrets.token_bytes', side_effect=AssertionError('No rotation')):
            prepare_ad_secret_key(self.state, 'fixture')
        encoded = database.read_bytes()[3:]
        sealed = base64.urlsafe_b64decode(encoded + b'=' * (-len(encoded) % 4))
        self.assertEqual(AESGCM(path.read_bytes()).decrypt(sealed[:12], sealed[12:],
            b'jingjiaagent:secretbox:v1'), b'fixture-query-password')
        self.assertEqual(database.read_bytes(), ciphertext)

    def test_invalid_existing_key_is_not_replaced_or_chmodded(self):
        self.state.mkdir(parents=True)
        path = self.state / 'ad-secret.key'
        for contents, mode in [(b'invalid', 0o600), (b'x' * 32, 0o644)]:
            if mode == 0o644 and os.name != 'posix':
                continue
            with self.subTest(mode=mode):
                path.write_bytes(contents)
                path.chmod(mode)
                with self.assertRaisesRegex(SystemExit, 'restore the original'):
                    prepare_ad_secret_key(self.state, 'fixture')
                self.assertEqual(path.read_bytes(), contents)
                if os.name == 'posix':
                    self.assertEqual(path.stat().st_mode & 0o777, mode)

    @unittest.skipUnless(os.name == 'posix', 'POSIX symlink contract')
    def test_symlink_key_is_rejected_without_changing_target(self):
        self.state.mkdir(parents=True)
        original = self.root / 'original.key'
        original.write_bytes(b'x' * 32)
        original.chmod(0o600)
        (self.state / 'ad-secret.key').symlink_to(original)
        with self.assertRaisesRegex(SystemExit, 'restore the original'):
            prepare_ad_secret_key(self.state, 'fixture')
        self.assertEqual(original.read_bytes(), b'x' * 32)

    def test_native_prepare_rejects_missing_key_before_mkdir_or_database_queries(self):
        script = self.root / 'prepare_web.py'
        shutil.copyfile(ROOT / script.name, script)
        before = self.snapshot()
        with mock.patch.object(subprocess, 'check_output') as query, \
             mock.patch.object(subprocess, 'run') as mutate, \
             self.assertRaisesRegex(SystemExit, 'restore the original'):
            runpy.run_path(str(script), run_name='__main__')
        query.assert_not_called()
        mutate.assert_not_called()
        self.assertEqual(before, self.snapshot())
        self.assertFalse((self.root / '.state').exists())

    def test_local_prepare_rejects_missing_key_before_bundle_generation(self):
        script = self.root / 'local_deployment.py'
        shutil.copyfile(ROOT / script.name, script)
        self.state.mkdir(parents=True)
        (self.state / 'credentials.json').write_bytes(b'existing-credentials')
        before = self.snapshot()
        with mock.patch.object(subprocess, 'run') as mutate, \
             mock.patch.object(sys, 'argv', [str(script), 'prepare']), \
             self.assertRaisesRegex(SystemExit, 'restore the original'):
            runpy.run_path(str(script), run_name='__main__')
        mutate.assert_not_called()
        self.assertEqual(before, self.snapshot())


if __name__ == '__main__':
    unittest.main()
