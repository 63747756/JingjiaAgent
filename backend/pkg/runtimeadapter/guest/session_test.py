import importlib.util
import json
import pathlib
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('session', pathlib.Path(__file__).with_name('session.py'))
session = importlib.util.module_from_spec(spec)
spec.loader.exec_module(session)


class SessionControlTest(unittest.TestCase):
    def test_other_provider_reset_keeps_files_and_separates_receipts(self):
        for provider in ["codex", "claude"]:
            with self.subTest(provider=provider), tempfile.TemporaryDirectory() as directory:
                session.ROOT=pathlib.Path(directory)
                target=session.ROOT / ("agents/providers/"+provider+".json")
                session.atomic_json(target,dict(provider=provider,threadId="native-session"))
                request=dict(op="restart",provider=provider,command_id="12345678-1234-1234-1234-123456789012",load_session=True)
                self.assertEqual(session.control(request)["session_id"],"native-session")
                with self.assertRaises(ValueError): session.control({**request,"provider":"opencode"})
                request.update(command_id="22345678-1234-1234-1234-123456789012",load_session=False)
                self.assertEqual(session.control(request)["session_id"],"")
                self.assertEqual(json.loads(target.read_text())["threadId"],"")

    def test_keep_clear_replay_and_validation(self):
        with tempfile.TemporaryDirectory() as directory:
            session.ROOT = pathlib.Path(directory)
            target = session.ROOT / 'agents/providers/opencode.json'
            session.atomic_json(target, dict(provider='opencode', threadId='ses_original'))
            def request(key, keep):
                return dict(op='restart', provider='opencode', command_id=key, load_session=keep)
            keep = request('12345678-1234-1234-1234-123456789012', True)
            self.assertEqual(session.control(keep)['session_id'], 'ses_original')
            clear = request('22345678-1234-1234-1234-123456789012', False)
            self.assertEqual(session.control(clear)['session_id'], '')
            session.atomic_json(target, dict(provider='opencode', threadId='ses_new'))
            # Retry must not erase a newer native session.
            self.assertEqual(session.control(clear)['session_id'], '')
            self.assertEqual(json.loads(target.read_text())['threadId'], 'ses_new')
            self.assertEqual(target.stat().st_mode & 0o777, 0o600)
            with self.assertRaises(ValueError): session.control({**clear, 'load_session': True})
            with self.assertRaises(ValueError): session.control({**clear, 'command_id': '../../escape'})
            with self.assertRaises(ValueError): session.control({**keep, 'provider': '../claude'})


if __name__ == '__main__': unittest.main()
