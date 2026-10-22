import hashlib
import http.server
import importlib.util
import json
import pathlib
import tempfile
import threading
import unittest
import urllib.error

spec=importlib.util.spec_from_file_location('attachments',pathlib.Path(__file__).with_name('attachments.py'))
attachments=importlib.util.module_from_spec(spec)
spec.loader.exec_module(attachments)


class AttachmentTests(unittest.TestCase):
    def setUp(self):
        self.folder=tempfile.TemporaryDirectory()
        root=pathlib.Path(self.folder.name)
        attachments.WORKSPACE=root / 'workspace'
        attachments.WORKSPACE.mkdir()
        attachments.STATE=root / 'state'
        attachments.BRIDGE=root / 'bridge.py'
        attachments.BRIDGE.write_text('ATTACHMENT_PROTOCOL = 1')
        self.bodies={'/text':'中文附件原样传输\n'.encode(),'/binary':bytes(range(256))*8,'/empty':b''}
        self.calls=[]
        case=self
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                case.calls.append(self.path)
                if self.path=='/redirect':
                    self.send_response(302)
                    self.send_header('Location','/text')
                    self.end_headers()
                    return
                self.send_response(200)
                self.send_header('Content-Type','application/octet-stream')
                self.end_headers()
                self.wfile.write(case.bodies[self.path])
            def log_message(self,*args):
                pass
        self.server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler)
        self.thread=threading.Thread(target=self.server.serve_forever,daemon=True)
        self.thread.start()
        self.req=dict(task_id='11111111-1111-1111-1111-111111111111',command_id='22222222-2222-2222-2222-222222222222',
                      attachments=[dict(url=self.url(key),filename=name) for key,name in [('/text','中文附件.txt'),('/binary','二进制.bin'),('/empty','空文件.txt')]])

    def url(self,path):
        return 'http://127.0.0.1:'+str(self.server.server_port)+path

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()
        self.folder.cleanup()

    def receipt(self):
        return json.loads((attachments.STATE/(self.req['task_id']+'.json')).read_text())

    def test_exact_bytes_replay_and_clear_selection(self):
        result=attachments.install(self.req)
        self.assertTrue(result['installed'])
        receipt=self.receipt()
        self.assertEqual(result['files'],[dict(filename=item['filename'],path=item['path']) for item in receipt['files']])
        for item,body in zip(receipt['files'],self.bodies.values()):
            self.assertEqual(pathlib.Path(item['path']).read_bytes(),body)
            self.assertEqual(item['sha256'],hashlib.sha256(body).hexdigest())
            self.assertEqual(item['size'],len(body))
        self.assertNotIn('url',json.dumps(receipt))
        self.assertEqual(attachments.install(self.req),result)
        self.assertEqual(len(self.calls),3)
        changed=dict(self.req,attachments=[dict(url=self.url('/text'),filename='changed.txt')])
        with self.assertRaises(ValueError):
            attachments.install(changed)
        cleared=dict(self.req,command_id='33333333-3333-3333-3333-333333333333',attachments=[])
        attachments.install(cleared)
        self.assertEqual(self.receipt()['files'],[])
        for item in receipt['files']:
            self.assertTrue(pathlib.Path(item['path']).is_file())

    def test_work_receipt_replay_and_empty_round_preserve_actual_paths(self):
        request=dict(self.req,work_mode=True)
        result=attachments.install(request)
        root=attachments.WORKSPACE/'.jingjiaagent/attachments'/request['task_id']/request['command_id']
        self.assertTrue(result['installed'])
        for index,item in enumerate(result['files']):
            self.assertEqual(set(item),{'filename','path'})
            self.assertEqual(pathlib.Path(item['path']),root/(str(index+1)+'-'+item['filename']))
            self.assertTrue(pathlib.Path(item['path']).is_file())
        self.assertEqual(attachments.install(request),result)
        self.assertEqual(len(self.calls),3)
        cleared=dict(request,command_id='33333333-3333-3333-3333-333333333333',attachments=[])
        self.assertEqual(attachments.install(cleared),dict(installed=True,files=[]))
        self.assertEqual(self.receipt()['files'],[])
        self.assertTrue(all(pathlib.Path(item['path']).is_file() for item in result['files']))

    def test_work_upload_does_not_create_or_overwrite_repository_entries(self):
        for directory in ('input', 'work', 'output', 'project'):
            (attachments.WORKSPACE/directory).write_text('repository-file-'+directory)
        empty=dict(self.req,work_mode=True,attachments=[])
        self.assertEqual(attachments.install(empty),dict(installed=True,files=[]))
        result=attachments.install(dict(self.req,work_mode=True))
        self.assertTrue(self.receipt()['work_mode'])
        self.assertEqual(len(result['files']),3)
        for directory in ('input', 'work', 'output', 'project'):
            self.assertEqual((attachments.WORKSPACE/directory).read_text(),'repository-file-'+directory)

    def test_empty_work_round_does_not_create_office_directories(self):
        attachments.install(dict(self.req,work_mode=True,attachments=[]))
        self.assertEqual(list(attachments.WORKSPACE.iterdir()),[])

    def test_recreated_workspace_restores_prior_rounds_before_clearing_current_selection(self):
        original=dict(self.req,work_mode=True)
        first=attachments.install(original)
        later=dict(original,command_id='33333333-3333-3333-3333-333333333333',
                   attachments=[dict(url=self.url('/binary'),filename='中文附件.txt')])
        second=attachments.install(later)
        # A replacement sandbox starts without the original files or pointer.
        attachments.WORKSPACE=pathlib.Path(self.folder.name)/'replacement'
        attachments.WORKSPACE.mkdir()
        attachments.STATE=pathlib.Path(self.folder.name)/'replacement-state'
        recovered=attachments.install(original)['files']+attachments.install(later)['files']
        self.assertEqual(len(recovered),len(first['files'])+len(second['files']))
        self.assertTrue(all(pathlib.Path(item['path']).is_file() for item in recovered))
        same_name=[item for item in recovered if item['filename']=='中文附件.txt']
        self.assertEqual(len({item['path'] for item in same_name}),2)
        self.assertEqual(pathlib.Path(same_name[0]['path']).read_bytes(),self.bodies['/text'])
        self.assertEqual(pathlib.Path(same_name[1]['path']).read_bytes(),self.bodies['/binary'])
        current=dict(original,command_id='44444444-4444-4444-4444-444444444444',attachments=[])
        self.assertEqual(attachments.install(current),dict(installed=True,files=[]))
        self.assertTrue(all(pathlib.Path(item['path']).is_file() for item in recovered))

    def test_invalid_name_redirect_and_limits_do_not_commit(self):
        for name in ['../逃逸.txt','a/b.txt','a\\b.txt','..','a\x00.txt']:
            with self.assertRaises(ValueError):
                attachments.install(dict(self.req,attachments=[dict(url=self.url('/text'),filename=name)]))
        with self.assertRaises(urllib.error.HTTPError):
            attachments.install(dict(self.req,attachments=[dict(url=self.url('/redirect'),filename='redirect.txt')]))
        self.assertEqual(self.calls,['/redirect'])
        previous=attachments.MAX_FILE
        attachments.MAX_FILE=1
        try:
            with self.assertRaises(ValueError):
                attachments.install(self.req)
        finally:
            attachments.MAX_FILE=previous
        self.assertFalse((attachments.STATE/(self.req['task_id']+'.json')).exists())
        self.assertEqual(list((attachments.WORKSPACE/'.jingjiaagent/attachments'/self.req['task_id']).iterdir()),[])

    def test_guest_capability_and_symlink_escape_fail_closed(self):
        attachments.BRIDGE.write_text('old Guest')
        with self.assertRaises(ValueError):
            attachments.install(self.req)
        attachments.BRIDGE.write_text('ATTACHMENT_PROTOCOL = 1')
        outside=pathlib.Path(self.folder.name)/'outside'
        outside.mkdir()
        (attachments.WORKSPACE/'.jingjiaagent').symlink_to(outside,target_is_directory=True)
        with self.assertRaises(ValueError):
            attachments.install(self.req)
        self.assertEqual(list(outside.iterdir()),[])


if __name__=='__main__':
    unittest.main()
