import copy
import json
import pathlib
import tempfile
import unittest
from runtime_mount_mapping import VARIABLE, ensure_runtime_mount_mapping, trusted_sandbox_root


def fixture():
    source = '/var/lib/docker/volumes/work-test_runtime-data/_data'
    container = {'labels': {'com.docker.compose.project': 'work-test', 'com.docker.compose.service': 'runtime'},
        'mounts': [{'Type': 'volume', 'Name': 'work-test_runtime-data', 'Driver': 'local', 'RW': True,
                    'Source': source, 'Destination': '/data'}]}
    volume = {'Name': 'work-test_runtime-data', 'Driver': 'local', 'Scope': 'local', 'Mountpoint': source,
        'Options': None, 'Labels': {'com.docker.compose.project': 'work-test', 'com.docker.compose.volume': 'runtime-data'}}
    return container, volume


class RuntimeMountMappingTest(unittest.TestCase):
    def test_ownership_mount_type_and_source_fail_closed(self):
        original, volume = fixture()
        self.assertEqual(trusted_sandbox_root('work-test', original, volume), volume['Mountpoint'] + '/sandboxes')
        for field, bad in [('Type', 'bind'), ('Type', 'tmpfs'), ('Name', 'other_runtime-data'), ('RW', False),
                           ('Source', '/'), ('Source', '/var/lib/../private'), ('Source', '/tmp/$secret'),
                           ('Source', '/tmp/a\nb'), ('Source', None), ('Driver', 'nfs')]:
            container = copy.deepcopy(original)
            container['mounts'][0][field] = bad
            with self.subTest(field=field, bad=bad), self.assertRaises(ValueError):
                trusted_sandbox_root('work-test', container, volume)
        for field, bad in [('Mountpoint', '/other'), ('Driver', 'nfs'), ('Scope', 'global'), ('Options', {'device': '/private'})]:
            other = copy.deepcopy(volume); other[field] = bad
            with self.subTest(field=field), self.assertRaises(ValueError):
                trusted_sandbox_root('work-test', original, other)
        for item in (original, volume):
            other = copy.deepcopy(item)
            other['labels' if item is original else 'Labels']['com.docker.compose.project'] = 'other'
            with self.assertRaises(ValueError):
                trusted_sandbox_root('work-test', other if item is original else original, other if item is volume else volume)
        duplicate = copy.deepcopy(original); duplicate['mounts'].append(duplicate['mounts'][0])
        with self.assertRaises(ValueError): trusted_sandbox_root('work-test', duplicate, volume)

    def test_mapping_updates_only_owned_key_and_is_idempotent(self):
        container, volume = fixture()
        def query(command, **kwargs):
            if 'ps' in command: return 'a' * 64 + '\n'
            if command[1:3] == ['volume', 'inspect']: return json.dumps([volume])
            return json.dumps(container)
        with tempfile.TemporaryDirectory() as folder:
            state = pathlib.Path(folder); env = state / 'compose.env'
            env.write_text('UNCHANGED=fixture-value\n')
            self.assertTrue(ensure_runtime_mount_mapping(['docker', 'compose'], 'work-test', state, query))
            self.assertEqual(env.read_text(), 'UNCHANGED=fixture-value\n' + VARIABLE + '=' + volume['Mountpoint'] + '/sandboxes\n')
            self.assertFalse(ensure_runtime_mount_mapping(['docker', 'compose'], 'work-test', state, query))
            container['labels']['com.docker.compose.project'] = 'other'
            before = env.read_bytes()
            with self.assertRaises(SystemExit): ensure_runtime_mount_mapping(['docker', 'compose'], 'work-test', state, query)
            self.assertEqual(before, env.read_bytes())


if __name__ == '__main__': unittest.main()
