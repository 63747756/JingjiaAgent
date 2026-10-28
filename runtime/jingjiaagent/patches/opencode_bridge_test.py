import hashlib
import importlib.util
import io
import json
import pathlib
import queue
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
spec=importlib.util.spec_from_file_location('bridge',pathlib.Path(__file__).with_name('opencode_bridge.py'))
bridge=importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)


class PromptInputTests(unittest.TestCase):
    def parse(self, content, extra=()):
        with patch.object(bridge.sys, 'argv', ['bridge', 'run', '--prompt-stdin', '--dir', '/workspace', *extra]), \
                patch.object(bridge.sys, 'stdin', io.TextIOWrapper(io.BytesIO(content), encoding='ascii')):
            return bridge.parse_args()

    def test_long_utf8_prompt_preserves_whitespace_and_control_arguments(self):
        text = '--model is prompt text\r\n' + ('中文😀\t"quoted"\\path\n' * 20000) + '\r\n  '
        args = self.parse(text.encode('utf-8'), ['--format', 'json', '--model', 'fixture/test', '--session', 'ses_existing'])
        self.assertGreater(len(text.encode('utf-8')), 128 * 1024)
        self.assertEqual(args.prompt, text)
        self.assertEqual((args.dir, args.model, args.session), ('/workspace', 'fixture/test', 'ses_existing'))

    def test_invalid_utf8_is_rejected_instead_of_replaced(self):
        with self.assertRaises(UnicodeDecodeError):
            self.parse(b'prefix\xffsuffix')

    def test_stdin_protocol_is_required(self):
        with patch.object(bridge.sys, 'argv', ['bridge', 'run', '--dir', '/workspace']), \
                patch.object(bridge.sys, 'stderr', io.StringIO()), self.assertRaises(SystemExit) as failure:
            bridge.parse_args()
        self.assertEqual(failure.exception.code, 2)


class NativeAttachmentTests(unittest.TestCase):
    def setUp(self):
        self.folder=tempfile.TemporaryDirectory()
        root=pathlib.Path(self.folder.name)
        bridge.ATTACHMENT_STATE=root/'state'
        bridge.ATTACHMENT_STATE.mkdir()
        bridge.ATTACHMENT_WORKSPACE=root/'workspace'
        self.task='11111111-1111-1111-1111-111111111111'
        self.command='22222222-2222-2222-2222-222222222222'
        self.root=bridge.ATTACHMENT_WORKSPACE/'.jingjiaagent/attachments'/self.task/self.command
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

    def test_work_input_scope_hash_and_path_only_delivery(self):
        work=bridge.ATTACHMENT_WORKSPACE/'input'/self.task/self.command
        work.mkdir(parents=True)
        for item in self.manifest['files']:
            target=work/pathlib.Path(item['path']).name
            target.write_bytes(pathlib.Path(item['path']).read_bytes())
            item['path']=str(target)
        self.pointer.write_text(json.dumps(self.manifest))
        parts=bridge.attachment_parts(dict(task_id=self.task))
        self.assertEqual(len(parts),3)
        self.assertTrue(all(part['type']=='text' and str(work) in part['text'] for part in parts))
        target=pathlib.Path(self.manifest['files'][0]['path'])
        target.write_bytes(b'changed')
        with self.assertRaises(ValueError): bridge.attachment_parts(dict(task_id=self.task))
        self.manifest['files'][0]['path']=str(work.parent/'foreign-command'/target.name)
        self.pointer.write_text(json.dumps(self.manifest))
        with self.assertRaises(ValueError): bridge.attachment_parts(dict(task_id=self.task))

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

    def test_work_images_and_pdf_keep_native_file_parts_in_both_locations(self):
        for private_root in (False, True):
            with self.subTest(private_root=private_root):
                root=self.root if private_root else bridge.ATTACHMENT_WORKSPACE/'input'/self.task/self.command
                root.mkdir(parents=True,exist_ok=True)
                self.manifest.update(work_mode=True,files=[])
                for name,mime,body in [('image.png','image/png',b'\x89PNG\r\n'),('report.pdf','application/pdf',b'%PDF-1.4'),('table.csv','text/csv',b'a,b\n1,2')]:
                    path=root/name
                    path.write_bytes(body)
                    self.manifest['files'].append(dict(filename=name,path=str(path),mime=mime,size=len(body),sha256=hashlib.sha256(body).hexdigest()))
                self.pointer.write_text(json.dumps(self.manifest))
                parts=bridge.attachment_parts(dict(task_id=self.task))
                self.assertEqual([part['mime'] for part in parts if part['type']=='file'],['image/png','application/pdf'])
                self.assertEqual(len([part for part in parts if part['type']=='text']),3)


class TextStreamingTests(unittest.TestCase):
    def test_reconnect_discards_incomplete_delta_base_and_repairs_from_snapshot(self):
        for kind in ('text', 'reasoning'):
            with self.subTest(kind=kind):
                native, record, offsets, output = bridge.NativeTextUpdates(), dict(session_id='s'), {}, []
                def consume(event, **data):
                    native.consume(record, set(), offsets, dict(type=event, properties=data))
                snapshot = dict(id='p', sessionID='s', messageID='m', type=kind, text='', time=dict(start=1))
                message = dict(info=dict(id='m', role='assistant'), parts=[snapshot])
                with patch.object(bridge, 'emit', side_effect=lambda event, session, p, **extra: output.append((event, p['text']))):
                    consume('message.updated', info=message['info'] | dict(sessionID='s'))
                    consume('message.part.updated', part=snapshot)
                    consume('message.part.delta', sessionID='s', messageID='m', partID='p', field='text', delta='甲')
                    # 乙 is generated while disconnected. The database still
                    # contains an empty unfinished part when SSE reconnects.
                    consume('jingjiaagent_subscription_reset')
                    consume('message.part.updated', part=snapshot)
                    consume('message.part.delta', sessionID='s', messageID='m', partID='p', field='text', delta='丙')
                    self.assertEqual(offsets['p'], '甲')
                    self.assertEqual(output, [('jingjiaagent_' + kind + '_delta', '甲')])
                    emitted = set()
                    bridge.publish_messages(record, [message], emitted, offsets)
                    snapshot.update(text='甲乙丙', time=dict(start=1, end=2))
                    bridge.publish_messages(record, [message], emitted, offsets)
                    bridge.publish_messages(record, [message], emitted, offsets)
                    consume('message.part.updated', part=snapshot)
                self.assertEqual(offsets['p'], '甲乙丙')
                self.assertEqual(output, [('jingjiaagent_' + kind + '_delta', '甲'),
                                         ('jingjiaagent_' + kind + '_delta', '乙丙'), (kind, '甲乙丙')])

    def test_reconnect_never_reenables_deltas_from_snapshots_or_new_parts(self):
        native, record, offsets, updates, output = bridge.NativeTextUpdates(), dict(session_id='s'), {}, queue.Queue(), []
        def put(kind, **data):
            updates.put(dict(type=kind, properties=data))
        put('message.updated', info=dict(id='m', sessionID='s', role='assistant'))
        put('message.part.updated', part=dict(id='p', sessionID='s', messageID='m', type='text', text=''))
        put('message.part.delta', sessionID='s', messageID='m', partID='p', field='text', delta='甲')
        with patch.object(bridge, 'emit', side_effect=lambda kind, session, p, **extra: output.append(p['text'])):
            native.drain(record, set(), offsets, updates)
            put('jingjiaagent_subscription_reset')
            put('message.part.delta', sessionID='s', messageID='m', partID='p', field='text', delta='丙')
            put('message.part.updated', part=dict(id='p', sessionID='s', messageID='m', type='text', text='甲乙丙'))
            put('message.part.delta', sessionID='s', messageID='m', partID='p', field='text', delta='丁')
            native.drain(record, set(), offsets, updates)
            # Another gap must also invalidate a previously repaired base.
            put('jingjiaagent_subscription_reset')
            put('message.part.delta', sessionID='s', messageID='m', partID='p', field='text', delta='己')
            put('message.part.updated', part=dict(id='next', sessionID='s', messageID='m', type='text', text=''))
            put('message.part.delta', sessionID='s', messageID='m', partID='next', field='text', delta='新段')
            native.drain(record, set(), offsets, updates)
            self.assertEqual(output, ['甲', '乙丙'])
            # Even a longer snapshot, or a newly observed part, cannot locate
            # deltas across a gap. Only full snapshots repair the remainder.
            put('message.part.updated', part=dict(id='p', sessionID='s', messageID='m', type='text', text='甲乙丙丁戊己', time=dict(end=2)))
            put('message.part.updated', part=dict(id='next', sessionID='s', messageID='m', type='text', text='新段', time=dict(end=2)))
            native.drain(record, set(), offsets, updates)
        self.assertEqual(output, ['甲', '乙丙', '丁戊己', '新段'])
        self.assertEqual(offsets, {'p': '甲乙丙丁戊己', 'next': '新段'})

    def test_reconnect_batch_boundaries_preserve_unpublished_prefix(self):
        # A synthetic post-reset old snapshot is an extra premise, not an
        # assertion that OpenCode's ordinary reconnect replays old events.
        for kind in ('text', 'reasoning'):
            for prior_drained in (False, True):
                for stale in (None, '', 'A', 'AB'):
                    for final_same_batch in (False, True):
                        with self.subTest(kind=kind, prior_drained=prior_drained, stale=stale, final_same_batch=final_same_batch):
                            native, record, offsets, updates, output = bridge.NativeTextUpdates(), dict(session_id='s'), {}, queue.Queue(), []
                            def put(event, **data):
                                updates.put(dict(type=event, properties=data))
                            def snapshot(text, final=False):
                                return dict(id='p', sessionID='s', messageID='m', type=kind, text=text,
                                            time=dict(start=1, **({'end': 2} if final else {})))
                            put('message.updated', info=dict(id='m', sessionID='s', role='assistant'))
                            put('message.part.updated', part=snapshot(''))
                            put('message.part.delta', sessionID='s', messageID='m', partID='p', field='text', delta='A')
                            with patch.object(bridge, 'emit', side_effect=lambda event, session, p, **extra: output.append((event, p['text']))):
                                if prior_drained:
                                    native.drain(record, set(), offsets, updates)
                                put('jingjiaagent_subscription_reset')
                                if stale is not None:
                                    put('message.part.updated', part=snapshot(stale))
                                # BC was missed. Old snapshots can be shorter,
                                # equal to, or longer than the published prefix.
                                put('message.part.delta', sessionID='s', messageID='m', partID='p', field='text', delta='D')
                                if final_same_batch:
                                    put('message.part.updated', part=snapshot('ABCD', final=True))
                                native.drain(record, set(), offsets, updates)
                                self.assertEqual(output[0], ('jingjiaagent_' + kind + '_delta', 'A'))
                                if not final_same_batch:
                                    self.assertEqual(offsets, {'p': 'AB' if stale == 'AB' else 'A'})
                                    put('message.part.updated', part=snapshot('ABCD', final=True))
                                    native.drain(record, set(), offsets, updates)
                                native.drain(record, set(), offsets, updates)
                                message = dict(info=dict(id='m', role='assistant'), parts=[snapshot('ABCD', final=True)])
                                emitted = set()
                                bridge.publish_messages(record, [message], emitted, offsets)
                                bridge.publish_messages(record, [message], emitted, offsets)
                            self.assertEqual(offsets, {'p': 'ABCD'})
                            self.assertEqual(''.join(text for event, text in output if event.startswith('jingjiaagent_')), 'ABCD')
                            self.assertEqual([text for event, text in output if event == kind], ['ABCD'])

    def test_repeated_resets_preserve_multiple_text_and_reasoning_parts(self):
        for final_source in ('sse', 'http'):
            with self.subTest(final_source=final_source):
                native, record, offsets, updates, output = bridge.NativeTextUpdates(), dict(session_id='s'), {}, queue.Queue(), []
                def put(event, **data):
                    updates.put(dict(type=event, properties=data))
                def snapshot(identifier, kind, text, final=False):
                    return dict(id=identifier, sessionID='s', messageID='m', type=kind, text=text,
                                time=dict(start=1, **({'end': 2} if final else {})))
                put('message.updated', info=dict(id='m', sessionID='s', role='assistant'))
                for identifier, kind, text in [('p', 'text', 'A'), ('r', 'reasoning', '甲')]:
                    put('message.part.updated', part=snapshot(identifier, kind, ''))
                    put('message.part.delta', sessionID='s', messageID='m', partID=identifier, field='text', delta=text)
                for _ in range(2):
                    put('jingjiaagent_subscription_reset')
                    for identifier, kind, text in [('p', 'text', 'A'), ('r', 'reasoning', '')]:
                        put('message.part.updated', part=snapshot(identifier, kind, text))
                        put('message.part.delta', sessionID='s', messageID='m', partID=identifier, field='text', delta='unsafe')
                # This part may have started during either gap; an empty stale
                # snapshot does not prove that its next observed delta is first.
                put('message.part.updated', part=snapshot('new', 'text', ''))
                put('message.part.delta', sessionID='s', messageID='m', partID='new', field='text', delta='suffix')
                final = [snapshot('p', 'text', 'ABC', True), snapshot('r', 'reasoning', '甲乙丙', True),
                         snapshot('new', 'text', 'prefix-suffix', True)]
                with patch.object(bridge, 'emit', side_effect=lambda event, session, p, **extra: output.append((event, p['id'], p['text']))):
                    native.drain(record, set(), offsets, updates)
                    self.assertEqual(offsets, {'p': 'A', 'r': '甲'})
                    self.assertEqual(len(output), 2)
                    if final_source == 'sse':
                        for part in final:
                            put('message.part.updated', part=part)
                        native.drain(record, set(), offsets, updates)
                    emitted = set()
                    message = dict(info=dict(id='m', role='assistant'), parts=final)
                    bridge.publish_messages(record, [message], emitted, offsets)
                    bridge.publish_messages(record, [message], emitted, offsets)
                self.assertEqual(offsets, {'p': 'ABC', 'r': '甲乙丙', 'new': 'prefix-suffix'})
                for part in final:
                    self.assertEqual(''.join(text for event, identifier, text in output if identifier == part['id'] and event.startswith('jingjiaagent_')), part['text'])
                    self.assertEqual(sum(event == part['type'] and identifier == part['id'] for event, identifier, text in output), 1)

    def test_reconnect_does_not_hide_conflicting_snapshot_or_native_errors(self):
        for kind in ('text', 'reasoning'):
            with self.subTest(kind=kind):
                native, record, offsets, updates, output = bridge.NativeTextUpdates(), dict(session_id='s'), {}, queue.Queue(), []
                def put(event, **data):
                    updates.put(dict(type=event, properties=data))
                put('message.updated', info=dict(id='m', sessionID='s', role='assistant'))
                put('message.part.updated', part=dict(id='p', sessionID='s', messageID='m', type=kind, text='A'))
                put('jingjiaagent_subscription_reset')
                put('message.part.updated', part=dict(id='p', sessionID='s', messageID='m', type=kind, text='conflict'))
                with patch.object(bridge, 'emit', side_effect=lambda event, session, p, **extra: output.append(p['text'])):
                    with self.assertRaisesRegex(ValueError, 'changed after publication'):
                        native.drain(record, set(), offsets, updates)
                    self.assertEqual(output, ['A'])
                    self.assertEqual(offsets, {'p': 'A'})
                    message = dict(info=dict(id='m', role='assistant', error=dict(name='APIError')), parts=[])
                    with self.assertRaisesRegex(ValueError, 'native Agent request failed'):
                        bridge.publish_messages(record, [message], set(), offsets)

    def test_cancelled_message_after_gap_reconciles_only_persisted_text(self):
        for kind in ('text', 'reasoning'):
            with self.subTest(kind=kind):
                native, record, offsets, updates, output = bridge.NativeTextUpdates(), dict(session_id='s'), {}, queue.Queue(), []
                def put(event, **data):
                    updates.put(dict(type=event, properties=data))
                put('message.updated', info=dict(id='m', sessionID='s', role='assistant'))
                part = dict(id='p', sessionID='s', messageID='m', type=kind, text='', time=dict(start=1))
                put('message.part.updated', part=part)
                put('message.part.delta', sessionID='s', messageID='m', partID='p', field='text', delta='A')
                put('jingjiaagent_subscription_reset')
                put('message.part.updated', part=part | dict(text='A'))
                put('message.part.delta', sessionID='s', messageID='m', partID='p', field='text', delta='C')
                cancelled = dict(info=dict(id='m', role='assistant', error=dict(name='MessageAbortedError')),
                                 parts=[part | dict(text='ABC', time=dict(start=1, end=2))])
                with patch.object(bridge, 'emit', side_effect=lambda event, session, p, **extra: output.append((event, p['text']))):
                    native.drain(record, set(), offsets, updates)
                    self.assertEqual(output, [('jingjiaagent_' + kind + '_delta', 'A')])
                    emitted = set()
                    bridge.publish_messages(record, [cancelled], emitted, offsets)
                    bridge.publish_messages(record, [cancelled], emitted, offsets)
                    put('message.part.delta', sessionID='s', messageID='m', partID='p', field='text', delta='late')
                    native.drain(record, set(), offsets, updates)
                self.assertEqual(offsets, {'p': 'ABC'})
                self.assertEqual(output, [('jingjiaagent_' + kind + '_delta', 'A'),
                                         ('jingjiaagent_' + kind + '_delta', 'BC'), (kind, 'ABC')])

    def test_subscription_reset_precedes_reconnected_deltas_on_eof_and_error(self):
        for failure in (None, OSError('disconnected')):
            with self.subTest(failure=failure):
                stopped, ready, updates = threading.Event(), threading.Event(), queue.Queue()
                first = dict(type='message.part.delta', properties=dict(delta='甲'))
                second = dict(type='message.part.delta', properties=dict(delta='丙'))
                class Response:
                    def __init__(self, event, terminal=False):
                        self.body = io.BytesIO(b'data: ' + json.dumps(event).encode() + b'\n\n')
                        self.terminal = terminal
                    def __enter__(self):
                        return self
                    def __exit__(self, *args):
                        return False
                    def readline(self, size=-1):
                        line = self.body.readline(size)
                        if line:
                            return line
                        if self.terminal:
                            stopped.set()
                        elif failure:
                            raise failure
                        return b''
                record = dict(password='local-test', port=1, directory='/tmp', session_id='s', run_id='r')
                with patch.object(bridge.LOCAL_HTTP, 'open', side_effect=[Response(first), Response(second, True)]), patch.object(stopped, 'wait', return_value=False):
                    bridge.audit_events(record, stopped, ready, updates)
                self.assertTrue(ready.is_set())
                self.assertEqual([updates.get_nowait(), updates.get_nowait(), updates.get_nowait()],
                                 [first, dict(type='jingjiaagent_subscription_reset'), second])
                self.assertTrue(updates.empty())

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
            bridge.publish_messages(record, [message], set(), offsets, previous=previous)
            snapshot.update(text='中文回答补齐',time=dict(start=1,end=2))
            emitted=set()
            bridge.publish_messages(record, [message], emitted, offsets, previous=previous)
            bridge.publish_messages(record, [message], emitted, offsets, previous=previous)
            self.assertEqual([item[1] for item in output], ['中文', '回答', '补齐', '中文回答补齐'])
            self.assertTrue(output[-1][2]['jingjiaagent_streamed'])
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
            self.assertEqual(output,['jingjiaagent_reasoning_delta'])

    def test_unfinished_parts_stream_suffixes_and_complete_once(self):
        record = dict(session_id='session')
        offsets, emitted, output = {}, set(), []
        part = dict(id='text1', type='text', text='中文', time=dict(start=1))
        message = dict(info=dict(id='message1', role='assistant'), parts=[part])
        with patch.object(bridge, 'emit', side_effect=lambda kind, session, p, **extra: output.append((kind, p.copy(), extra))):
            bridge.publish_messages(record, [message], emitted, offsets)
            bridge.publish_messages(record, [message], emitted, offsets)
            self.assertEqual([item[1]['text'] for item in output], ['中文'])
            part['text'] = '中文回答'
            bridge.publish_messages(record, [message], emitted, offsets)
            part['time']['end'] = 2
            bridge.publish_messages(record, [message], emitted, offsets)
            bridge.publish_messages(record, [message], emitted, offsets)
        self.assertEqual([item[0] for item in output], ['jingjiaagent_text_delta', 'jingjiaagent_text_delta', 'text'])
        self.assertEqual([item[1]['text'] for item in output], ['中文', '回答', '中文回答'])
        self.assertTrue(output[-1][2]['jingjiaagent_streamed'])

    def test_reasoning_old_snapshots_and_previous_turns(self):
        part = dict(id='thought1', type='reasoning', text='思考中', time={})
        message = dict(info=dict(id='m1', role='assistant'), parts=[part])
        offsets, emitted, output = {}, set(), []
        with patch.object(bridge, 'emit', side_effect=lambda kind, *args, **extra: output.append(kind)):
            bridge.publish_messages(dict(session_id='s'), [message], emitted, offsets, previous={'m1'})
            self.assertEqual(output, [])
            bridge.publish_messages(dict(session_id='s'), [message], emitted, offsets)
            part['text'] = '思考'
            bridge.publish_messages(dict(session_id='s'), [message], emitted, offsets)
            self.assertEqual(output, ['jingjiaagent_reasoning_delta'])
            part['text'] = '改写'
            with self.assertRaises(ValueError):
                bridge.publish_messages(dict(session_id='s'), [message], emitted, offsets)


if __name__=='__main__':
    unittest.main()
