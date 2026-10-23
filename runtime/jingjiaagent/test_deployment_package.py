import tarfile
import tempfile
import unittest
from pathlib import Path
from package_deployment import archive_release
from pinned_proxy import prepare_proxy
from unittest.mock import patch
import subprocess
import json
import runpy
import shutil


class DeploymentArchiveTests(unittest.TestCase):
    def test_storage_source_builder_records_identity_without_existing_build_dir(self):
        source = Path(__file__).resolve().parent
        lock = json.loads((source/'minio-source.lock.json').read_text())
        image = {'Id': 'sha256:'+'b'*64, 'Os': 'linux', 'Architecture': 'amd64', 'Config': {'Labels': {
            'org.opencontainers.image.revision': lock['commit'], 'org.opencontainers.image.licenses': lock['license']}}}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ('build_storage.py', 'minio-source.lock.json'):
                shutil.copyfile(source/name, root/name)
            with patch('subprocess.run') as build, patch('subprocess.check_output', return_value=json.dumps([image]).encode()):
                runpy.run_path(str(root/'build_storage.py'))
            self.assertIn('MINIO_COMMIT='+lock['commit'], build.call_args.args[0])
            record = json.loads((root/'.build/storage-image.json').read_text())
            self.assertEqual(record['image_id'], image['Id'])
            self.assertEqual(record['source'], lock)

    def test_storage_source_builder_rejects_wrong_revision(self):
        source = Path(__file__).resolve().parent
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ('build_storage.py', 'minio-source.lock.json'):
                shutil.copyfile(source/name, root/name)
            with patch('subprocess.run'), patch('subprocess.check_output', return_value=b'[{"Config":{"Labels":{}}}]'):
                with self.assertRaisesRegex(SystemExit, 'identity differs'):
                    runpy.run_path(str(root/'build_storage.py'))
            self.assertFalse((root/'.build/storage-image.json').exists())

    def test_clean_builder_pulls_exact_digest_and_accepts_classic_id(self):
        ref = 'nginx@sha256:' + 'a'*64
        image = {'Id':'sha256:'+'b'*64,'Os':'linux','Architecture':'amd64','RepoDigests':[ref]}
        with patch('pinned_proxy.subprocess.check_output', side_effect=[subprocess.CalledProcessError(1, 'inspect'),json.dumps([image]).encode()]) as inspect, patch('pinned_proxy.subprocess.run') as run:
            self.assertEqual(prepare_proxy({'platform':'linux/amd64','repo_digests':[ref],'image':'sha256:'+'a'*64}), image['Id'])
            run.assert_called_once_with(['docker','pull','--platform','linux/amd64',ref],check=True)
            self.assertEqual(inspect.call_args.args[0][-1],ref)

    def test_cached_wrong_proxy_is_rejected(self):
        ref = 'nginx@sha256:'+'a'*64
        for image in ({'Id':'sha256:'+'b'*64,'Os':'linux','Architecture':'arm64','RepoDigests':[ref]},
                      {'Id':'sha256:'+'b'*64,'Os':'linux','Architecture':'amd64','RepoDigests':['nginx:alpine']}):
            with patch('pinned_proxy.subprocess.check_output',return_value=json.dumps([image]).encode()):
                with self.assertRaises(ValueError): prepare_proxy({'platform':'linux/amd64','repo_digests':[ref]})

    def test_linux_entrypoints_are_executable_even_from_windows(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root/'release'; source.mkdir()
            for name in ('jingjiaagent', 'install.sh', 'install-online.sh', 'manifest.json'):
                (source/name).write_text('fixture')
                (source/name).chmod(0o666)
            archive_release(source, root/'release.tar.gz', 'test')
            with tarfile.open(root/'release.tar.gz') as archive:
                for name in ('jingjiaagent', 'install.sh', 'install-online.sh'):
                    self.assertEqual(archive.getmember('jingjiaagent-test/'+name).mode, 0o755)
                self.assertEqual(archive.getmember('jingjiaagent-test/manifest.json').mode, 0o644)


if __name__ == '__main__': unittest.main()
