"""Linux entrypoint contract; no Docker, AD or private installation keys used."""
import os
import pathlib
import shlex
import subprocess
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parent


@unittest.skipUnless(os.name == 'posix', 'Linux shell and permissions contract')
class BackendEntrypointTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='jingjiaagent-ad-entry-')
        self.addCleanup(self.temporary.cleanup)
        self.directory = pathlib.Path(self.temporary.name)
        self.source = self.directory / 'source'
        self.target = self.directory / 'target'
        self.source.write_bytes(b'y' * 32)
        self.assertEqual(self.source.stat().st_size, 32)
        self.source.chmod(0o777)  # Docker Desktop's bind-mounted file presentation.
        application = self.directory / 'application'
        application.write_text('#!/bin/sh\nprintf "started\\n"\n')
        application.chmod(0o700)
        script = (ROOT / 'backend-entrypoint.sh').read_text()
        script = script.replace('source=/run/secrets/ad-secret-key-source', 'source=' + shlex.quote(str(self.source)))
        script = script.replace('target=/run/secrets/ad-secret-key', 'target=' + shlex.quote(str(self.target)))
        script = script.replace('/run/secrets/.ad-secret-key.XXXXXX', shlex.quote(str(self.directory / '.key.XXXXXX')))
        script = script.replace('exec /app/main', 'exec ' + shlex.quote(str(application)))
        self.entrypoint = self.directory / 'entrypoint'
        self.entrypoint.write_text(script)

    def execute(self):
        return subprocess.run(['sh', str(self.entrypoint)], capture_output=True, text=True,
                              env=dict(os.environ, JINGJIAAGENT_AD_SECRET_KEY_FILE=str(self.target)))

    def test_mounted_key_is_private_copy_and_repeat_start_preserves_it(self):
        original = self.source.read_bytes()
        result = self.execute()
        self.assertEqual((result.returncode, result.stdout), (0, 'started\n'))
        self.assertEqual(self.target.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.target.read_bytes(), original)
        self.assertEqual(self.source.stat().st_mode & 0o777, 0o777)
        inode = self.target.stat().st_ino
        self.assertEqual(self.execute().returncode, 0)
        self.assertEqual(self.target.stat().st_ino, inode)
        self.source.write_bytes(b'x' * 32)
        result = self.execute()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, '')
        self.assertEqual(self.target.read_bytes(), original)

    def test_invalid_source_never_admits_service(self):
        for size in (0, 31, 33):
            with self.subTest(size=size):
                self.source.write_bytes(b'x' * size)
                result = self.execute()
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, '')
                self.assertFalse(self.target.exists())
        self.source.unlink()
        self.assertNotEqual(self.execute().returncode, 0)

    def test_existing_symlink_or_public_copy_is_rejected_without_replacement(self):
        self.target.symlink_to(self.source)
        self.assertNotEqual(self.execute().returncode, 0)
        self.assertTrue(self.target.is_symlink())
        self.target.unlink()
        self.target.write_bytes(self.source.read_bytes())
        self.target.chmod(0o644)
        self.assertNotEqual(self.execute().returncode, 0)
        self.assertEqual(self.target.stat().st_mode & 0o777, 0o644)

    def test_parallel_starts_do_not_create_divergent_keys(self):
        environment = dict(os.environ, JINGJIAAGENT_AD_SECRET_KEY_FILE=str(self.target))
        processes = [subprocess.Popen(['sh', str(self.entrypoint)], stdout=subprocess.PIPE,
                                      stderr=subprocess.PIPE, env=environment) for _ in range(4)]
        for process in processes:
            stdout, stderr = process.communicate(timeout=5)
            self.assertEqual(process.returncode, 0, stderr.decode())
            self.assertEqual(stdout, b'started\n')
        self.assertEqual(self.target.read_bytes(), self.source.read_bytes())
        self.assertEqual(self.target.stat().st_mode & 0o777, 0o600)
        self.assertEqual(list(self.directory.glob('.key.*')), [])


if __name__ == '__main__':
    unittest.main()
