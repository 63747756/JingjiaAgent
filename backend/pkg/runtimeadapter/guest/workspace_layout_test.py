import importlib.util
import json
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
        path.write_text(text, encoding='utf-8')
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

    def test_repository_folders_are_never_copied_or_removed(self):
        source = self.put('output/assets/data.txt')
        layout.apply({})
        layout.apply({})
        self.assertEqual(source.read_text(), 'content')
        self.assertFalse((self.root/'.jingjiaagent/output/assets/data.txt').exists())

    @unittest.skipUnless(shutil.which('git'), 'Git required')
    def test_git_initialization_preserves_sources_and_ignores_platform_files(self):
        self.git('init', '-q')
        self.put('work/source.py', 'tracked source')
        self.put('output/assets/source.txt', 'tracked asset')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-qm', 'fixture')
        self.put('output/build.txt', 'repository build output')
        before = self.git('status', '--porcelain')
        layout.apply({})
        self.put('.jingjiaagent/output/report.md', 'generated report')
        layout.apply({})
        self.assertEqual((self.root/'work/source.py').read_text(), 'tracked source')
        self.assertEqual((self.root/'output/assets/source.txt').read_text(), 'tracked asset')
        self.assertEqual(self.git('status', '--porcelain'), before)
        self.assertFalse((self.root/'.jingjiaagent/output/build.txt').exists())
        self.assertEqual((self.root/'.git/info/exclude').read_text().splitlines().count('/.jingjiaagent/'), 1)
        self.assertFalse((self.root/'.gitignore').exists())

    def test_repeated_preparation_preserves_existing_results(self):
        self.put('.jingjiaagent/work/context.json', '{}')
        self.put('.jingjiaagent/output/report.md', 'current report')
        layout.apply({})
        layout.apply({})
        self.assertEqual((self.root/'.jingjiaagent/work/context.json').read_text(), '{}')
        self.assertEqual((self.root/'.jingjiaagent/output/report.md').read_text(), 'current report')

    def test_managed_symlinks_are_rejected(self):
        with tempfile.TemporaryDirectory() as external:
            (self.root/'.jingjiaagent').symlink_to(external)
            with self.assertRaises(ValueError):
                layout.apply({})
            self.assertEqual(list(pathlib.Path(external).iterdir()), [])

    def test_managed_output_symlink_is_rejected(self):
        with tempfile.TemporaryDirectory() as external:
            (self.root/'.jingjiaagent').mkdir()
            (self.root/'.jingjiaagent/output').symlink_to(external)
            with self.assertRaises(ValueError):
                layout.apply({})
            self.assertEqual(list(pathlib.Path(external).iterdir()), [])

    def test_unmanaged_repository_symlink_is_untouched(self):
        target = self.put('source.txt', 'repository source')
        (self.root/'work').mkdir()
        link = self.root/'work/link'
        link.symlink_to(target)
        layout.apply({})
        self.assertTrue(link.is_symlink())
        self.assertEqual(target.read_text(), 'repository source')

    def test_model_only_opencode_update_preserves_retained_config(self):
        previous_home = layout.HOME
        self.addCleanup(setattr, layout, 'HOME', previous_home)
        layout.HOME = self.root / 'home'
        config = dict(plugin=['file:///installed/plugin.js'], provider={'company': {'model': 'retained'}}, instructions=['existing.md'])
        path = self.put('home/.config/opencode/opencode.json', json.dumps(config))
        request = dict(patch_opencode=True, workspace_notice='当前目录规则')
        layout.apply(request)
        layout.apply(dict(request, workspace_notice='更新后的目录规则'))
        updated = json.loads(path.read_text())
        self.assertEqual(updated['plugin'], config['plugin'])
        self.assertEqual(updated['provider'], config['provider'])
        self.assertEqual(len(updated['instructions']), 2)
        self.assertEqual(updated['instructions'][0], 'existing.md')
        rule = pathlib.Path(updated['instructions'][1])
        self.assertEqual(rule.read_text(encoding='utf-8'), '更新后的目录规则')

    def test_opencode_config_link_cannot_redirect_writes(self):
        previous_home = layout.HOME
        self.addCleanup(setattr, layout, 'HOME', previous_home)
        layout.HOME = self.root / 'home'
        target = self.put('external.json', '{}')
        config_dir = self.root/'home/.config/opencode'
        config_dir.mkdir(parents=True)
        (config_dir/'opencode.json').symlink_to(target)
        with self.assertRaises(ValueError):
            layout.apply(dict(patch_opencode=True, workspace_notice='NOTICE'))
        self.assertEqual(target.read_text(), '{}')


if __name__ == '__main__':
    unittest.main()
