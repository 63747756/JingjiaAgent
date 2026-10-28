"""Regressions for SSE-first native runs and loss-safe snapshot reconciliation."""
import importlib.util
import io
import json
import pathlib
import queue
import threading
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    'event_bridge', pathlib.Path(__file__).with_name('opencode_bridge.py'))
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)


class NativeRunEventTests(unittest.TestCase):
    def setUp(self):
        self.record = dict(session_id='ses_current')
        self.output = []
        self.publisher = patch.object(
            bridge, 'emit', side_effect=lambda kind, session, part=None, **extra:
            self.output.append(dict(type=kind, sessionID=session, part=part or {}, **extra)))
        self.publisher.start()
        self.addCleanup(self.publisher.stop)
        self.boundary = self.info('msg_previous', created=100)
        self.current = bridge.NativeRunUpdates(self.record, self.boundary)

    def info(self, identifier='msg_answer', *, created=101, role='assistant', **extra):
        return dict(id=identifier, sessionID='ses_current', role=role,
                    time=dict(created=created), **extra)

    def part(self, identifier='prt_text', *, message='msg_answer', kind='text', **extra):
        return dict(id=identifier, messageID=message, sessionID='ses_current', type=kind, **extra)

    @staticmethod
    def message_event(info):
        return dict(type='message.updated', properties=dict(info=info))

    @staticmethod
    def part_event(part):
        return dict(type='message.part.updated', properties=dict(part=part))

    @staticmethod
    def delta(text, *, part='prt_text', message='msg_answer', session='ses_current'):
        return dict(type='message.part.delta', properties=dict(
            sessionID=session, messageID=message, partID=part, field='text', delta=text))

    def drain(self, *events):
        updates = queue.Queue()
        for event in events:
            updates.put(event)
        self.current.drain(updates)

    def events(self, kind):
        return [event for event in self.output if event['type'] == kind]

    def text(self):
        return ''.join(event['part']['text'] for event in self.events('jingjiaagent_text_delta'))

    def test_filters_other_session_old_prune_updates_and_user_text(self):
        foreign = dict(self.info(), sessionID='ses_other')
        self.drain(
            self.message_event(foreign),
            self.part_event(dict(self.part(text='foreign'), sessionID='ses_other')),
            self.message_event(self.info('msg_old', created=99)),
            self.part_event(self.part('prt_pruned', message='msg_old', kind='tool',
                                      tool='bash', state=dict(status='completed', output='old'))),
            self.message_event(self.info('msg_user', role='user')),
            self.part_event(self.part('prt_user', message='msg_user', text='private prompt')),
            self.delta('private delta', part='prt_user', message='msg_user'))
        self.assertEqual(self.output, [])
        self.assertEqual(self.current.pending_events, {})

    def test_part_before_info_is_published_when_role_arrives(self):
        self.drain(self.part_event(self.part(text='buffered')),
                   self.delta(' response'))
        self.assertEqual(self.output, [])
        self.drain(self.message_event(self.info()))
        self.assertEqual(self.text(), 'buffered response')
        self.assertEqual(self.current.pending_events, {})
        self.assertEqual(self.current.pending_bytes, 0)

    def test_tool_terminal_state_does_not_regress_or_emit_twice(self):
        self.drain(self.message_event(self.info()))
        for status in ('pending', 'running', 'completed', 'running', 'pending', 'completed'):
            self.drain(self.part_event(self.part('prt_tool', kind='tool', tool='bash',
                       state=dict(status=status, input=dict(command='true'), output='done'))))
        states = [event['part']['state']['status'] for event in self.events('tool_use')]
        self.assertEqual(states, ['pending', 'running', 'completed'])
        self.assertEqual(self.current.tool_states['prt_tool'], 'completed')
        self.drain(self.part_event(self.part('prt_question', kind='tool', tool='question',
                                            state=dict(status='completed'))))
        self.assertEqual(len(self.events('tool_use')), 3)

    def test_step_usage_is_emitted_once_across_sse_and_snapshot(self):
        info = self.info()
        step = self.part('prt_finish', kind='step-finish', tokens=dict(input=40, output=7), cost=0.1)
        self.drain(self.message_event(info), self.part_event(step),
                   self.message_event(dict(info, tokens=dict(input=40, output=7), cost=0.1)))
        self.current.snapshot(dict(info=info, parts=[step]))
        self.assertEqual(len(self.events('step_finish')), 1)
        self.assertEqual(self.events('step_finish')[0]['part']['tokens']['output'], 7)

    def test_final_text_snapshot_rejects_late_delta_in_same_or_later_batch(self):
        expected = 'SSE_FINAL_ROUND_1'
        for same_batch in (True, False):
            with self.subTest(same_batch=same_batch):
                self.output.clear()
                self.current = bridge.NativeRunUpdates(self.record, self.boundary)
                events = [self.message_event(self.info()),
                          self.part_event(self.part(text='')),
                          self.part_event(self.part(text=expected, time=dict(start=101, end=102)))]
                if same_batch:
                    self.drain(*events, self.delta(expected))
                else:
                    self.drain(*events)
                    self.drain(self.delta(expected))
                self.assertEqual(self.text(), expected)
                self.assertEqual(len(self.events('text')), 1)
                self.assertEqual(self.events('text')[0]['part']['text'], expected)

    def test_native_accumulator_ignores_delta_after_completed_part(self):
        native, offsets = bridge.NativeTextUpdates(), {}
        expected = 'completed response'
        for event in [self.message_event(self.info()),
                      self.part_event(self.part(text=expected, time=dict(start=101, end=102))),
                      self.delta(expected)]:
            native.consume(self.record, set(), offsets, event)
        self.assertEqual(self.text(), expected)
        self.assertEqual(offsets['prt_text'], expected)

    def test_stale_nonterminal_snapshot_cannot_reopen_completed_text(self):
        expected = 'SSE_FINAL_ROUND_1'
        self.drain(self.message_event(self.info()),
                   self.part_event(self.part(text=expected, time=dict(start=101, end=102))))
        self.drain(self.part_event(self.part(text=expected, time=dict(start=101))),
                   self.delta(expected))
        self.assertEqual(self.text(), expected)
        self.assertEqual(len(self.events('text')), 1)
        self.assertEqual(self.current.native.parts['prt_text']['time']['end'], 102)

    def test_compaction_summary_hides_text_but_preserves_usage_and_errors(self):
        summary = self.info(summary=True)
        self.drain(self.message_event(summary), self.part_event(self.part(text='internal summary')),
                   self.part_event(self.part('prt_reason', kind='reasoning', text='internal reasoning')),
                   self.part_event(self.part('prt_finish', kind='step-finish', tokens=dict(input=9))))
        self.assertEqual(self.text(), '')
        self.assertEqual(self.events('jingjiaagent_reasoning_delta'), [])
        self.assertEqual(len(self.events('step_finish')), 1)
        with self.assertRaisesRegex(ValueError, 'native Agent request failed'):
            self.drain(self.message_event(dict(summary, error=dict(name='APIError'))))

    def test_repeated_gaps_keep_snapshot_mode_for_existing_and_new_parts(self):
        self.drain(self.message_event(self.info()), self.part_event(self.part(text='A')),
                   self.delta('B'), dict(type='jingjiaagent_subscription_reset'))
        self.assertEqual(self.text(), 'AB')
        self.current.snapshot(dict(info=self.info(), parts=[self.part(text='ABC')]))
        self.drain(self.delta('C'), dict(type='jingjiaagent_subscription_reset'),
                   self.message_event(self.info('msg_next', created=102)),
                   self.part_event(self.part('prt_next', message='msg_next', text='D')),
                   self.delta('E', part='prt_next', message='msg_next'))
        self.current.snapshot(dict(info=self.info('msg_next', created=102), parts=[
            self.part('prt_next', message='msg_next', text='DE', time=dict(end=3))]))
        self.drain(self.delta('E', part='prt_next', message='msg_next'))
        self.assertEqual(self.text(), 'ABCDE')
        self.assertTrue(self.current.native.snapshot_only)
        self.assertEqual(len(self.events('text')), 1)

    def test_gap_repairs_step_start_before_publishing_its_finish(self):
        info = self.info()
        start = self.part('prt_start', kind='step-start')
        finish = self.part('prt_finish', kind='step-finish', tokens=dict(input=40, output=7))
        self.drain(self.message_event(info), dict(type='jingjiaagent_subscription_reset'),
                   self.part_event(finish))
        with patch.object(bridge, 'iter_turn_messages', return_value=iter([
                dict(info=info, parts=[start, finish])])):
            self.current.reconcile()
        self.assertEqual([event['type'] for event in self.output], ['step_start', 'step_finish'])

    def test_unknown_message_uses_single_message_lookup_and_ignores_late_delta(self):
        self.drain(self.part_event(self.part(text='A')), self.delta('B'))
        snapshot = dict(info=self.info(), parts=[self.part(text='AB')])
        with patch.object(bridge, 'read_message', return_value=snapshot) as read, \
                patch.object(bridge, 'iter_turn_messages', return_value=iter([snapshot])) as history:
            self.current.resolve_unknown()
            self.assertEqual(self.text(), '')  # Wait for the ordered turn, not a possibly later step.
            history.assert_not_called()
            self.current.reconcile()
        read.assert_called_once_with(self.record, 'msg_answer')
        history.assert_called_once_with(self.record, self.boundary['id'], include_users=True)
        self.drain(self.delta('B'))
        self.assertEqual(self.text(), 'AB')
        self.assertTrue(self.current.native.snapshot_only)

    def test_unknown_old_prune_and_user_do_not_disable_live_streaming(self):
        self.drain(self.part_event(self.part('prt_old', message='msg_old', kind='tool',
                                           tool='bash', state=dict(status='completed'))),
                   self.part_event(self.part('prt_user', message='msg_user', text='private prompt')))
        snapshots = [dict(info=self.info('msg_old', created=99), parts=[]),
                     dict(info=self.info('msg_user', role='user'), parts=[])]
        with patch.object(bridge, 'read_message', side_effect=snapshots) as read, \
                patch.object(bridge, 'iter_turn_messages') as history:
            self.current.resolve_unknown()
        self.assertEqual(read.call_count, 2)
        history.assert_not_called()
        self.assertFalse(self.current.native.snapshot_only)
        self.assertFalse(self.current.needs_reconcile)
        self.assertEqual(self.output, [])

    def test_missing_delta_base_triggers_gap_without_guessing_content(self):
        self.drain(self.message_event(self.info()), self.delta('unknown base'))
        self.assertEqual(self.text(), '')
        self.assertTrue(self.current.native.snapshot_only)
        self.assertTrue(self.current.needs_reconcile)
        self.current.snapshot(dict(info=self.info(), parts=[self.part(text='complete')]))
        self.assertEqual(self.text(), 'complete')

    def test_pending_payload_budget_triggers_reconciliation(self):
        with patch.object(bridge, 'MAX_PENDING_BYTES', 128):
            self.drain(self.part_event(self.part(text='x' * 256)))
        self.assertEqual(self.text(), '')
        self.assertLessEqual(self.current.pending_bytes, 128)
        self.assertTrue(self.current.native.snapshot_only)
        self.assertTrue(self.current.needs_reconcile)

    def test_reconcile_follows_boundary_and_accepts_compaction_replay_children(self):
        messages = [
            dict(info=self.info('msg_user', role='user'), parts=[
                self.part('prt_prompt', message='msg_user', text='prompt')]),
            dict(info=self.info('msg_summary', created=102, summary=True, parentID='msg_compact'),
                 parts=[self.part('prt_summary', message='msg_summary', text='summary')]),
            dict(info=self.info('msg_replay', created=103, role='user'), parts=[]),
            dict(info=self.info('msg_replayed_answer', created=104, parentID='msg_replay'),
                 parts=[self.part('prt_answer', message='msg_replayed_answer', text='final',
                                  time=dict(end=106))]),
        ]
        with patch.object(bridge, 'iter_turn_messages', return_value=iter(messages)) as history:
            self.current.reconcile()
        history.assert_called_once_with(self.record, self.boundary['id'], include_users=True)
        self.assertEqual(self.text(), 'final')
        self.assertEqual(len(self.events('text')), 1)
        self.assertFalse(self.current.needs_reconcile)

    def test_current_errors_fail_but_foreign_or_old_errors_do_not(self):
        self.drain(dict(type='session.error', properties=dict(
            sessionID='ses_other', error=dict(name='APIError'))),
            self.message_event(self.info('msg_old', created=99, error=dict(name='APIError'))))
        with self.assertRaisesRegex(ValueError, 'native Agent request failed'):
            self.current.snapshot(dict(info=self.info(error=dict(name='APIError')), parts=[]))
        with self.assertRaisesRegex(ValueError, 'native Agent request failed'):
            self.drain(dict(type='session.error', properties=dict(
                sessionID='ses_current', error=dict(name='APIError'))))

    def test_context_overflow_event_allows_native_compaction_to_recover(self):
        self.drain(dict(type='session.error', properties=dict(
            sessionID='ses_current', error=dict(name='ContextOverflowError'))))
        self.drain(self.message_event(self.info('msg_summary', summary=True)),
                   self.part_event(self.part('prt_summary', message='msg_summary',
                                             text='internal recovery summary', time=dict(end=102))),
                   self.message_event(self.info('msg_recovered', created=103)),
                   self.part_event(self.part('prt_recovered', message='msg_recovered',
                                             text='recovered reply', time=dict(end=104))))
        self.assertEqual(self.text(), 'recovered reply')
        self.assertEqual(len(self.events('text')), 1)

    def test_unrecovered_context_overflow_in_message_still_fails(self):
        for name in ('ContextOverflowError', 'APIError'):
            with self.subTest(final_error=name):
                self.drain(dict(type='session.error', properties=dict(
                    sessionID='ses_current', error=dict(name='ContextOverflowError'))))
                # Native persists the overflow when automatic compaction is
                # disabled, or another error if its recovery request fails.
                with self.assertRaisesRegex(ValueError, 'native Agent request failed'):
                    self.current.snapshot(dict(info=self.info(error=dict(name=name)), parts=[]))

    def test_snapshot_rejects_cross_message_part(self):
        with self.assertRaisesRegex(ValueError, 'belongs to another message'):
            self.current.snapshot(dict(info=self.info(), parts=[
                self.part(message='msg_other', text='wrong message')]))
        self.assertEqual(self.text(), '')


class NativeSSEFrameTests(unittest.TestCase):
    def read(self, body):
        return list(bridge.sse_events(io.BytesIO(body), threading.Event()))

    def test_multiline_json_crlf_and_heartbeat(self):
        body = (b': heartbeat\r\n\r\ndata: {"type":"message.updated",\r\n'
                b'data: "properties":{"info":{"id":"msg_one"}}}\r\n\r\n')
        events = self.read(body)
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0]['properties']['info']['id'], 'msg_one')

    def test_oversized_line_and_multiline_frame_emit_reset_then_resume(self):
        valid = b'data: {"type":"ok"}\n\n'
        oversized_line = b'data: ' + b'x' * 100 + b'\n\n'
        oversized_frame = b'data: ' + b'x' * 40 + b'\ndata: ' + b'x' * 40 + b'\n\n'
        with patch.object(bridge, 'MAX_SSE_BYTES', 64):
            events = self.read(oversized_line + valid + oversized_frame + valid)
        self.assertEqual([event['type'] for event in events], [
            'jingjiaagent_subscription_reset', 'ok', 'jingjiaagent_subscription_reset', 'ok'])

    def test_truncated_frame_is_not_published_and_reads_are_bounded(self):
        class BoundedResponse(io.BytesIO):
            def readline(self, size=-1):
                if size != bridge.MAX_SSE_BYTES + 1:
                    raise AssertionError('SSE reads must be bounded')
                return super().readline(size)

        response = BoundedResponse(b'data: {"type":"partial"}')
        self.assertEqual(list(bridge.sse_events(response, threading.Event())), [])


if __name__ == '__main__':
    unittest.main()
