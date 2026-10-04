import importlib.util
import json
import os
import pathlib
import queue
import sys
import tempfile
import threading
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).parent))
import sdk_control

source = pathlib.Path(__file__).parents[3] / 'backend/pkg/runtimeadapter/guest/interaction.py'
spec = importlib.util.spec_from_file_location('sdk_interaction_test', source)
interaction = importlib.util.module_from_spec(spec)
spec.loader.exec_module(interaction)


class SDKControlTest(unittest.TestCase):
    def test_native_response_receipt_replay_and_expiry(self):
        with tempfile.TemporaryDirectory() as folder:
            sdk_control.ROOT = pathlib.Path(folder) / 'requests'
            sdk_control.POLICIES = pathlib.Path(folder) / 'policies'
            interaction.ROOT = sdk_control.ROOT
            original = {name: os.environ.get(name) for name in ['AGENT_COMPOSE_RUN_ID', 'MONKEYCODE_TASK_ID']}
            os.environ['AGENT_COMPOSE_RUN_ID'] = 'run-sdk-proof'
            os.environ['MONKEYCODE_TASK_ID'] = '12345678-1234-1234-1234-123456789012'
            try:
                events, replies = queue.Queue(), queue.Queue()
                thread = threading.Thread(target=lambda: replies.put(sdk_control.wait_decision(dict(provider='claude', native_id='native-call', kind='permission', title='Bash test', parent_pid=os.getpid()), events.put)))
                thread.start()
                metadata = events.get(timeout=3)['metadata']
                req = {key: metadata[key] for key in ['run_id','session_id','request_id','request_kind']}
                req.update(answers=[['拒绝']], cancelled=False)
                self.assertEqual(interaction.control(req), dict(success=True))
                thread.join(timeout=3)
                self.assertFalse(thread.is_alive())
                self.assertEqual(replies.get(timeout=1)['answers'], [['拒绝']])
                (sdk_control.ROOT / 'run-sdk-proof.json').unlink()
                self.assertEqual(interaction.control(req), dict(success=True))
                with self.assertRaises(ValueError): interaction.control({**req,'answers':[['允许一次']]})
                target = sdk_control.ROOT / 'run-sdk-proof' / (metadata['request_id'] + '.json')
                record = json.loads(target.read_text())
                record['uncertain'] = record.pop('receipt')
                target.write_text(json.dumps(record))
                self.assertEqual(interaction.control(req), dict(success=True))
            finally:
                for name,value in original.items():
                    if value is None: os.environ.pop(name,None)
                    else: os.environ[name]=value


if __name__ == '__main__': unittest.main()
