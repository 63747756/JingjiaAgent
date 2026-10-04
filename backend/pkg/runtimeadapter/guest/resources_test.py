import io
import os
import pathlib
import stat
import tempfile
import unittest
import unittest.mock
import zipfile

import resources


def package(files):
    out = io.BytesIO()
    with zipfile.ZipFile(out, 'w') as archive:
        for name, content in files.items():
            archive.writestr(name, content)
    return out.getvalue()


class ResourceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.addCleanup(unittest.mock.patch.stopall)
        unittest.mock.patch.object(resources, 'HOME', pathlib.Path(self.temp.name)).start()
        self.packages = {
            'skill': package({'wrapper/SKILL.md': 'skill-v1', 'wrapper/scripts/run.py': 'print(42)'}),
            'plugin': package({'index.ts': 'plugin-v1'}),
        }
        unittest.mock.patch.object(resources, 'archive', lambda ref: self.packages[ref['zip_url']]).start()
        self.selection = dict(resources=dict(skills=[dict(name='中文技能', version='1', zip_url='skill')],
                                             plugins=[dict(name='runtime-plugin', version='1', zip_url='plugin', entry_filename='index.ts')]))
        self.root = resources.HOME / '.codingmatrix' / 'project-tpl' / '.ai-ready'

    def test_install_reuse_clear_and_private_modes(self):
        self.assertFalse(resources.apply(self.selection)['reused'])
        skill = self.root / 'skills' / '中文技能' / 'SKILL.md'
        self.assertEqual(skill.read_text(), 'skill-v1')
        self.assertEqual(stat.S_IMODE(skill.stat().st_mode), 0o600)
        self.assertEqual((self.root / 'plugins' / 'runtime-plugin' / 'index.ts').read_text(), 'plugin-v1')
        self.packages.clear()  # Replay does not need an expired presigned URL.
        self.assertTrue(resources.apply(self.selection)['reused'])
        resources.apply(dict(resources={}))
        self.assertEqual(list((self.root / 'skills').iterdir()), [])
        self.assertEqual(list((self.root / 'plugins').iterdir()), [])

    def test_invalid_archive_leaves_installed_selection_intact(self):
        resources.apply(self.selection)
        self.selection['resources']['skills'][0]['version'] = '2'
        for path in ('../escape', '/absolute', 'bad\\windows', 'wrapper/../../escape'):
            with self.subTest(path=path):
                self.packages['skill'] = package({path: 'bad', 'SKILL.md': 'new'})
                with self.assertRaises(resources.InvalidResource):
                    resources.apply(self.selection)
                self.assertEqual((self.root / 'skills' / '中文技能' / 'SKILL.md').read_text(), 'skill-v1')
                self.assertEqual((self.root / 'plugins' / 'runtime-plugin' / 'index.ts').read_text(), 'plugin-v1')

    def test_symlink_zip_and_managed_path_are_rejected(self):
        out = io.BytesIO()
        with zipfile.ZipFile(out, 'w') as archive:
            link = zipfile.ZipInfo('SKILL.md')
            link.create_system = 3
            link.external_attr = (stat.S_IFLNK | 0o777) << 16
            archive.writestr(link, '/tmp/external')
        self.packages['skill'] = out.getvalue()
        with self.assertRaises(resources.InvalidResource):
            resources.apply(self.selection)
        self.root.mkdir(parents=True, exist_ok=True)
        os.symlink(self.temp.name, self.root / 'skills')
        with self.assertRaises(resources.InvalidResource):
            resources.apply(self.selection)

    def test_swap_failure_rolls_back_both_kinds(self):
        resources.apply(self.selection)
        self.selection['resources']['skills'][0]['version'] = '2'
        self.packages['skill'] = package({'SKILL.md': 'skill-v2'})
        original = os.replace
        failed = False

        def fail_once(source, target):
            nonlocal failed
            if not failed and pathlib.Path(source).name == 'plugins' and pathlib.Path(target) == self.root / 'plugins':
                failed = True
                raise OSError('fixture swap failure')
            return original(source, target)

        with unittest.mock.patch.object(resources.os, 'replace', side_effect=fail_once):
            with self.assertRaises(OSError):
                resources.apply(self.selection)
        self.assertEqual((self.root / 'skills' / '中文技能' / 'SKILL.md').read_text(), 'skill-v1')
        self.assertEqual((self.root / 'plugins' / 'runtime-plugin' / 'index.ts').read_text(), 'plugin-v1')
        self.assertFalse(resources.apply(self.selection)['reused'])
        self.assertEqual((self.root / 'skills' / '中文技能' / 'SKILL.md').read_text(), 'skill-v2')

    def test_zip_bomb_limits_and_duplicate_names(self):
        self.packages['skill'] = package({'SKILL.md': 'x' * 101})
        with unittest.mock.patch.object(resources, 'MAX_FILE', 100):
            with self.assertRaises(resources.InvalidResource):
                resources.apply(self.selection)
        self.selection['resources']['skills'].append(self.selection['resources']['skills'][0])
        with self.assertRaises(resources.InvalidResource):
            resources.apply(self.selection)


if __name__ == '__main__':
    unittest.main()
