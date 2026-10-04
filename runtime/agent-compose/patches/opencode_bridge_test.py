import hashlib
import importlib.util
import json
import pathlib
import queue
import tempfile
import unittest
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('bridge',pathlib.Path(__file__).with_name('opencode_bridge.py'))
bridge=importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)


class NativeAttachmentTests(unittest.TestCase):
    def setUp(self):
        self.folder=tempfile.TemporaryDirectory()
        root=pathlib.Path(self.folder.name)
        bridge.ATTACHMENT_STATE=root/'state'
        bridge.ATTACHMENT_STATE.mkdir()
        bridge.ATTACHMENT_WORKSPACE=root/'workspace'
        self.task='11111111-1111-1111-1111-111111111111'
        self.command='22222222-2222-2222-2222-222222222222'
        self.root=bridge.ATTACHMENT_WORKSPACE/'.monkeycode/attachments'/self.task/self.command
        self.root.mkdir(parents=True)
        self.manifest=dict(task_id=self.task,command_id=self.command,files=[])
        for name,mime,content in [('中文附件.txt','text/plain','随机回执\n'.encode()),('二进制.bin','application/octet-stream',bytes(range(256))),('空附件.txt','text/plain',b'')]:
            path=self.root/name
            path.write_bytes(content)
            self.manifest['files'].append(dict(filename=name,path=str(path),mime=mime,size=len(content),sha256=hashlib.sha256(content).hexdigest()))
        self.pointer=bridge.ATTACHMENT_STATE/(self.task+'.json')
        self.pointer.write_text(json.dumps(self.manifest))

    def tearDown(self):
        self.folder.cleanup()

    def test_native_parts_use_local_urls_and_preserve_file_names(self):
        parts=bridge.attachment_parts(dict(task_id=self.task))
        files=[part for part in parts if part['type']=='file']
        self.assertEqual([part['filename'] for part in files],['中文附件.txt','空附件.txt'])
        self.assertTrue(all(part['url'].startswith('file://') and '?' not in part['url'] for part in files))
        self.assertIn('二进制.bin',str(parts))
        self.assertEqual(bridge.attachment_parts(dict(task_id='33333333-3333-3333-3333-333333333333')),[])
        self.manifest['files']=[]
        self.pointer.write_text(json.dumps(self.manifest))
        self.assertEqual(bridge.attachment_parts(dict(task_id=self.task)),[])

    def test_changed_bytes_and_foreign_task_paths_are_rejected(self):
        first=self.manifest['files'][0]
        pathlib.Path(first['path']).write_text('修改后')
        with self.assertRaises(ValueError):
            bridge.attachment_parts(dict(task_id=self.task))
        first['path']=str(bridge.ATTACHMENT_WORKSPACE/'foreign.txt')
        pathlib.Path(first['path']).write_text('foreign')
        self.pointer.write_text(json.dumps(self.manifest))
        with self.assertRaises(ValueError):
            bridge.attachment_parts(dict(task_id=self.task))

    def test_manifest_correlation_and_symlink_are_required(self):
        self.manifest['task_id']='33333333-3333-3333-3333-333333333333'
        self.pointer.write_text(json.dumps(self.manifest))
        with self.assertRaises(ValueError):
            bridge.attachment_parts(dict(task_id=self.task))
        self.manifest['task_id']=self.task
        first=self.manifest['files'][0]
        path=pathlib.Path(first['path'])
        outside=bridge.ATTACHMENT_WORKSPACE/'outside.txt'
        outside.write_bytes(path.read_bytes())
        path.unlink()
        path.symlink_to(outside)
        self.pointer.write_text(json.dumps(self.manifest))
        with self.assertRaises(ValueError):
            bridge.attachment_parts(dict(task_id=self.task))


class TextStreamingTests(unittest.TestCase):
    def test_native_token_events_are_coalesced_per_poll(self):
        native,record,updates,offsets,output=bridge.NativeTextUpdates(),dict(session_id='s'),queue.Queue(),{},[]
        events=[dict(type='message.updated',properties=dict(info=dict(id='m',sessionID='s',role='assistant'))),
                dict(type='message.part.updated',properties=dict(part=dict(id='p',sessionID='s',messageID='m',type='text',text='')))]
        events += [dict(type='message.part.delta',properties=dict(sessionID='s',messageID='m',partID='p',field='text',delta=word)) for word in ['中','文','回','答']]
        for event in events:updates.put(event)
        with patch.object(bridge,'emit',side_effect=lambda kind,session,p,**extra:output.append(p['text'])):
            native.drain(record,set(),offsets,updates)
            native.drain(record,set(),offsets,updates)
            self.assertEqual(output,['中文回答'])
            updates.put(dict(type='message.part.delta',properties=dict(sessionID='s',messageID='m',partID='p',field='text',delta='继续')))
            native.drain(record,set(),offsets,updates)
            self.assertEqual(output,['中文回答','继续'])

    def test_native_subscription_streams_before_database_snapshot_and_reconciles(self):
        record, previous, offsets, output = dict(session_id='s'), {'old'}, {}, []
        native = bridge.NativeTextUpdates()
        def consume(kind, data):
            native.consume(record, previous, offsets, dict(type=kind, properties=data))
        with patch.object(bridge, 'emit', side_effect=lambda kind, session, p, **extra: output.append((kind, p['text'], extra))):
            consume('message.updated', dict(info=dict(id='m', sessionID='s', role='assistant')))
            consume('message.part.updated', dict(part=dict(id='p', sessionID='s', messageID='m', type='text', text='')))
            consume('message.part.delta', dict(sessionID='s', messageID='m', partID='p', field='text', delta='中文'))
            consume('message.part.delta', dict(sessionID='s', messageID='m', partID='p', field='text', delta='回答'))
            self.assertEqual([item[1] for item in output], ['中文', '回答'])
            # The native HTTP snapshot can lag behind its SSE publication.
            snapshot=dict(id='p',sessionID='s',messageID='m',type='text',text='',time=dict(start=1))
            message=dict(info=dict(id='m',role='assistant'),parts=[snapshot])
            with patch.object(bridge, 'api', return_value=[message]):
                bridge.parts(record, previous, set(), offsets)
                snapshot.update(text='中文回答补齐',time=dict(start=1,end=2))
                emitted=set()
                bridge.parts(record, previous, emitted, offsets)
                bridge.parts(record, previous, emitted, offsets)
            self.assertEqual([item[1] for item in output], ['中文', '回答', '补齐', '中文回答补齐'])
            self.assertTrue(output[-1][2]['monkeycode_streamed'])
            consume('message.part.updated', dict(part=snapshot))
            self.assertEqual(len(output),4)

    def test_native_subscription_rejects_foreign_user_previous_and_non_text_events(self):
        native, record, offsets, output=bridge.NativeTextUpdates(),dict(session_id='s'),{},[]
        def consume(kind,data):native.consume(record,{'old'},offsets,dict(type=kind,properties=data))
        with patch.object(bridge,'emit',side_effect=lambda kind,*args,**extra:output.append(kind)):
            for message,session,role in [('old','s','assistant'),('foreign','other','assistant'),('user','s','user')]:
                consume('message.updated',dict(info=dict(id=message,sessionID=session,role=role)))
                consume('message.part.updated',dict(part=dict(id=message,sessionID=session,messageID=message,type='text',text='private')))
                consume('message.part.delta',dict(sessionID=session,messageID=message,partID=message,field='text',delta='ignored'))
            consume('message.updated',dict(info=dict(id='a',sessionID='s',role='assistant')))
            consume('message.part.updated',dict(part=dict(id='r',sessionID='s',messageID='a',type='reasoning',text='')))
            consume('message.part.delta',dict(sessionID='s',messageID='a',partID='r',field='metadata',delta='ignored'))
            consume('message.part.delta',dict(sessionID='s',messageID='a',partID='missing',field='text',delta='ignored'))
            self.assertEqual(output,[])
            consume('message.part.delta',dict(sessionID='s',messageID='a',partID='r',field='text',delta='思考'))
            self.assertEqual(output,['monkeycode_reasoning_delta'])

    def test_unfinished_parts_stream_suffixes_and_complete_once(self):
        record = dict(session_id='session')
        offsets, emitted, output = {}, set(), []
        part = dict(id='text1', type='text', text='中文', time=dict(start=1))
        message = dict(info=dict(id='message1', role='assistant'), parts=[part])
        with patch.object(bridge, 'api', return_value=[message]), patch.object(bridge, 'emit', side_effect=lambda kind, session, p, **extra: output.append((kind, p.copy(), extra))):
            bridge.parts(record, set(), emitted, offsets)
            bridge.parts(record, set(), emitted, offsets)
            self.assertEqual([item[1]['text'] for item in output], ['中文'])
            part['text'] = '中文回答'
            bridge.parts(record, set(), emitted, offsets)
            part['time']['end'] = 2
            bridge.parts(record, set(), emitted, offsets)
            bridge.parts(record, set(), emitted, offsets)
        self.assertEqual([item[0] for item in output], ['monkeycode_text_delta', 'monkeycode_text_delta', 'text'])
        self.assertEqual([item[1]['text'] for item in output], ['中文', '回答', '中文回答'])
        self.assertTrue(output[-1][2]['monkeycode_streamed'])

    def test_reasoning_old_snapshots_and_previous_turns(self):
        part = dict(id='thought1', type='reasoning', text='思考中', time={})
        message = dict(info=dict(id='m1', role='assistant'), parts=[part])
        offsets, emitted, output = {}, set(), []
        with patch.object(bridge, 'api', return_value=[message]), patch.object(bridge, 'emit', side_effect=lambda kind, *args, **extra: output.append(kind)):
            bridge.parts(dict(session_id='s'), {'m1'}, emitted, offsets)
            self.assertEqual(output, [])
            bridge.parts(dict(session_id='s'), set(), emitted, offsets)
            part['text'] = '思考'
            bridge.parts(dict(session_id='s'), set(), emitted, offsets)
            self.assertEqual(output, ['monkeycode_reasoning_delta'])
            part['text'] = '改写'
            with self.assertRaises(ValueError):
                bridge.parts(dict(session_id='s'), set(), emitted, offsets)


if __name__=='__main__':
    unittest.main()
