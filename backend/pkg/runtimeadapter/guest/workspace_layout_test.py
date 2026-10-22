import importlib.util
import json
import hashlib
import pathlib
import shutil
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('layout', pathlib.Path(__file__).with_name('workspace_layout.py'))
layout = importlib.util.module_from_spec(spec)
spec.loader.exec_module(layout)


class WorkspaceLayoutTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        layout.WORKSPACE = pathlib.Path(self.temp.name)
        self.root = layout.WORKSPACE

    def put(self, name, text='content'):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        return path

    def git(self, *args):
        return subprocess.check_output(['git', '-C', str(self.root), *args]).decode()

    def test_empty_workspace_has_only_platform_directories(self):
        self.assertTrue(layout.apply({})['installed'])
        self.assertEqual({p.name for p in self.root.iterdir()}, {'.jingjiaagent'})
        self.assertTrue((self.root/'.jingjiaagent/work').is_dir())
        self.assertTrue((self.root/'.jingjiaagent/output').is_dir())

    def test_new_coding_workspace_preserves_all_four_repository_names(self):
        for name in ('input', 'work', 'output', 'project'):
            self.put(name, 'repository-'+name)
        layout.apply({})
        for name in ('input', 'work', 'output', 'project'):
            self.assertEqual((self.root/name).read_text(), 'repository-'+name)

    def test_new_repository_folders_are_never_treated_as_legacy_outputs(self):
        source = self.put('output/assets/data.txt')
        layout.apply({})
        self.assertTrue(source.exists())
        self.assertFalse((self.root/'.jingjiaagent/output/assets/data.txt').exists())

    def test_legacy_migration_preserves_bytes_modes_and_is_idempotent(self):
        script = self.put('work/scripts/run.sh', '#!/bin/sh\necho ok\n')
        script.chmod(0o700)
        self.put('output/report.md', 'original report')
        (self.root/'input').mkdir()
        (self.root/'project').mkdir()
        result = layout.apply(dict(migrate_legacy=True))
        self.assertEqual(result['migrated'], 2)
        self.assertEqual((self.root/'.jingjiaagent/output/report.md').read_text(), 'original report')
        self.assertEqual((self.root/'.jingjiaagent/work/scripts/run.sh').stat().st_mode & 0o777, 0o700)
        self.assertTrue(all(not (self.root/name).exists() for name in ('input','work','output','project')))
        self.put('output/new-user-file.txt', 'leave alone after migration')
        self.assertEqual(layout.apply(dict(migrate_legacy=True))['migrated'], 0)
        self.assertTrue((self.root/'output/new-user-file.txt').exists())

    @unittest.skipUnless(shutil.which('git'), 'Git required')
    def test_git_migration_ignores_platform_files_and_preserves_tracked_sources(self):
        self.git('init', '-q')
        self.put('work/source.py', 'tracked source')
        self.put('output/assets/source.txt', 'tracked asset')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-qm', 'fixture')
        self.put('work/generated/context.json', '{}')
        self.put('output/report.md', 'generated report')
        self.put('output/build-not-managed.txt', 'repository build output')
        before = self.git('status', '--porcelain')
        self.assertEqual(layout.apply(dict(migrate_legacy=True, legacy_files=['work/generated/context.json','output/report.md']))['migrated'], 2)
        self.assertEqual((self.root/'work/source.py').read_text(), 'tracked source')
        self.assertEqual((self.root/'output/assets/source.txt').read_text(), 'tracked asset')
        self.assertEqual(self.git('status', '--porcelain'), before)
        self.assertFalse((self.root/'.jingjiaagent/output/build-not-managed.txt').exists())
        self.assertIn('/.jingjiaagent/', (self.root/'.git/info/exclude').read_text())
        self.assertFalse((self.root/'.gitignore').exists())

    def test_migration_conflict_keeps_original_files_and_does_not_mark_complete(self):
        self.put('work/a.txt', 'original')
        self.put('output/report.md', 'original report')
        self.put('.jingjiaagent/output/report.md', 'different report')
        with self.assertRaises(ValueError):
            layout.apply(dict(migrate_legacy=True))
        self.assertEqual((self.root/'work/a.txt').read_text(), 'original')
        self.assertEqual((self.root/'output/report.md').read_text(), 'original report')
        self.assertFalse((self.root/'.jingjiaagent/workspace-layout.json').exists())

    def test_managed_symlinks_are_rejected(self):
        with tempfile.TemporaryDirectory() as external:
            (self.root/'.jingjiaagent').symlink_to(external)
            with self.assertRaises(ValueError):
                layout.apply({})
            self.assertEqual(list(pathlib.Path(external).iterdir()), [])

    def test_legacy_file_symlink_is_not_migrated(self):
        target = self.put('private.txt', 'outside legacy folders')
        (self.root/'work').mkdir()
        (self.root/'work/link').symlink_to(target)
        with self.assertRaises(ValueError):
            layout.apply(dict(migrate_legacy=True))
        self.assertEqual(target.read_text(), 'outside legacy folders')

    def test_model_only_opencode_update_preserves_retained_config(self):
        previous_home = layout.HOME
        self.addCleanup(setattr, layout, 'HOME', previous_home)
        layout.HOME = self.root / 'home'
        config = dict(plugin=['file:///installed/plugin.js'], provider={'company': {'model': 'retained'}}, instructions=['existing.md'])
        path = self.put('home/.config/opencode/opencode.json', json.dumps(config))
        request = dict(patch_opencode=True, workspace_notice='WORKSPACE NOTICE')
        layout.apply(request)
        layout.apply(request)
        updated = json.loads(path.read_text())
        self.assertEqual(updated['plugin'], config['plugin'])
        self.assertEqual(updated['provider'], config['provider'])
        self.assertEqual(len(updated['instructions']), 2)
        self.assertEqual(updated['instructions'][0], 'existing.md')

    def test_large_inventory_uses_a_verified_internal_file(self):
        previous = layout.REQUEST_ROOT
        self.addCleanup(setattr, layout, 'REQUEST_ROOT', previous)
        layout.REQUEST_ROOT = self.root / 'state'
        content = json.dumps(dict(legacy_files=['output/' + 'a' * 100 + str(i) for i in range(1000)])).encode()
        path = self.put('state/request.json', content.decode())
        value = dict(request_path=str(path), sha256=hashlib.sha256(content).hexdigest())
        self.assertEqual(len(layout.read_request(value)['legacy_files']), 1000)
        path.write_text('{}')
        with self.assertRaises(ValueError): layout.read_request(value)
        value['request_path'] = str(self.put('outside.json', '{}'))
        with self.assertRaises(ValueError): layout.read_request(value)


if __name__ == '__main__':
    unittest.main()
