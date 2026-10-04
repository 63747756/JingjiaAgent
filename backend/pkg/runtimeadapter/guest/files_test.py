"""Linux Guest filesystem contracts; execute inside the built Guest image."""
import base64
import importlib.util
import os
import pathlib
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location('guest_files', pathlib.Path(__file__).with_name('files.py'))
files = importlib.util.module_from_spec(spec)
spec.loader.exec_module(files)


class FileContract(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = pathlib.Path(self.directory.name)

    def upload(self, name, data, mode=None):
        target = str(self.root / name)
        temp = files.run({'op':'begin','path':target})['temp']
        request = {'path':target,'temp':temp}
        for offset in range(0,len(data),32768):
            chunk = dict(request,op='write',offset=offset,data=base64.b64encode(data[offset:offset+32768]).decode())
            files.run(chunk)
            files.run(chunk)  # retry is idempotent, never appends twice
        files.run(dict(request,op='commit',size=len(data),mode=mode))
        return pathlib.Path(target)

    def test_binary_unicode_empty_and_mode(self):
        for name,data in [('中文.txt','中文内容'.encode()),('binary',bytes(range(256))*400),('empty',b'')]:
            target = self.upload(name,data,0o600)
            self.assertEqual(target.read_bytes(),data)
            self.assertEqual(target.stat().st_mode & 0o777,0o600)

    def test_atomic_abort_and_conflicts(self):
        target = self.upload('original',b'original')
        temp = files.run({'op':'begin','path':str(target)})['temp']
        with self.assertRaises(ValueError):
            files.run({'op':'commit','path':str(target),'temp':temp,'size':100})
        self.assertEqual(target.read_bytes(),b'original')
        files.run({'op':'abort','path':str(target),'temp':temp})
        self.assertFalse(pathlib.Path(temp).exists())

    def test_download_change_detection(self):
        target = self.upload('changing',b'original')
        signature = files.run({'op':'stat','path':str(target)})['signature']
        target.write_bytes(b'changed size')
        with self.assertRaisesRegex(ValueError,'changed during download'):
            files.run({'op':'read','path':str(target),'signature':signature,'offset':0,'length':32768})

    def test_copy_move_and_symlink_metadata(self):
        target = self.upload('source',b'content')
        copied, moved = self.root/'copied', self.root/'moved'
        files.run({'op':'copy','source':str(target),'target':str(copied)})
        files.run({'op':'move','source':str(copied),'target':str(moved)})
        self.assertEqual(moved.read_bytes(),b'content')
        (self.root/'link').symlink_to(moved)
        info = {item['name']:item for item in files.run({'op':'list','path':str(self.root)})}
        self.assertEqual(info['link']['kind'],'symlink')
        files.run({'op':'delete','path':str(self.root/'link')})
        self.assertTrue(moved.exists())

    def test_configuration_parent_and_home(self):
        target = self.root/'nested'/'config.json'
        temp = files.run({'op':'begin','path':str(target),'parents':True})['temp']
        files.run({'op':'commit','path':str(target),'temp':temp,'size':0,'mode':0o600})
        self.assertEqual(files.path('~/.config/test'),os.path.join(os.path.expanduser('~'),'.config/test'))
        self.assertEqual(target.parent.stat().st_mode & 0o777,0o700)

    def test_repository_listing_range_and_boundary(self):
        with tempfile.TemporaryDirectory(dir='/workspace') as directory:
            repo = pathlib.Path(directory)
            data = bytes(range(256)) * 200
            (repo/'中文.bin').write_bytes(data)
            (repo/'.hidden').write_text('hidden')
            (repo/'folder').mkdir()
            (repo/'escape').symlink_to('/etc/passwd')
            listed = {x['name']:x for x in files.run({'op':'repo_list','path':directory})}
            self.assertNotIn('.hidden',listed)
            self.assertEqual(listed['folder']['entry_mode'],4)
            self.assertEqual(listed['escape']['entry_mode'],3)
            self.assertEqual(listed['中文.bin']['size'],len(data))
            globbed = files.run({'op':'repo_list','path':directory,'glob_pattern':'*.bin'})
            self.assertEqual(len(globbed),1)
            sig = files.run({'op':'repo_stat','path':str(repo/'中文.bin')})['signature']
            chunk = files.run({'op':'repo_read','path':str(repo/'中文.bin'),'signature':sig,'offset':7,'length':100})
            self.assertEqual(base64.b64decode(chunk['data']),data[7:107])
            for escape in ['/etc/passwd',str(repo/'escape'),'../../etc/passwd']:
                with self.assertRaisesRegex(ValueError,'escapes workspace'):
                    files.run({'op':'repo_stat','path':escape})


if __name__=='__main__':
    unittest.main()
