"""Invoked through authenticated sandbox Exec; never runs on the host filesystem."""
import base64
import fnmatch
import json
import os
import pwd
import selectors
import shutil
import stat
import subprocess
import sys
import tempfile
import time


def path(value):
    if not isinstance(value, str) or "\x00" in value:
        raise ValueError("invalid path")
    if value == "~" or value.startswith("~/"):
        value = os.path.expanduser(value)
    return os.path.abspath(os.path.join("/workspace", value or "."))


def signature(p):
    s = os.stat(p)
    if not stat.S_ISREG(s.st_mode):
        raise ValueError("not a regular file")
    return [s.st_ino, s.st_size, s.st_mtime_ns]


def repo_path(value):
    p = path('.' if value in ('', '/') else value)
    if os.path.commonpath(['/workspace', os.path.realpath(p)]) != '/workspace':
        raise ValueError('repository path escapes workspace')
    return p


def info(p):
    s = os.lstat(p)
    kind = "symlink" if stat.S_ISLNK(s.st_mode) else "dir" if stat.S_ISDIR(s.st_mode) else "file"
    try:
        user = pwd.getpwuid(s.st_uid).pw_name
    except KeyError:
        user = str(s.st_uid)
    out = dict(name=os.path.basename(p), user=user, size=s.st_size, kind=kind,
               unix_mode=s.st_mode, created_at=int(s.st_ctime), accessed_at=int(s.st_atime),
               updated_at=int(s.st_mtime))
    if kind == "symlink":
        out["symlink_target"] = os.readlink(p)
        out["symlink_kind"] = "dir" if os.path.isdir(p) else "file" if os.path.isfile(p) else "unknown"
    return out


def git(args, allowed=(0,)):
    # Never run external diff, textconv, hooks or fsmonitor from repository config.
    process = subprocess.Popen(['git', '-c', 'core.quotepath=false', '-c', 'core.fsmonitor=false',
        '-c', 'core.hooksPath=/dev/null', '--literal-pathspecs', '--no-optional-locks'] + args, cwd='/workspace',
        env={k:v for k,v in os.environ.items() if not k.startswith('GIT_')},
        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    try:
        data = bytearray()
        os.set_blocking(process.stdout.fileno(), False)
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            deadline = time.monotonic() + 10
            while True:
                remaining = deadline - time.monotonic()
                if remaining <= 0 or not selector.select(remaining):
                    raise ValueError('repository Git operation timed out')
                chunk = os.read(process.stdout.fileno(), 32768)
                if not chunk:
                    break
                data.extend(chunk)
                if len(data) > 2 * 1024 * 1024:
                    raise ValueError('repository output exceeds limit')
        if process.wait(timeout=10) not in allowed:
            raise ValueError('repository Git operation failed')
        return bytes(data)
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
        process.stdout.close()


def git_repository():
    if not os.path.exists('/workspace/.git'):
        return False
    if os.path.commonpath(['/workspace', os.path.realpath('/workspace/.git')]) != '/workspace':
        raise ValueError('Git metadata escapes workspace')
    root = git(['rev-parse', '--show-toplevel']).decode().strip()
    if os.path.realpath(root) != '/workspace':
        raise ValueError('Git repository escapes workspace')
    return True


def repository_changes():
    if not git_repository():
        # An empty development workspace has no HEAD yet; its files are untracked.
        changes = []
        for directory, directories, names in os.walk('/workspace', followlinks=False):
            directories[:] = [d for d in directories if not d.startswith('.')]
            for name in names:
                if not name.startswith('.'):
                    changes.append(dict(path=os.path.relpath(os.path.join(directory, name), '/workspace'), status='??'))
                if len(changes) > 2000:
                    raise ValueError('repository change count exceeds limit')
        return dict(changes=changes)
    records = git(['status', '--porcelain=v1', '-z', '--untracked-files=all', '--ignore-submodules=all']).split(b'\x00')
    changes = []
    i = 0
    while i < len(records) and records[i]:
        item = records[i]
        i += 1
        status = item[:2].decode('ascii')
        change = dict(path=item[3:].decode('utf-8'), status=status.strip())
        if 'R' in status or 'C' in status:
            change['old_path'] = records[i].decode('utf-8')
            i += 1
        changes.append(change)
    if len(changes) > 2000:
        raise ValueError('repository change count exceeds limit')
    # HEAD might not exist on a newly initialized repository.
    head = git(['rev-parse', '--verify', 'HEAD'], allowed=(0, 128)).decode().strip() or None
    branch = git(['symbolic-ref', '--quiet', '--short', 'HEAD'], allowed=(0, 1)).decode().strip() or None
    if head:
        stats = git(['diff', '--no-ext-diff', '--no-textconv', '--numstat', '-z', 'HEAD', '--']).split(b'\x00')
        i = 0
        by_path = {c['path']: c for c in changes}
        while i < len(stats) and stats[i]:
            added, removed, name = stats[i].split(b'\t', 2)
            i += 1
            if not name:  # Rename: old and new names are separate NUL records.
                name = stats[i + 1]
                i += 2
            change = by_path.get(name.decode('utf-8'))
            if change is not None and added != b'-':
                change.update(additions=int(added), deletions=int(removed))
    return dict(changes=changes, branch=branch, commit_hash=head)


def repository_diff(r):
    p = repo_path(r['path'])
    relative = os.path.relpath(p, '/workspace')
    if relative == '.':
        raise ValueError('file diff requires a file path')
    context = r.get('context_lines')
    context = 3 if context is None else int(context)
    if not 0 <= context <= 1000:
        raise ValueError('invalid diff context size')
    if r.get('unified') is False:
        raise ValueError('non-unified repository diff is not supported')
    args = ['diff', '--no-ext-diff', '--no-textconv', '--no-color', '-U' + str(context)]
    tracked = False
    if git_repository():
        tracked = bool(git(['ls-files', '-z', '--', relative]))
        head = git(['rev-parse', '--verify', 'HEAD'], allowed=(0, 128)).strip()
        if tracked and head:
            names = [relative]
            for change in repository_changes()['changes']:
                if change['path'] == relative and 'old_path' in change:
                    names.insert(0, change['old_path'])
            return git(args + ['HEAD', '--'] + names).decode('utf-8', errors='replace')
    if not os.path.lexists(p):
        raise ValueError('repository file not found')
    return git(args + ['--no-index', '--', '/dev/null', p], allowed=(0, 1)).decode('utf-8', errors='replace')


def run(r):
    op = r["op"]
    if op == 'home':
        return dict(path=os.path.expanduser('~'))
    p = path(r.get("path", ""))
    if op.startswith('repo_'):
        p = repo_path(r.get('path', ''))
    if op == 'repo_changes':
        return repository_changes()
    if op == 'repo_diff':
        return repository_diff(r)
    if op == 'repo_list':
        out = []
        for name in sorted(os.listdir(p)):
            if not r.get('include_hidden') and name.startswith('.'):
                continue
            item = os.path.join(p, name)
            relative = os.path.relpath(item, '/workspace')
            if r.get('glob_pattern') and not (fnmatch.fnmatch(name, r['glob_pattern']) or fnmatch.fnmatch(relative, r['glob_pattern'])):
                continue
            s = os.lstat(item)
            mode = 3 if stat.S_ISLNK(s.st_mode) else 4 if stat.S_ISDIR(s.st_mode) else 2 if s.st_mode & 0o111 else 1
            entry = dict(name=name, path=relative, entry_mode=mode, size=s.st_size,
                         modified_at=int(s.st_mtime), mode=stat.S_IMODE(s.st_mode))
            if mode == 3:
                entry['symlink_target'] = os.readlink(item)
            out.append(entry)
        return out
    if op == "list":
        return [info(os.path.join(p, n)) for n in sorted(os.listdir(p))]
    if op in ("stat", "repo_stat"):
        sig = signature(p)
        return dict(size=sig[1], signature=sig)
    if op in ("read", "repo_read"):
        # A concurrent edit must fail explicitly instead of silently corrupting a download.
        if signature(p) != r["signature"]:
            raise ValueError("file changed during download")
        with open(p, "rb") as f:
            if [os.fstat(f.fileno()).st_ino, os.fstat(f.fileno()).st_size,
                os.fstat(f.fileno()).st_mtime_ns] != r["signature"]:
                raise ValueError("file changed during download")
            f.seek(r["offset"])
            data = f.read(min(r["length"], 32768))
        if signature(p) != r["signature"]:
            raise ValueError("file changed during download")
        return dict(data=base64.b64encode(data).decode())
    if op == "begin":
        if r.get("parents"):
            os.makedirs(os.path.dirname(p), mode=0o700, exist_ok=True)
        if os.path.isdir(p):
            raise ValueError("target is a directory")
        fd, temp = tempfile.mkstemp(prefix=".jingjiaagent-upload-", dir=os.path.dirname(p))
        os.close(fd)
        return dict(temp=temp)
    if op in ("write", "commit", "abort"):
        temp = path(r["temp"])
        if os.path.dirname(temp) != os.path.dirname(p) or not os.path.basename(temp).startswith(".jingjiaagent-upload-"):
            raise ValueError("invalid upload temporary path")
        if os.path.islink(temp):
            raise ValueError("invalid upload temporary file")
        if op == "abort":
            if os.path.exists(temp):
                os.unlink(temp)
        elif op == "write":
            data = base64.b64decode(r["data"], validate=True)
            with open(temp, "r+b") as f:
                f.seek(0, 2)
                size = f.tell()
                if size == r["offset"]:
                    f.write(data)
                elif size == r["offset"] + len(data):
                    f.seek(r["offset"])
                    if f.read() != data:
                        raise ValueError("upload offset conflict")
                else:
                    raise ValueError("upload offset conflict")
        else:
            if signature(temp)[1] != r["size"]:
                raise ValueError("upload size mismatch")
            with open(temp, "rb") as f:
                os.fsync(f.fileno())
            mode = r.get("mode")
            if mode is None:
                mode = stat.S_IMODE(os.stat(p).st_mode) if os.path.isfile(p) else 0o644
            if not isinstance(mode, int) or mode < 0 or mode > 0o777:
                raise ValueError("invalid file mode")
            os.chmod(temp, mode)
            os.replace(temp, p)
            directory = os.open(os.path.dirname(p), os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
        return []
    if op == "mkdir":
        os.makedirs(p, exist_ok=True)
    elif op == "delete":
        if p in ("/", "/workspace"):
            raise ValueError("cannot delete filesystem root")
        if os.path.isdir(p) and not os.path.islink(p):
            shutil.rmtree(p)
        else:
            os.unlink(p)
    elif op in ("copy", "move"):
        src, dst = path(r["source"]), path(r["target"])
        if src in ("/", "/workspace") or dst == "/":
            raise ValueError("cannot move or copy filesystem root")
        if op == "move":
            shutil.move(src, dst)
        elif os.path.isdir(src) and not os.path.islink(src):
            shutil.copytree(src, dst, symlinks=True)
        elif os.path.islink(src):
            os.symlink(os.readlink(src), dst)
        else:
            shutil.copy2(src, dst)
    else:
        raise ValueError("unsupported file operation")
    return []


if __name__ == "__main__":
    try:
        request = json.loads(base64.b64decode(sys.argv[1], validate=True))
        print(json.dumps(dict(data=run(request)), ensure_ascii=False))
    except (ValueError, OSError, KeyError, TypeError) as e:
        print(json.dumps(dict(error=str(e)), ensure_ascii=False))
