"""Exercise packaging with an in-memory Docker boundary and temporary output."""
import hashlib
import json
import pathlib
import re
import runpy
import shutil
import subprocess
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
                'Config': {'Labels': build_metadata.labels(component, lock, ('a'*40, 'b'*64))}}
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
            self.assertIn(' ps\n', readme)
            self.assertIn(' logs --tail 100 backend runtime web\n', readme)
            manifest = json.loads((bundle/'manifest.json').read_text())
            self.assertEqual(manifest['component_revisions']['frontend'], lock['frontend_patch_revision'])
            self.assertEqual(manifest['archive_sha256'], hashlib.sha256(b'fixture image archive').hexdigest())


if __name__ == '__main__':
    unittest.main()
