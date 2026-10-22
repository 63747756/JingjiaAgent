"""Resolve only the owned runtime-data volume's trusted Docker host path.

Docker Desktop self-inspect through the daemon socket can take seconds. Resolve
the actual Compose volume from the host once per start, cross-check its identity,
and use upstream's explicit SandboxRoot mapping. No caller provides a host path.
"""
import json
import os
import pathlib
import re
import subprocess


VARIABLE = 'JINGJIAAGENT_RUNTIME_HOST_SANDBOX_ROOT'


def trusted_sandbox_root(project, container, volume):
    if not re.fullmatch(r'[a-z0-9][a-z0-9_-]{0,62}', project):
        raise ValueError('invalid Compose project')
    labels = container.get('labels') or {}
    if (labels.get('com.docker.compose.project'), labels.get('com.docker.compose.service')) != (project, 'runtime'):
        raise ValueError('runtime container ownership differs')
    mounts = [item for item in container.get('mounts', []) if item.get('Destination') == '/data']
    if len(mounts) != 1:
        raise ValueError('runtime /data must have one volume')
    mount = mounts[0]
    expected_name = project + '_runtime-data'
    if (mount.get('Type'), mount.get('Name'), mount.get('Driver'), mount.get('RW')) != ('volume', expected_name, 'local', True):
        raise ValueError('runtime /data must use its owned local writable volume')
    owner = volume.get('Labels') or {}
    if (volume.get('Name'), volume.get('Driver'), volume.get('Scope'), owner.get('com.docker.compose.project'), owner.get('com.docker.compose.volume')) != (expected_name, 'local', 'local', project, 'runtime-data'):
        raise ValueError('runtime-data volume ownership differs')
    source = mount.get('Source', '')
    if not isinstance(source, str):
        raise ValueError('runtime-data volume source is invalid')
    path = pathlib.PurePosixPath(source)
    if (not path.is_absolute() or source != path.as_posix()
        or '..' in path.parts or any(char in source for char in ('\\', '\x00', '\r', '\n', '$', '#', '"', "'"))
        or source == '/' or volume.get('Mountpoint') != source or volume.get('Options')):
        raise ValueError('runtime-data volume source is not a verified plain local path')
    return source + '/sandboxes'


def ensure_runtime_mount_mapping(command, project, state, check_output=None):
    query = check_output or subprocess.check_output
    try:
        container_id = query(command + ['ps', '-q', 'runtime'], text=True).strip()
        if not re.fullmatch(r'[0-9a-f]{12,64}', container_id):
            raise ValueError('one running runtime container is required')
        container = json.loads(query(['docker', 'inspect', container_id, '--format',
            '{"labels":{{json .Config.Labels}},"mounts":{{json .Mounts}}}'], text=True))
        volumes = json.loads(query(['docker', 'volume', 'inspect', project + '_runtime-data'], text=True))
        if not isinstance(volumes, list) or len(volumes) != 1:
            raise ValueError('one runtime-data volume is required')
        host_root = trusted_sandbox_root(project, container, volumes[0])
        env_file = state / 'compose.env'
        lines = env_file.read_text(encoding='utf-8').splitlines()
        selected = [line for line in lines if line.startswith(VARIABLE + '=')]
        if len(selected) > 1:
            raise ValueError('duplicate runtime mapping configuration')
        expected = VARIABLE + '=' + host_root
        if selected == [expected]:
            return False
        lines = [line for line in lines if not line.startswith(VARIABLE + '=')]
        lines.append(expected)
        temporary = env_file.with_suffix('.env.mount-tmp')
        with open(temporary, 'w', encoding='utf-8', newline='\n') as output:
            os.chmod(temporary, 0o600)
            output.write('\n'.join(lines) + '\n')
        os.replace(temporary, env_file)
        return True
    except (OSError, ValueError, TypeError, subprocess.SubprocessError):
        raise SystemExit('Cannot verify the owned runtime-data Docker mount; no runtime mapping was accepted.') from None
