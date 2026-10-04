"""Run only in an expendable Linux container with an empty /workspace."""
import importlib.util
import os
import pathlib
import subprocess
import sys
import unittest

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location('guest_files', pathlib.Path(__file__).with_name('files.py'))
files = importlib.util.module_from_spec(spec)
spec.loader.exec_module(files)


class RepositoryTest(unittest.TestCase):
    def test_git_unicode_rename_staged_unstaged_binary_delete_and_paths(self):
        root = pathlib.Path('/workspace')
        self.assertFalse((root / '.git').exists(), 'test requires empty isolated workspace')
        text = root / '中文 空格.txt'
        text.write_text('baseline\n', encoding='utf-8')
        (root / 'rename.txt').write_text('rename content\n')
        (root / 'deleted.txt').write_text('deleted content\n')
        (root / 'binary.bin').write_bytes(b'\x00\x01')
        initial = files.run(dict(op='repo_changes'))
        self.assertIn('中文 空格.txt', {x['path'] for x in initial['changes']})
        self.assertIn('+baseline', files.run(dict(op='repo_diff', path='中文 空格.txt')))
        subprocess.run(['git','init','-q'],cwd=root,check=True)
        subprocess.run(['git','add','.'],cwd=root,check=True)
        subprocess.run(['git','-c','user.email=poc@example.invalid','-c','user.name=PoC','commit','-qm','baseline'],cwd=root,check=True)
        text.write_text('staged\n',encoding='utf-8')
        subprocess.run(['git','add','中文 空格.txt'],cwd=root,check=True)
        text.write_text('unstaged\n',encoding='utf-8')
        subprocess.run(['git','mv','rename.txt','renamed.txt'],cwd=root,check=True)
        (root / 'deleted.txt').unlink()
        (root / 'binary.bin').write_bytes(b'\x00\xff')
        (root / 'untracked.txt').write_text('untracked\n')
        changes = files.run(dict(op='repo_changes'))
        by_path = {x['path']:x for x in changes['changes']}
        self.assertEqual(by_path['中文 空格.txt']['additions'],1)
        self.assertEqual(by_path['中文 空格.txt']['deletions'],1)
        self.assertEqual(by_path['renamed.txt']['old_path'],'rename.txt')
        self.assertEqual(by_path['deleted.txt']['status'],'D')
        self.assertEqual(by_path['untracked.txt']['status'],'??')
        self.assertNotIn('additions',by_path['binary.bin'])
        self.assertTrue(changes['commit_hash'])
        diff = files.run(dict(op='repo_diff',path='中文 空格.txt',context_lines=0))
        self.assertIn('-baseline',diff)
        self.assertIn('+unstaged',diff)
        self.assertNotIn('+staged',diff)
        self.assertIn('rename from',files.run(dict(op='repo_diff',path='renamed.txt')))
        self.assertIn('-deleted content',files.run(dict(op='repo_diff',path='deleted.txt')))
        self.assertIn('+untracked',files.run(dict(op='repo_diff',path='untracked.txt')))
        self.assertIn('Binary files',files.run(dict(op='repo_diff',path='binary.bin')))
        with self.assertRaisesRegex(ValueError,'escapes workspace'):
            files.run(dict(op='repo_diff',path='../home/secret'))
        # Literal pathspecs prevent a filename from becoming a Git selector.
        with self.assertRaisesRegex(ValueError,'not found'):
            files.run(dict(op='repo_diff',path=':(glob)**'))
        os.environ['GIT_WORK_TREE'] = '/tmp'
        self.assertIn('+unstaged',files.run(dict(op='repo_diff',path='中文 空格.txt')))
        del os.environ['GIT_WORK_TREE']


if __name__ == '__main__':
    unittest.main()
