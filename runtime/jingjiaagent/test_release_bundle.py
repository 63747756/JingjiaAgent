"""Exercise packaging with an in-memory Docker boundary and temporary output."""
import hashlib
import json
import pathlib
import re
import runpy
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import build_metadata


ROOT = pathlib.Path(__file__).resolve().parent


class ReleaseBundleTests(unittest.TestCase):
    def test_standalone_readme_only_references_delivered_management_scripts(self):
        lock = build_metadata.load_lock()
        images = {}
        for component in ('daemon', 'guest', 'backend', 'frontend'):
            tag = build_metadata.image_tag(component, lock)
            images[tag] = {'Id': 'sha256:' + hashlib.sha256(tag.encode()).hexdigest(),
                'Os': 'linux', 'Architecture': 'amd64', 'RepoDigests': [],
                'Config': {'Labels': build_metadata.labels(component, lock,
                    ('c'*40, 'd'*64) if component in ('daemon', 'guest') else ('a'*40, 'b'*64))}}
        proxy = json.loads((ROOT/'installer-proxy.lock.json').read_text())['image']
        for tag in ('postgres:16-alpine', 'redis:7-alpine', 'minio/minio:latest',
                    'nginx:alpine', 'clickhouse/clickhouse-server:25.8-alpine', proxy):
            images[tag] = {'Id': 'sha256:' + hashlib.sha256(tag.encode()).hexdigest(),
                'Os': 'linux', 'Architecture': 'amd64', 'Config': {}}

        def inspect(args, **_):
            self.assertEqual(args[:3], ['docker', 'image', 'inspect'])
            return json.dumps([images[args[3]]]).encode()

        def docker(args, **_):
            if args[:3] == ['docker', 'image', 'save']:
                pathlib.Path(args[4]).write_bytes(b'fixture image archive')
            else:
                self.assertEqual(args[:3], ['docker', 'image', 'load'])
            return subprocess.CompletedProcess(args, 0)

        with tempfile.TemporaryDirectory(prefix='jingjiaagent-release-test-') as temporary:
            root = pathlib.Path(temporary)
            for path in ROOT.iterdir():
                if path.is_file() and path.suffix in ('.py', '.json', '.yaml', '.md'):
                    shutil.copyfile(path, root/path.name)
            with mock.patch.object(build_metadata, 'ROOT', root), \
                 mock.patch.object(subprocess, 'check_output', side_effect=inspect), \
                 mock.patch.object(subprocess, 'run', side_effect=docker):
                runpy.run_path(str(ROOT/'package_linux_release.py'), run_name='__main__')
            bundle = root/'.state/release-bundle'
            readme = (bundle/'README.md').read_text(encoding='utf-8')
            self.assertEqual(readme, (ROOT/'README.release.md').read_text(encoding='utf-8'))
            self.assertNotIn('local_deployment.py', readme)
            self.assertNotIn('runtime/jingjiaagent/', readme)
            scripts = set(re.findall(r'\b[\w-]+\.py\b', readme))
            self.assertEqual(scripts, {'install_web.py', 'start_linux_web.py'})
            for script in scripts:
                self.assertTrue((bundle/script).is_file(), script)
            self.assertTrue((bundle/'compose.web.yaml').is_file())
            self.assertTrue((bundle/'AD.md').is_file())
            self.assertIn(' ps\n', readme)
            self.assertIn(' logs --tail 100 backend runtime web\n', readme)
            manifest = json.loads((bundle/'manifest.json').read_text())
            self.assertEqual(manifest['component_revisions']['frontend'], lock['frontend_patch_revision'])
            self.assertEqual(manifest['archive_sha256'], hashlib.sha256(b'fixture image archive').hexdigest())
            self.assertEqual(manifest['schema'], 2)
            self.assertEqual(manifest['component_sources']['daemon']['fork_commit'], 'c'*40)
            self.assertEqual(manifest['component_sources']['backend']['fork_commit'], 'a'*40)
            self.assertEqual(set(path.name for path in bundle.iterdir()) &
                             {'ad-secret.key', 'directory.json', 'users.json', 'server.key', 'compose.ad-fixture.yaml'}, set())
            # Verify-only is safe without a model file or installed data. It exercises
            # the real standalone installer against independently pinned components.
            with mock.patch.object(build_metadata, 'ROOT', root), \
                 mock.patch.object(subprocess, 'check_output', side_effect=inspect), \
                 mock.patch.object(subprocess, 'run', side_effect=docker), \
                 mock.patch.object(sys, 'argv', ['install_web.py', '--bundle', str(bundle),
                                               '--model-config', str(root/'not-provided.json'), '--verify-only']), \
                 self.assertRaises(SystemExit) as exit_result:
                runpy.run_path(str(ROOT/'install_web.py'), run_name='__main__')
            self.assertEqual(exit_result.exception.code, 0)
            # Recovering/restoring storage without its private directory must not
            # become a fresh install. Cover volumes whose Compose labels were lost.
            model = root / 'private-model.json'
            model.write_text(json.dumps({'model': 'fixture', 'api_key': 'fixture-only'}))
            def existing_storage(args, **kwargs):
                if args[:3] == ['docker', 'image', 'inspect']:
                    return inspect(args)
                if args[:3] == ['docker', 'volume', 'ls']:
                    return 'jingjiaagent_postgres-data|\n'
                self.assertEqual(args[:3], ['docker', 'ps', '-a'])
                return ''
            before = {path: path.read_bytes() for path in root.rglob('*') if path.is_file()}
            with mock.patch.object(build_metadata, 'ROOT', root), \
                 mock.patch.object(subprocess, 'check_output', side_effect=existing_storage), \
                 mock.patch.object(subprocess, 'run', side_effect=docker), \
                 mock.patch.object(sys, 'argv', ['install_web.py', '--bundle', str(bundle),
                                               '--model-config', str(model)]), \
                 self.assertRaisesRegex(SystemExit, 'Restore the original ad-secret.key'):
                runpy.run_path(str(ROOT/'install_web.py'), run_name='__main__')
            self.assertEqual(before, {path: path.read_bytes() for path in root.rglob('*') if path.is_file()})
            self.assertFalse((root/'.state/linux-web').exists())

            # Fresh installation initializes the original key before any child
            # can write its bundle/config. A later prepare retains that key.
            stages = []
            def fresh_storage(args, **kwargs):
                return inspect(args) if args[:3] == ['docker', 'image', 'inspect'] else ''
            def fresh_run(args, **kwargs):
                if args[:3] == ['docker', 'image', 'load']:
                    return docker(args)
                self.assertEqual(args[0], sys.executable)
                stages.append(pathlib.Path(args[1]).name)
                self.assertEqual((root/'.state/linux-web/ad-secret.key').read_bytes(), b'n' * 32)
                return subprocess.CompletedProcess(args, 0)
            with mock.patch.object(build_metadata, 'ROOT', root), \
                 mock.patch.object(subprocess, 'check_output', side_effect=fresh_storage), \
                 mock.patch.object(subprocess, 'run', side_effect=fresh_run), \
                 mock.patch('linux_web_security.secrets.token_bytes', return_value=b'n'*32), \
                 mock.patch.object(sys, 'argv', ['install_web.py', '--bundle', str(bundle),
                                               '--model-config', str(model)]):
                runpy.run_path(str(ROOT/'install_web.py'), run_name='__main__')
            self.assertEqual(stages[0], 'build_install_bundle.py')
            self.assertEqual(json.loads((root/'.state/model.json').read_text())['model'], 'fixture')
            # Lost key with an installation directory is rejected even if Docker
            # currently has no containers or volumes (restore/recovery case).
            (root/'.state/linux-web/ad-secret.key').unlink()
            before = {path: path.read_bytes() for path in root.rglob('*') if path.is_file()}
            with mock.patch.object(build_metadata, 'ROOT', root), \
                 mock.patch.object(subprocess, 'check_output', side_effect=fresh_storage), \
                 mock.patch.object(subprocess, 'run', side_effect=docker), \
                 mock.patch.object(sys, 'argv', ['install_web.py', '--bundle', str(bundle),
                                               '--model-config', str(model)]), \
                 self.assertRaisesRegex(SystemExit, 'Existing data must be managed separately'):
                runpy.run_path(str(ROOT/'install_web.py'), run_name='__main__')
            self.assertEqual(before, {path: path.read_bytes() for path in root.rglob('*') if path.is_file()})

    def test_manifest_sources_reject_tampering_and_keep_schema_one_contract(self):
        lock = build_metadata.load_lock()
        images = {name: {'labels': build_metadata.labels(name, lock, ('a'*40, 'b'*64))}
                  for name in ('daemon', 'guest', 'backend', 'frontend')}
        manifest = {'schema': 1, 'images': images, 'fork_commit': 'a'*40, 'source_tree_sha256': 'b'*64}
        build_metadata.validate_release_sources(manifest)
        images['daemon']['labels']['org.opencontainers.image.revision'] = 'c'*40
        with self.assertRaisesRegex(SystemExit, 'daemon source identity differs'):
            build_metadata.validate_release_sources(manifest)
        manifest['schema'] = 2
        manifest['component_sources'] = {name: {'fork_commit': item['labels']['org.opencontainers.image.revision'],
                                              'source_tree_sha256': item['labels']['jingjiaagent.source.tree.sha256']}
                                         for name, item in images.items()}
        build_metadata.validate_release_sources(manifest)
        manifest['component_sources']['daemon']['fork_commit'] = 'd'*40
        with self.assertRaisesRegex(SystemExit, 'daemon source identity differs'):
            build_metadata.validate_release_sources(manifest)
        manifest['component_sources']['daemon']['fork_commit'] = 'c'*40
        images['frontend']['labels']['org.opencontainers.image.revision'] = 'e'*40
        manifest['component_sources']['frontend']['fork_commit'] = 'e'*40
        with self.assertRaisesRegex(SystemExit, 'backend and frontend'):
            build_metadata.validate_release_sources(manifest)


if __name__ == '__main__':
    unittest.main()
