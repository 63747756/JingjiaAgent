"""Install the business-authorized Skill/plugin selection inside one Guest."""
import base64
import fcntl
import hashlib
import io
import json
import os
import pathlib
import shutil
import stat
import sys
import tempfile
import urllib.parse
import urllib.request
import zipfile

HOME = pathlib.Path.home()
MAX_ARCHIVE = 32 << 20
MAX_FILE = 32 << 20
MAX_TOTAL = 256 << 20
MAX_FILES = 1000


class InvalidResource(ValueError):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None


def relative(value):
    if not isinstance(value, str) or not value or '\\' in value or '\x00' in value:
        raise InvalidResource('invalid archive path')
    p = pathlib.PurePosixPath(value)
    if p.is_absolute() or any(part in ('..', '') for part in value.split('/')) or p == pathlib.PurePosixPath('.'):
        raise InvalidResource('archive path escapes resource')
    return p


def name(value):
    if (not isinstance(value, str) or not value or value in ('.', '..') or
            any(c in value for c in '/\\') or any(ord(c) < 32 for c in value) or len(value.encode()) > 200):
        raise InvalidResource('invalid resource name')
    return value


def archive(reference):
    address = reference.get('zip_url', '')
    u = urllib.parse.urlsplit(address)
    if u.scheme not in ('http', 'https') or not u.hostname or u.username or u.password or u.fragment:
        raise InvalidResource('invalid resource URL')
    # Never forward a presigned URL to a redirect target or reflect it in errors.
    client = urllib.request.build_opener(NoRedirect())
    with client.open(address, timeout=25) as response:
        data = response.read(MAX_ARCHIVE + 1)
    if len(data) > MAX_ARCHIVE:
        raise InvalidResource('resource archive exceeds limit')
    expected = reference.get('sha256', '')
    if expected and (len(expected) != 64 or any(c not in '0123456789abcdef' for c in expected) or hashlib.sha256(data).hexdigest() != expected):
        raise InvalidResource('resource archive hash mismatch')
    return data


def unpack(data, target, required, verify=False):
    try:
        source = zipfile.ZipFile(io.BytesIO(data))
    except zipfile.BadZipFile:
        raise InvalidResource('invalid resource archive') from None
    with source:
        entries, total = [], 0
        for entry in source.infolist():
            path = relative(entry.filename.rstrip('/'))
            mode = entry.external_attr >> 16
            if stat.S_IFMT(mode) not in (0, stat.S_IFREG, stat.S_IFDIR):
                raise InvalidResource('archive has a special file')
            if entry.is_dir():
                continue
            total += entry.file_size
            if entry.file_size > MAX_FILE or total > MAX_TOTAL:
                raise InvalidResource('expanded resource exceeds limit')
            entries.append((entry, path, mode))
        if len(entries) > MAX_FILES:
            raise InvalidResource('resource has too many files')
        required = relative(required) if required else None
        paths = {path for _, path, _ in entries}
        if any(parent in paths for path in paths for parent in path.parents if parent != pathlib.PurePosixPath('.')):
            raise InvalidResource('archive path conflicts with a file')
        prefix = None
        if required and required not in paths and entries:
            roots = {path.parts[0] for _, path, _ in entries}
            if len(roots) == 1:
                candidate = pathlib.PurePosixPath(next(iter(roots)))
                if candidate / required in paths:
                    prefix = candidate
        written, normalized = set(), []
        for entry, path, mode in entries:
            if prefix:
                path = path.relative_to(prefix)
            if path in written:
                raise InvalidResource('duplicate archive path')
            written.add(path)
            normalized.append((entry, path, mode))
        if required and required not in written:
            raise InvalidResource('resource entry file is missing')
        if verify:
            if target.is_symlink() or not target.is_dir():
                return False
            expected_paths = set(written)
            for path in written:
                expected_paths.update(p for p in path.parents if p != pathlib.PurePosixPath('.'))
            actual_paths = set()
            for parent, dirs, files in os.walk(target, followlinks=False):
                for filename in dirs + files:
                    installed = pathlib.Path(parent) / filename
                    if installed.is_symlink():
                        return False
                    actual_paths.add(pathlib.PurePosixPath(installed.relative_to(target).as_posix()))
                    if len(actual_paths) > len(expected_paths):
                        return False
            if actual_paths != expected_paths:
                return False
        actual = 0
        for entry, path, mode in normalized:
            destination = target.joinpath(*path.parts)
            if verify:
                info = destination.lstat()
                if (not stat.S_ISREG(info.st_mode) or info.st_size != entry.file_size or
                        stat.S_IMODE(info.st_mode) != (0o600 | (mode & 0o111))):
                    return False
                expected_digest, installed_digest = hashlib.sha256(), hashlib.sha256()
                with source.open(entry) as stream, destination.open('rb') as installed:
                    count = 0
                    while chunk := stream.read(65536):
                        count += len(chunk)
                        actual += len(chunk)
                        if count > MAX_FILE or actual > MAX_TOTAL:
                            raise InvalidResource('expanded resource exceeds limit')
                        expected_digest.update(chunk)
                        installed_digest.update(installed.read(len(chunk)))
                    if installed.read(1) or expected_digest.digest() != installed_digest.digest():
                        return False
                continue
            destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            with source.open(entry) as stream, destination.open('xb') as output:
                count = 0
                while chunk := stream.read(65536):
                    count += len(chunk)
                    actual += len(chunk)
                    if count > MAX_FILE or actual > MAX_TOTAL:
                        raise InvalidResource('expanded resource exceeds limit')
                    output.write(chunk)
            os.chmod(destination, 0o600 | (mode & 0o111))
        if verify:
            return True
        return actual, len(written)


def pinned_archive(cache, reference):
    """Reuse bytes only after checking the caller's immutable package hash."""
    expected = reference.get('sha256', '')
    if not expected:
        return archive(reference)
    path = cache / (expected + '.zip')
    if path.is_symlink():
        raise InvalidResource('resource cache is a symbolic link')
    if path.exists():
        if not path.is_file() or path.stat().st_size > MAX_ARCHIVE:
            raise InvalidResource('invalid resource cache')
        data = path.read_bytes()
        if hashlib.sha256(data).hexdigest() == expected:
            return data
    data = archive(reference)
    # Keep verification here too: callers and cache contents are untrusted.
    if len(data) > MAX_ARCHIVE or hashlib.sha256(data).hexdigest() != expected:
        raise InvalidResource('resource archive hash mismatch')
    with tempfile.NamedTemporaryFile(dir=cache, delete=False) as stream:
        temporary = pathlib.Path(stream.name)
        try:
            os.chmod(temporary, 0o600)
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)
    return data


def installed_matches(data, target, required):
    """Compare the complete installed tree with a verified ZIP, without writes.

    Never trust an editable Guest receipt as proof of installed script bytes.
    ZIP validation is shared with installation, including wrapper stripping.
    """
    return unpack(data, target, required, verify=True)


def safe_directory(root):
    current = root
    while current != HOME.parent:
        if current.is_symlink():
            raise InvalidResource('resource directory is a symbolic link')
        current = current.parent
    root.mkdir(mode=0o700, parents=True, exist_ok=True)


def apply(request):
    resources = request.get('resources')
    if not isinstance(resources, dict):
        raise InvalidResource('invalid resource selection')
    selected, descriptors = {}, {}
    for kind in ('skills', 'plugins'):
        refs = resources.get(kind) or []
        if not isinstance(refs, list) or len(refs) > 128:
            raise InvalidResource('resource count exceeds limit')
        names, items = set(), []
        for ref in refs:
            if not isinstance(ref, dict):
                raise InvalidResource('invalid resource reference')
            key = name(ref.get('name'))
            if key in names:
                raise InvalidResource('duplicate resource name')
            names.add(key)
            entry = 'SKILL.md' if kind == 'skills' else ref.get('entry_filename', '')
            if entry:
                relative(entry)
            version = ref.get('version', '')
            if not isinstance(version, str):
                raise InvalidResource('invalid resource version')
            expected = ref.get('sha256', '')
            if not isinstance(expected, str) or (expected and (len(expected) != 64 or any(c not in '0123456789abcdef' for c in expected))):
                raise InvalidResource('invalid resource hash')
            identity = version or hashlib.sha256(ref.get('zip_url', '').encode()).hexdigest()
            items.append(dict(name=key, version=identity, entry=entry, sha256=ref.get('sha256', '')))
        selected[kind], descriptors[kind] = refs, sorted(items, key=lambda item: item['name'])
    digest = hashlib.sha256(json.dumps(descriptors, sort_keys=True).encode()).hexdigest()
    root = HOME / '.codingmatrix' / 'project-tpl' / '.ai-ready'
    safe_directory(root)
    for kind in selected:
        if (root / kind).is_symlink():
            raise InvalidResource('managed resource directory is a symbolic link')
    lock_path = root / '.runtime-resource-lock'
    manifest = root / '.runtime-resources.json'
    if lock_path.is_symlink() or manifest.is_symlink():
        raise InvalidResource('resource metadata is a symbolic link')
    with open(lock_path, 'a+') as lock:
        os.chmod(lock_path, 0o600)
        fcntl.flock(lock, fcntl.LOCK_EX)
        cache = root / '.runtime-archives'
        safe_directory(cache)
        retained = {item['sha256'] + '.zip' for items in descriptors.values() for item in items if item['sha256']}
        # This cache belongs to the installer, and retains only this selection.
        for cached in cache.iterdir():
            if cached.is_symlink() or not cached.is_file():
                raise InvalidResource('invalid resource cache entry')
            if cached.name not in retained:
                cached.unlink()
        packages = {}
        cache_bytes, expanded_bytes, expanded_files = 0, 0, 0
        for kind, refs in selected.items():
            for ref in refs:
                if ref.get('sha256'):
                    data = pinned_archive(cache, ref)
                    packages[kind, ref['name']] = data
                    cache_bytes += len(data)
                    if cache_bytes > MAX_TOTAL:
                        raise InvalidResource('resource cache exceeds limit')
                    with zipfile.ZipFile(io.BytesIO(data)) as package:
                        for entry in package.infolist():
                            if not entry.is_dir():
                                expanded_bytes += entry.file_size
                                expanded_files += 1
                                if entry.file_size > MAX_FILE or expanded_bytes > MAX_TOTAL or expanded_files > MAX_FILES:
                                    raise InvalidResource('resource selection exceeds expanded limits')
        if manifest.exists() and json.loads(manifest.read_text()).get('digest') == digest:
            valid = all(((root / kind / item['name'] / item['entry']).is_file() if item['entry'] else (root / kind / item['name']).is_dir()) and
                        not (root / kind / item['name']).is_symlink()
                        for kind, items in descriptors.items() for item in items)
            valid = valid and all({p.name for p in (root / kind).iterdir()} == {item['name'] for item in descriptors[kind]}
                                  for kind in selected if (root / kind).is_dir())
            if valid:
                valid = all(installed_matches(packages[kind, item['name']], root / kind / item['name'], item['entry'])
                            for kind, items in descriptors.items() for item in items if item['sha256'])
            if valid and all((root / kind).is_dir() for kind in selected):
                return dict(installed=True, reused=True)
        stage = pathlib.Path(tempfile.mkdtemp(prefix='.runtime-resources-', dir=root))
        installed, backed_up = [], []
        try:
            total_bytes, total_files = 0, 0
            for kind, refs in selected.items():
                (stage / kind).mkdir(mode=0o700)
                for ref in refs:
                    destination = stage / kind / ref['name']
                    destination.mkdir(mode=0o700)
                    data = packages.get((kind, ref['name']))
                    if data is None:
                        data = archive(ref)
                    size, count = unpack(data, destination, 'SKILL.md' if kind == 'skills' else ref.get('entry_filename'))
                    total_bytes += size
                    total_files += count
                    if total_bytes > MAX_TOTAL or total_files > MAX_FILES:
                        raise InvalidResource('resource selection exceeds expanded limits')
            # All archives pass validation before any active selection changes.
            for kind in selected:
                target = root / kind
                if target.exists():
                    os.replace(target, stage / ('previous-' + kind))
                    backed_up.append(kind)
                os.replace(stage / kind, target)
                installed.append(kind)
            temporary = stage / 'manifest.json'
            with temporary.open('w') as output:
                json.dump(dict(digest=digest, resources=descriptors), output)
                output.flush()
                os.fsync(output.fileno())
            os.chmod(temporary, 0o600)
            os.replace(temporary, manifest)
            return dict(installed=True, reused=False)
        except Exception:
            for kind in reversed(installed):
                shutil.rmtree(root / kind)
            for kind in reversed(backed_up):
                os.replace(stage / ('previous-' + kind), root / kind)
            raise
        finally:
            shutil.rmtree(stage)


if __name__ == '__main__':
    try:
        request = json.loads(base64.b64decode(sys.argv[1], validate=True))
        print(json.dumps(dict(data=apply(request))))
    except (InvalidResource, zipfile.BadZipFile):
        print(json.dumps(dict(error='Guest resource package rejected', code='invalid_argument')))
    except Exception:
        print(json.dumps(dict(error='Guest resource installation failed', code='unavailable')))
