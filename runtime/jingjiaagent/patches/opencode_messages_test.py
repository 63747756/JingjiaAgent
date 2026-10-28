"""Streaming message-reader contracts, independent of Docker or an AI model."""
import io
import json
import pathlib
import sys
import unittest
import urllib.error
import urllib.parse
from unittest.mock import patch

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import opencode_messages as messages


RECORD = {'port': 1234, 'password': 'private', 'directory': '/workspace', 'session_id': 'ses_1'}


def message(identifier, role='assistant', text='hello', *, session='ses_1', error=None):
    info = {'id': identifier, 'sessionID': session, 'role': role, 'time': {'created': 123}}
    if error is not None:
        info['error'] = error
    return {'info': info, 'parts': [{'id': 'prt_' + identifier, 'type': 'text', 'text': text}]}


def numeric_message_wire(literal, *, role='assistant', array=False):
    """Put the actual JSON token in both retained metadata and tool arguments."""
    item = message('numeric', role)
    item['info']['numeric'] = '__NUMBER__'
    item['parts'] = [{'id': 'prt_number', 'type': 'tool', 'tool': 'calculator',
                      'state': {'status': 'completed', 'input': {'value': '__NUMBER__'}}}]
    return json.dumps([item] if array else item).replace('"__NUMBER__"', literal).encode()


NUMBER_CASES = (
    ('100000000000000000000', 10 ** 20),
    ('-100000000000000000000', -(10 ** 20)),
    ('18446744073709551617', 2 ** 64 + 1),
    ('-18446744073709551617', -(2 ** 64 + 1)),
    ('1.25', 1.25), ('-1.25', -1.25),
    ('6.25e-12', 6.25e-12), ('-6.25E+40', -6.25e40),
    ('1e20', 1e20), ('-1e20', -1e20),
)


class Response(io.BytesIO):
    def __init__(self, value, cursor=None, *, raw=False):
        super().__init__(value if raw else json.dumps(value, ensure_ascii=False).encode())
        self.headers = {} if cursor is None else {'X-Next-Cursor': cursor}
        self.read_sizes = []

    def read(self, size=-1):
        if size < 0:
            raise AssertionError('response must not be read without a bound')
        self.read_sizes.append(size)
        return super().read(size)


class MessageReaderTests(unittest.TestCase):
    def assert_serializable_number(self, value, expected):
        self.assertEqual(value, expected)
        self.assertIs(type(value), type(expected))
        self.assertEqual(json.loads(json.dumps(value, allow_nan=False)), expected)

    def test_recent_boundary_accepts_large_integers_and_decimal_metadata(self):
        for literal, expected in NUMBER_CASES:
            with self.subTest(literal=literal), patch.object(messages, 'open_response',
                    return_value=Response(numeric_message_wire(literal, role='user', array=True), raw=True)):
                info = messages.recent_boundary(RECORD)
                self.assert_serializable_number(info['numeric'], expected)

    def test_single_message_retains_json_serializable_numeric_tool_arguments(self):
        for literal, expected in NUMBER_CASES:
            with self.subTest(literal=literal), patch.object(messages, 'open_response',
                    return_value=Response(numeric_message_wire(literal), raw=True)):
                item = messages.read_message(RECORD, 'numeric')
                self.assert_serializable_number(item['info']['numeric'], expected)
                self.assert_serializable_number(item['parts'][0]['state']['input']['value'], expected)

    def test_submit_preserves_numeric_metadata_while_discarding_tool_arguments(self):
        for literal, expected in NUMBER_CASES:
            with self.subTest(literal=literal), patch.object(messages, 'open_response',
                    return_value=Response(numeric_message_wire(literal), raw=True)):
                info = messages.submit_prompt(RECORD, {'parts': []})
                self.assert_serializable_number(info['numeric'], expected)

    def test_reconcile_spill_round_trip_preserves_large_integers_and_decimal_numbers(self):
        for literal, expected in NUMBER_CASES:
            pages = [Response(numeric_message_wire(literal, array=True), cursor='older', raw=True),
                     Response([message('boundary')])]
            with self.subTest(literal=literal), patch.object(messages, 'open_response', side_effect=pages):
                items = list(messages.iter_turn_messages(RECORD, 'boundary'))
                self.assertEqual(len(items), 1)
                self.assert_serializable_number(items[0]['info']['numeric'], expected)
                self.assert_serializable_number(items[0]['parts'][0]['state']['input']['value'], expected)

    def test_non_finite_numbers_are_rejected_even_in_discarded_parts(self):
        for literal in ('NaN', 'Infinity', '-Infinity', '1e400', '-1e400'):
            item = message('numeric', 'user')
            item['parts'][0]['numeric'] = '__NUMBER__'
            wire = json.dumps([item]).replace('"__NUMBER__"', literal).encode()
            with self.subTest(literal=literal), patch.object(messages, 'open_response',
                    return_value=Response(wire, raw=True)):
                with self.assertRaises(ValueError):
                    messages.recent_boundary(RECORD)

    def test_large_integer_size_counts_toward_retained_metadata_budget(self):
        with patch.object(messages, 'MAX_METADATA_BYTES', 2048):
            with patch.object(messages, 'open_response',
                    return_value=Response(numeric_message_wire('1', array=True), raw=True)):
                self.assertEqual(messages.recent_boundary(RECORD)['numeric'], 1)
            with patch.object(messages, 'open_response',
                    return_value=Response(numeric_message_wire('9' * 4000, array=True), raw=True)):
                with self.assertRaisesRegex(ValueError, 'retained data budget'):
                    messages.recent_boundary(RECORD)

    def test_recent_boundary_returns_metadata_only_and_uses_limit_one(self):
        item = message('boundary', text='sensitive old text')
        response = Response([item])
        with patch.object(messages, 'open_response', return_value=response) as opened:
            self.assertEqual(messages.recent_boundary(RECORD), item['info'])
        self.assertIn('limit=1', opened.call_args.args[1])
        self.assertTrue(response.closed)
        self.assertTrue(all(size <= 65536 for size in response.read_sizes))

    def test_recent_empty_session(self):
        with patch.object(messages, 'open_response', return_value=Response([])):
            self.assertIsNone(messages.recent_boundary(RECORD))

    def test_recent_boundary_discards_seventeen_mib_user(self):
        item = message('big', 'user', 'x' * (17 << 20))
        with patch.object(messages, 'open_response', return_value=Response([item])):
            self.assertEqual(messages.recent_boundary(RECORD)['id'], 'big')

    def test_pages_replayed_in_native_chronological_order_not_id_order(self):
        # Newest page first; each page itself is oldest first. IDs deliberately
        # have no lexicographic relationship to chronology.
        pages = [Response([message('aaa'), message('zzz')], 'older&one'),
                 Response([message('boundary'), message('uuu', 'user'), message('mmm')])]
        with patch.object(messages, 'open_response', side_effect=pages) as opened:
            result = list(messages.iter_turn_messages(RECORD, 'boundary', include_users=True))
        self.assertEqual([item['info']['id'] for item in result], ['uuu', 'mmm', 'aaa', 'zzz'])
        self.assertEqual(result[0]['parts'], [])
        query = urllib.parse.parse_qs(urllib.parse.urlsplit(opened.call_args_list[1].args[1]).query)
        self.assertEqual(query['before'], ['older&one'])
        self.assertEqual(query['limit'], ['50'])

    def test_boundary_excludes_older_parts_before_any_retention_budget(self):
        page = [message('old', text='x' * 10000), message('boundary'), message('new', text='small')]
        with patch.object(messages, 'MAX_MESSAGE_BYTES', 1024), \
             patch.object(messages, 'open_response', return_value=Response(page)):
            result = list(messages.iter_turn_messages(RECORD, 'boundary'))
        self.assertEqual([item['info']['id'] for item in result], ['new'])

    def test_cumulative_history_and_user_page_exceed_sixteen_mib(self):
        huge = message('new-user', 'user', '用' * (6 << 20))
        pages = [Response([message('last')], 'older'),
                 Response([message('boundary'), huge, message('first')])]
        with patch.object(messages, 'open_response', side_effect=pages):
            result = list(messages.iter_turn_messages(RECORD, 'boundary'))
        self.assertEqual([item['info']['id'] for item in result], ['first', 'last'])

    def test_missing_boundary_does_not_emit_partial_results(self):
        with patch.object(messages, 'open_response', return_value=Response([message('new')])):
            iterator = messages.iter_turn_messages(RECORD, 'missing')
            with self.assertRaisesRegex(ValueError, 'boundary is missing'):
                next(iterator)

    def test_cursor_cycle_rejected(self):
        pages = [Response([message('new')], 'same'), Response([message('old')], 'same')]
        with patch.object(messages, 'open_response', side_effect=pages):
            with self.assertRaisesRegex(ValueError, 'cursor repeated'):
                list(messages.iter_turn_messages(RECORD, 'missing'))

    def test_overlapping_pages_rejected(self):
        pages = [Response([message('new')], 'older'), Response([message('new')])]
        with patch.object(messages, 'open_response', side_effect=pages):
            with self.assertRaisesRegex(ValueError, 'overlap'):
                list(messages.iter_turn_messages(RECORD, 'missing'))

    def test_empty_page_with_cursor_rejected(self):
        with patch.object(messages, 'open_response', return_value=Response([], 'older')):
            with self.assertRaisesRegex(ValueError, 'empty message page'):
                list(messages.iter_turn_messages(RECORD, None))

    def test_cross_session_rejected(self):
        with patch.object(messages, 'open_response', return_value=Response([message('wrong', session='ses_other')])):
            with self.assertRaisesRegex(ValueError, 'another session'):
                list(messages.iter_turn_messages(RECORD, None))

    def test_new_session_retains_all_assistants_and_skips_user(self):
        page = [message('input', 'user'), message('reply')]
        with patch.object(messages, 'open_response', return_value=Response(page)):
            result = list(messages.iter_turn_messages(RECORD, None))
        self.assertEqual([item['info']['id'] for item in result], ['reply'])

    def test_invalid_json_shapes_and_trailing_data_rejected(self):
        values = [b'{', b'[]{}', b'[12]', b'[{"info":null,"parts":[]}]',
                  b'[{"parts":[],"info":{}}]', b'[{"info":{},"info":{},"parts":[]}]']
        for value in values:
            with self.subTest(value=value):
                with patch.object(messages, 'open_response', return_value=Response(value, raw=True)):
                    with self.assertRaises(ValueError):
                        list(messages.iter_turn_messages(RECORD, None))

    def test_http_error_preserved_and_not_success(self):
        error = urllib.error.HTTPError('http://127.0.0.1', 500, 'failed', {}, None)
        with patch.object(messages, 'open_response', side_effect=error):
            with self.assertRaises(urllib.error.HTTPError):
                list(messages.iter_turn_messages(RECORD, None))

    def test_ignored_pagination_rejected(self):
        with patch.object(messages, 'open_response', return_value=Response([message('a'), message('b')])):
            with self.assertRaisesRegex(ValueError, 'ignored message pagination'):
                messages.recent_boundary(RECORD)

    def test_page_budget_rejected(self):
        with patch.object(messages, 'MAX_PAGES', 1), \
             patch.object(messages, 'open_response', return_value=Response([message('a')], 'next')):
            with self.assertRaisesRegex(ValueError, 'page budget'):
                list(messages.iter_turn_messages(RECORD, None))

    def test_wire_metadata_and_retention_budgets_are_explicit(self):
        for setting, budget, text in [('MAX_RESPONSE_BYTES', 64, 'a'),
                                      ('MAX_METADATA_BYTES', 64, 'a'),
                                      ('MAX_MESSAGE_BYTES', 64, 'a' * 1024),
                                      ('MAX_TURN_BYTES', 64, 'a' * 1024)]:
            with self.subTest(setting=setting):
                with patch.object(messages, setting, budget), \
                     patch.object(messages, 'open_response', return_value=Response([message('a', text=text)])):
                    with self.assertRaisesRegex(ValueError, 'budget'):
                        list(messages.iter_turn_messages(RECORD, None))

    def test_single_message_retains_assistant_and_discards_user_parts(self):
        for role in ('assistant', 'user'):
            with self.subTest(role=role):
                item = message('one', role)
                with patch.object(messages, 'open_response', return_value=Response(item)):
                    result = messages.read_message(RECORD, 'one')
                self.assertEqual(result['parts'], item['parts'] if role == 'assistant' else [])

    def test_single_message_requires_requested_id(self):
        with patch.object(messages, 'open_response', return_value=Response(message('different'))):
            with self.assertRaisesRegex(ValueError, 'different message'):
                messages.read_message(RECORD, 'wanted')

    def test_submit_large_response_keeps_only_info(self):
        item = message('reply', text='x' * (17 << 20))
        with patch.object(messages, 'open_response', return_value=Response(item)) as opened:
            result = messages.submit_prompt(RECORD, {'parts': []})
        self.assertEqual(result, item['info'])
        self.assertEqual(opened.call_args.kwargs['body'], {'parts': []})

    def test_submit_native_error_cannot_be_misread_as_success(self):
        item = message('reply', error={'name': 'APIError', 'data': {'message': 'provider failed'}})
        with patch.object(messages, 'open_response', return_value=Response(item)):
            with self.assertRaisesRegex(ValueError, '^native Agent returned an execution error$'):
                messages.submit_prompt(RECORD, {'parts': []})

    def test_submit_preserves_abort_for_caller_cancellation_handling(self):
        item = message('reply', error={'name': 'MessageAbortedError', 'data': {'message': 'aborted'}})
        with patch.object(messages, 'open_response', return_value=Response(item)):
            result = messages.submit_prompt(RECORD, {'parts': []})
        self.assertEqual(result['error'], item['info']['error'])

    def test_submit_refuses_successful_http_with_wrong_or_missing_body(self):
        for value in [message('input', 'user'), message('reply', session='another'), {}, []]:
            with self.subTest(value=value):
                with patch.object(messages, 'open_response', return_value=Response(value)):
                    with self.assertRaises(ValueError):
                        messages.submit_prompt(RECORD, {'parts': []})

    def test_spill_directory_removed_when_iteration_finishes_or_closes(self):
        captured = []
        original = messages.tempfile.TemporaryDirectory

        def tracked(*args, **kwargs):
            result = original(*args, **kwargs)
            captured.append(pathlib.Path(result.name))
            return result

        with patch.object(messages.tempfile, 'TemporaryDirectory', side_effect=tracked), \
             patch.object(messages, 'open_response', return_value=Response([message('a'), message('b')])):
            iterator = messages.iter_turn_messages(RECORD, None)
            next(iterator)
            self.assertTrue(captured[0].exists())
            iterator.close()
        self.assertFalse(captured[0].exists())


if __name__ == '__main__':
    unittest.main()
