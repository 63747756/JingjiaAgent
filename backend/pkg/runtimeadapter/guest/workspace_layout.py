"""Prepare platform-owned files without changing the repository working root."""
import base64
import fcntl
import json
import os
import pathlib
import sys
import tempfile

WORKSPACE = pathlib.Path('/workspace')
HOME = pathlib.Path.home()


def directory(path):
    if path.is_symlink() or path.resolve() != path:
        raise ValueError('workspace directory escapes root')
    path.mkdir(mode=0o700, parents=True, exist_ok=True)


def atomic(path, text):
    if path.is_symlink():
        raise ValueError('configuration file is a link')
    directory(path.parent)
    with tempfile.NamedTemporaryFile(dir=path.parent, mode='w', encoding='utf-8', delete=False) as stream:
        temporary = pathlib.Path(stream.name)
        try:
            os.chmod(temporary, 0o600)
            stream.write(text)
            stream.flush()
            os.fsync(stream.fileno())
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)


def apply(request):
    managed = WORKSPACE / '.jingjiaagent'
    directory(managed)
    lock_path = managed / '.workspace-layout.lock'
    marker = managed / 'workspace-layout.json'
    if lock_path.is_symlink() or marker.is_symlink():
        raise ValueError('invalid workspace metadata')
    with lock_path.open('a+') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        for name in ('work', 'output'):
            directory(managed / name)
        git = WORKSPACE / '.git'
        if git.exists() or git.is_symlink():
            if git.is_symlink() or not git.is_dir() or git.resolve() != git:
                raise ValueError('Git metadata must stay in workspace')
            directory(git / 'info')
            exclude = git / 'info/exclude'
            if exclude.is_symlink() or (exclude.exists() and (not exclude.is_file() or exclude.stat().st_size > 1 << 20)):
                raise ValueError('invalid Git local exclusions')
            previous = exclude.read_text() if exclude.exists() else ''
            if '/.jingjiaagent/' not in previous.splitlines():
                with exclude.open('a') as output:
                    output.write(('\n' if previous and not previous.endswith('\n') else '') + '/.jingjiaagent/\n')
        if not marker.exists():
            atomic(marker, json.dumps(dict(schema=1)))
        if request.get('patch_opencode'):
            rule = HOME / '.codingmatrix/project-tpl/.ai-ready/rules/jingjiaagent-workspace.md'
            config_path = HOME / '.config/opencode/opencode.json'
            if config_path.is_symlink() or config_path.resolve() != config_path:
                raise ValueError('OpenCode configuration escapes home')
            config = {}
            if config_path.exists():
                if not config_path.is_file() or config_path.stat().st_size > 1 << 20:
                    raise ValueError('invalid OpenCode configuration')
                config = json.loads(config_path.read_text())
            if not isinstance(config, dict):
                raise ValueError('invalid OpenCode configuration')
            instructions = config.get('instructions', [])
            if not isinstance(instructions, list):
                raise ValueError('invalid OpenCode instructions')
            atomic(rule, request['workspace_notice'])
            if str(rule) not in instructions:
                config['instructions'] = [*instructions, str(rule)]
                atomic(config_path, json.dumps(config))
        return dict(installed=True)


if __name__ == '__main__':
    try:
        print(json.dumps(dict(data=apply(json.loads(base64.b64decode(sys.argv[1], validate=True))))))
    except Exception:
        print(json.dumps(dict(error='Workspace layout preparation rejected', code='invalid_argument')))
