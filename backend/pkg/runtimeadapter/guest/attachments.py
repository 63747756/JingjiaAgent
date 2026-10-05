"""Stage business-authorized attachments once per durable command in the Guest."""
import base64
import fcntl
import hashlib
import json
import mimetypes
import os
import pathlib
import re
import shutil
import sys
import tempfile
import time
import urllib.parse
import urllib.request
import urllib.error

WORKSPACE = pathlib.Path('/workspace')
STATE = pathlib.Path('/data/state/jingjiaagent-attachments')
BRIDGE = pathlib.Path('/opt/agent-compose-runtime/jingjiaagent-opencode.py')
MAX_FILE = 32 << 20
MAX_TOTAL = 256 << 20


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None


def atomic_json(path, value):
    with tempfile.NamedTemporaryFile(dir=path.parent, mode='w', encoding='utf-8', delete=False) as stream:
        temporary = pathlib.Path(stream.name)
        try:
            os.chmod(temporary, 0o600)
            json.dump(value, stream, ensure_ascii=False)
            stream.flush()
            os.fsync(stream.fileno())
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)


def install(req):
    task, command, files = req.get('task_id', ''), req.get('command_id', ''), req.get('attachments', [])
    if (not re.fullmatch(r'[a-f0-9-]{36}', task) or not re.fullmatch(r'[a-f0-9-]{36}', command) or
            not isinstance(files, list) or len(files) > 10):
        raise ValueError('invalid attachment request')
    if files and 'ATTACHMENT_PROTOCOL = 1' not in BRIDGE.read_text():
        raise ValueError('Guest attachment protocol unavailable')
    for item in files:
        filename = item.get('filename', '')
        if (not isinstance(filename, str) or not filename or filename in ('.', '..') or
                any(c in filename for c in '/\\') or any(ord(c) < 32 for c in filename) or len(filename.encode()) > 200):
            raise ValueError('invalid attachment filename')
        url = urllib.parse.urlsplit(item.get('url', ''))
        if url.scheme not in ('http', 'https') or not url.hostname or url.username or url.password or url.fragment:
            raise ValueError('invalid attachment URL')
    STATE.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd = os.open(STATE / (task + '.lock'), os.O_RDWR | os.O_CREAT, 0o600)
    fingerprint = hashlib.sha256(json.dumps(req, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    with os.fdopen(fd, 'r+') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if not files:
            atomic_json(STATE / (task + '.json'), dict(task_id=task, command_id=command, fingerprint=fingerprint, files=[]))
            return dict(installed=True)
        root = WORKSPACE / '.jingjiaagent' / 'attachments' / task
        for parent in (WORKSPACE / '.jingjiaagent', WORKSPACE / '.jingjiaagent/attachments', root):
            if parent.is_symlink() or parent.resolve()!=parent:
                raise ValueError('attachment directory escapes workspace')
            parent.mkdir(mode=0o700, exist_ok=True)
        target = root / command
        manifest = target / '.manifest.json'
        if target.exists():
            if target.is_symlink() or not manifest.is_file() or manifest.is_symlink():
                raise ValueError('invalid existing attachment directory')
            receipt = json.loads(manifest.read_text())
            if receipt.get('fingerprint') != fingerprint:
                raise ValueError('attachment replay conflict')
            for item in receipt['files']:
                path = pathlib.Path(item['path'])
                if path.parent != target or path.is_symlink() or not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != item['sha256']:
                    raise ValueError('staged attachment was changed')
        else:
            temporary = pathlib.Path(tempfile.mkdtemp(dir=root))
            try:
                staged, total = [], 0
                deadline = time.monotonic() + 35
                client = urllib.request.build_opener(NoRedirect())
                for index, item in enumerate(files):
                    name = str(index + 1) + '-' + item['filename']
                    path = temporary / name
                    timeout = deadline - time.monotonic()
                    if timeout <= 0:
                        raise TimeoutError('attachment staging exceeded deadline')
                    digest, size = hashlib.sha256(), 0
                    with client.open(item['url'], timeout=min(15, timeout)) as response, path.open('xb') as stream:
                        os.chmod(path, 0o600)
                        mime = mimetypes.guess_type(item['filename'])[0] or response.headers.get_content_type()
                        while True:
                            if time.monotonic() > deadline:
                                raise TimeoutError('attachment staging exceeded deadline')
                            data = response.read(65536)
                            if not data:
                                break
                            size += len(data)
                            total += len(data)
                            if size > MAX_FILE or total > MAX_TOTAL:
                                raise ValueError('attachment size exceeds limit')
                            digest.update(data)
                            stream.write(data)
                        stream.flush()
                        os.fsync(stream.fileno())
                    staged.append(dict(filename=item['filename'], path=str(target / name), mime=mime,
                                       size=size, sha256=digest.hexdigest()))
                receipt = dict(task_id=task, command_id=command, fingerprint=fingerprint, files=staged)
                atomic_json(temporary / '.manifest.json', receipt)
                os.rename(temporary, target)
            finally:
                if temporary.exists():
                    shutil.rmtree(temporary)
        # Empty selections also install an empty pointer. A later round cannot
        # silently reuse attachments from the previous prompt.
        atomic_json(STATE / (task + '.json'), receipt)
        return dict(installed=True)


if __name__ == '__main__':
    try:
        print(json.dumps(dict(data=install(json.loads(base64.b64decode(sys.argv[1], validate=True))))))
    except (ValueError, FileNotFoundError):
        print(json.dumps(dict(error='Guest attachments rejected', code='invalid_argument')))
    except urllib.error.HTTPError as error:
        permanent=300<=error.code<500 and error.code not in (408,429)
        print(json.dumps(dict(error='Guest attachment source rejected',code='invalid_argument' if permanent else 'unavailable')))
    except Exception:
        # URLs may contain credentials. Never reflect upstream diagnostics.
        print(json.dumps(dict(error='Guest attachment staging failed')))
