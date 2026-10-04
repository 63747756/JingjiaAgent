import fcntl
import importlib.util
import json
import os
import pathlib
import tempfile
import threading
import time
import unittest

spec = importlib.util.spec_from_file_location('approval', pathlib.Path(__file__).with_name('approval.py'))
approval = importlib.util.module_from_spec(spec)
spec.loader.exec_module(approval)


class ApprovalTests(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        root = pathlib.Path(self.folder.name)
        approval.ROOT, approval.INTERACTIONS, approval.BRIDGE = root / 'policies', root / 'active', root / 'bridge.py'
        approval.INTERACTIONS.mkdir()
        approval.BRIDGE.write_text('POLICY_PROTOCOL = 1')
        self.req = dict(task_id='11111111-1111-1111-1111-111111111111', enabled=True, revision=1,
                        command_id='22222222-2222-2222-2222-222222222222')

    def tearDown(self):
        self.folder.cleanup()

    def test_replay_and_task_isolation(self):
        self.assertEqual(approval.apply(self.req), dict(success=True))
        self.assertEqual(approval.apply(self.req), dict(success=True))
        with self.assertRaises(ValueError):
            approval.apply(dict(self.req, enabled=False))
        other = dict(self.req, task_id='33333333-3333-3333-3333-333333333333', enabled=False)
        approval.apply(other)
        self.assertEqual(json.loads((approval.ROOT / (self.req['task_id'] + '.json')).read_text()), self.req)
        with self.assertRaises(ValueError):
            approval.apply(dict(self.req, task_id='../11111111-1111-1111-1111-111111111111'))

    def test_disable_waits_for_inflight_decision_and_bridge_observation(self):
        approval.apply(self.req)
        disabled = dict(self.req, enabled=False, revision=2, command_id='44444444-4444-4444-4444-444444444444')
        pointer = approval.INTERACTIONS / 'run-proof.json'
        pointer.write_text(json.dumps(dict(task_id=self.req['task_id'], pid=os.getpid())))
        proof = pointer.with_name('run-proof.policy.json')
        proof.write_text(json.dumps(dict(revision=1, enabled=True)))
        with open(approval.ROOT / (self.req['task_id'] + '.lock'), 'r+') as lock:
            fcntl.flock(lock, fcntl.LOCK_SH)
            result, errors = [], []
            def submit():
                try:
                    result.append(approval.apply(disabled))
                except Exception as error:
                    errors.append(error)
            worker = threading.Thread(target=submit)
            worker.start()
            time.sleep(.1)
            self.assertEqual(json.loads((approval.ROOT / (self.req['task_id'] + '.json')).read_text()), self.req)
            self.assertEqual(result, [])
            fcntl.flock(lock, fcntl.LOCK_UN)
        deadline = time.monotonic() + 2
        while json.loads((approval.ROOT / (self.req['task_id'] + '.json')).read_text()) != disabled:
            self.assertLess(time.monotonic(), deadline)
            time.sleep(.01)
        self.assertEqual(result, [])
        proof.write_text(json.dumps(dict(revision=2, enabled=False)))
        worker.join(2)
        self.assertFalse(worker.is_alive())
        self.assertEqual(errors, [])
        self.assertEqual(result, [dict(success=True)])

    def test_old_guest_cannot_claim_policy_support(self):
        approval.BRIDGE.write_text('old Guest')
        with self.assertRaises(ValueError):
            approval.apply(self.req)


if __name__ == '__main__':
    unittest.main()
