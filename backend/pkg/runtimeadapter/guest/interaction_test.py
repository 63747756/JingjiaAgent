import hashlib
import importlib.util
import json
import pathlib
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('interaction', pathlib.Path(__file__).with_name('interaction.py'))
interaction = importlib.util.module_from_spec(spec)
spec.loader.exec_module(interaction)


class InteractionTests(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        interaction.ROOT = pathlib.Path(self.folder.name)
        self.req = dict(run_id='run-proof', session_id='ses-proof', request_id='que_proof',
                        request_kind='question', answers=[['A']], cancelled=False)
        self.target = interaction.ROOT / 'run-proof' / 'que_proof.json'
        self.target.parent.mkdir()
        self.native = {key: self.req[key] for key in ('run_id', 'session_id', 'request_id', 'request_kind')}
        self.save(dict(request=self.native))

    def tearDown(self):
        self.folder.cleanup()

    def save(self, record):
        self.target.write_text(json.dumps(record))

    def fingerprint(self):
        return hashlib.sha256(json.dumps(self.req, sort_keys=True, separators=(',', ':')).encode()).hexdigest()

    def test_completed_replay_needs_no_running_server(self):
        self.save(dict(request=self.native, receipt=self.fingerprint()))
        self.assertEqual(interaction.control(self.req), dict(success=True))
        with self.assertRaises(ValueError):
            interaction.control(dict(self.req, answers=[['B']]))

    def test_native_proof_reconciles_lost_response_after_server_exits(self):
        self.save(dict(request=self.native, uncertain=self.fingerprint()))
        proof = self.target.with_name('que_proof.native.json')
        proof.write_text(json.dumps(dict(kind='question.replied', data=dict(sessionID='ses-proof', requestID='que_proof', answers=[['A']]))))
        self.assertEqual(interaction.control(self.req), dict(success=True))
        self.assertEqual(json.loads(self.target.read_text())['receipt'], self.fingerprint())

    def test_missing_or_wrong_native_proof_stays_uncertain(self):
        self.save(dict(request=self.native, uncertain=self.fingerprint()))
        with self.assertRaises(RuntimeError):
            interaction.control(self.req)
        proof = self.target.with_name('que_proof.native.json')
        proof.write_text(json.dumps(dict(kind='question.replied', data=dict(sessionID='ses-other', requestID='que_proof', answers=[['A']]))))
        with self.assertRaises(RuntimeError):
            interaction.control(self.req)
        with self.assertRaises(ValueError):
            interaction.control(dict(self.req, answers=[['B']]))

    def test_request_correlations_and_path_are_required(self):
        for mutation in [dict(session_id='ses-other'), dict(request_kind='permission'), dict(request_id='../que_proof'), dict(run_id='../run-proof')]:
            with self.assertRaises(ValueError):
                interaction.control(dict(self.req, **mutation))

    def test_permission_proof_matches_once_or_reject_exactly(self):
        self.req.update(request_id='per_proof', request_kind='permission', answers=[['允许一次']])
        self.target = self.target.with_name('per_proof.json')
        self.native = {key: self.req[key] for key in ('run_id', 'session_id', 'request_id', 'request_kind')}
        self.save(dict(request=self.native, uncertain=self.fingerprint()))
        proof = self.target.with_name('per_proof.native.json')
        proof.write_text(json.dumps(dict(kind='permission.replied', data=dict(sessionID='ses-proof', requestID='per_proof', reply='reject'))))
        with self.assertRaises(RuntimeError):
            interaction.control(self.req)
        proof.write_text(json.dumps(dict(kind='permission.replied', data=dict(sessionID='ses-proof', requestID='per_proof', reply='once'))))
        self.assertEqual(interaction.control(self.req), dict(success=True))


if __name__ == '__main__':
    unittest.main()
