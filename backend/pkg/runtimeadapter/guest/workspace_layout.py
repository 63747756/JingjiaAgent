"""Prepare platform-owned files without changing the repository working root."""
import base64
import fcntl
import hashlib
import json
import os
import pathlib
import stat
import subprocess
import sys
import tempfile

WORKSPACE = pathlib.Path('/workspace')
HOME = pathlib.Path.home()
REQUEST_ROOT = pathlib.Path('/data/state/jingjiaagent-layout')
MAX_BYTES = 256 << 20
MAX_FILES = 2000


def directory(path):
    if path.is_symlink() or path.resolve() != path:
        raise ValueError('workspace directory escapes root')
    path.mkdir(mode=0o700, parents=True, exist_ok=True)


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def atomic(path, text):
    if path.is_symlink():
        raise ValueError('configuration file is a link')
    directory(path.parent)
    with tempfile.NamedTemporaryFile(dir=path.parent, mode='w', delete=False) as stream:
        temporary = pathlib.Path(stream.name)
        try:
            os.chmod(temporary, 0o600)
            stream.write(text)
            stream.flush()
            os.fsync(stream.fileno())
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)


def read_request(value):
    if 'request_path' not in value:
        return value
    path = pathlib.Path(value['request_path'])
    if path.parent != REQUEST_ROOT or path.resolve() != path or not path.is_file() or path.stat().st_size > 2 << 20:
        raise ValueError('invalid workspace inventory path')
    content = path.read_bytes()
    if hashlib.sha256(content).hexdigest() != value['sha256']:
        raise ValueError('workspace inventory changed')
    return json.loads(content)


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
        tracked = set()
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
            if request.get('migrate_legacy') and not marker.exists():
                process = subprocess.run(['git', '-c', 'core.fsmonitor=false', '-c', 'core.hooksPath=/dev/null',
                                          'ls-files', '-z', '--', 'work', 'output'], cwd=WORKSPACE,
                                         env={k: v for k, v in os.environ.items() if not k.startswith('GIT_')},
                                         stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10, check=True)
                tracked = {os.fsdecode(item) for item in process.stdout.split(b'\0') if item}
        moved = []
        allowed = set(request.get('legacy_files') or [])
        if request.get('migrate_legacy') and not marker.exists():
            candidates, total = [], 0
            for name in ('work', 'output'):
                source = WORKSPACE / name
                if name in tracked:
                    continue
                if not source.exists():
                    continue
                if source.is_symlink():
                    raise ValueError('legacy work directory is a link')
                if not source.is_dir():
                    continue  # A repository may have a regular file named output.
                for parent, dirs, files in os.walk(source, followlinks=False):
                    for filename in dirs + files:
                        path = pathlib.Path(parent) / filename
                        if path.is_symlink() and path.relative_to(WORKSPACE).as_posix() not in tracked:
                            raise ValueError('legacy work files contain a link')
                    for filename in files:
                        path = pathlib.Path(parent) / filename
                        relative = path.relative_to(WORKSPACE)
                        if relative.as_posix() in tracked:
                            continue
                        # A coding repository may have unrelated ignored build
                        # trees. Copy only files from its committed platform
                        # checkpoint, and never remove repository-side files.
                        if git.exists() and relative.as_posix() not in allowed:
                            continue
                        info = path.stat()
                        if not stat.S_ISREG(info.st_mode) or info.st_size > 100 << 20:
                            raise ValueError('unsupported legacy work file')
                        total += info.st_size
                        candidates.append((path, managed / relative, info))
                        if len(candidates) > MAX_FILES or total > MAX_BYTES:
                            raise ValueError('legacy workspace exceeds limits')
            # Copy and verify the entire set before removing any old file.
            for source, target, info in candidates:
                directory(target.parent)
                expected = digest(source)
                if target.is_symlink():
                    raise ValueError('managed work file is a link')
                if target.exists():
                    if not target.is_file() or digest(target) != expected:
                        raise ValueError('legacy and managed work files conflict')
                else:
                    with tempfile.NamedTemporaryFile(dir=target.parent, delete=False) as output:
                        temporary = pathlib.Path(output.name)
                        try:
                            with source.open('rb') as stream:
                                copied = 0
                                while chunk := stream.read(65536):
                                    copied += len(chunk)
                                    if copied > info.st_size:
                                        raise ValueError('legacy workspace changed while copying')
                                    output.write(chunk)
                            output.flush()
                            os.fsync(output.fileno())
                            os.chmod(temporary, 0o600 | (info.st_mode & 0o111))
                            if temporary.stat().st_size != info.st_size or digest(temporary) != expected:
                                raise ValueError('legacy workspace changed while copying')
                            os.replace(temporary, target)
                        finally:
                            temporary.unlink(missing_ok=True)
                moved.append((source, target, expected))
            for source, target, expected in moved:
                if digest(source) != expected or digest(target) != expected:
                    raise ValueError('legacy workspace changed before migration')
            # Publish the new layout only after all copies are verified, and
            # before deleting old files. An interrupted cleanup is harmless.
            atomic(marker, json.dumps(dict(schema=1)))
            if not git.exists():
                for source, _, _ in moved:
                    source.unlink()
            for name in ('work', 'output', 'input', 'project'):
                old = WORKSPACE / name
                if old.is_dir() and not old.is_symlink():
                    if git.exists():
                        continue
                    # Remove empty platform directories only; never recurse-delete.
                    for parent, dirs, files in os.walk(old, topdown=False):
                        if name in ('input', 'project') and git.exists():
                            break
                        try:
                            pathlib.Path(parent).rmdir()
                        except OSError:
                            pass
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
        return dict(installed=True, migrated=len(moved))


if __name__ == '__main__':
    try:
        print(json.dumps(dict(data=apply(read_request(json.loads(base64.b64decode(sys.argv[1], validate=True)))))))
    except Exception:
        print(json.dumps(dict(error='Workspace layout preparation rejected', code='invalid_argument')))
