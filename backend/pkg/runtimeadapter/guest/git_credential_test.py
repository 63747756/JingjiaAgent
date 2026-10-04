import importlib.util
import pathlib
import unittest

spec = importlib.util.spec_from_file_location('git_credential', pathlib.Path(__file__).with_name('git_credential.py'))
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)


class CredentialTargets(unittest.TestCase):
    def test_other_repositories_never_receive_a_request(self):
        config = {'repo':'https://git.example.invalid/team/repo.git'}
        for values in [
            {'protocol':'https','host':'git.example.invalid','path':'team/other.git'},
            {'protocol':'http','host':'git.example.invalid','path':'team/repo.git'},
            {'protocol':'https','host':'other.example.invalid','path':'team/repo.git'},
            {'protocol':'https','host':'git.example.invalid','path':'team/repo.git?leak=1'},
        ]:
            self.assertIsNone(helper.get(config, values))
    def test_equivalent_git_suffix_and_unicode(self):
        self.assertTrue(helper.target_matches('https://git.example.invalid/team/中文.git',
            {'protocol':'https','host':'git.example.invalid','path':'team/%E4%B8%AD%E6%96%87'}))


if __name__ == '__main__':unittest.main()
