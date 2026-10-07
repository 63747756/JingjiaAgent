"""One existing pN revision sequence per owned image; upstream versions are separate."""
import hashlib
import json
import pathlib
import subprocess

ROOT = pathlib.Path(__file__).resolve().parent
PRODUCT_SOURCE = 'https://github.com/63747756/JingjiaAgent'
REVISION_KEYS = {'daemon': 'patch_revision', 'guest': 'guest_patch_revision',
                 'backend': 'backend_patch_revision', 'frontend': 'frontend_patch_revision'}

def load_lock():
    return json.loads((ROOT / 'source.lock.json').read_text(encoding='utf-8'))

def revision(component, lock=None):
    lock = load_lock() if lock is None else lock
    if REVISION_KEYS[component] not in lock:
        raise SystemExit('Missing ' + component + ' component revision')
    value = lock[REVISION_KEYS[component]]
    if type(value) is not int or value < 1:
        raise ValueError('Image revision must be a positive integer')
    return value

def image_tag(component, lock=None):
    lock = load_lock() if lock is None else lock
    suffix = lock['commit'][:7] + '-' if component in ('daemon', 'guest') else ''
    return f'jingjiaagent-{component}:{suffix}p{revision(component, lock)}'

def source_metadata():
    repo = ROOT.parent.parent
    commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=repo, text=True).strip()
    digest = hashlib.sha256()
    for name in sorted(set(subprocess.check_output(['git', 'ls-files', '-co', '--exclude-standard'], cwd=repo, text=True).splitlines())):
        path = repo / name
        if name.startswith(('monkeyai/', 'docs/', '.monkeycode/', '.ohmyagent/')) or not path.is_file():
            continue
        digest.update(name.encode()); digest.update(b'\0'); digest.update(path.read_bytes())
    return commit, digest.hexdigest()

def labels(component, lock=None, metadata=None):
    lock = load_lock() if lock is None else lock
    commit, tree = metadata or source_metadata()
    result = {'org.opencontainers.image.source': PRODUCT_SOURCE,
              'org.opencontainers.image.revision': commit,
              'org.opencontainers.image.version': f'p{revision(component, lock)}',
              'org.opencontainers.image.title': 'JingjiaAgent ' + component,
              'jingjiaagent.component': component,
              'jingjiaagent.image.revision': str(revision(component, lock)),
              'jingjiaagent.source.tree.sha256': tree}
    if component in ('daemon', 'guest'):
        result.update({'jingjiaagent.upstream.source': lock['repository'],
                       'jingjiaagent.upstream.commit': lock['commit']})
    return result

def validate_image(component, image, lock=None):
    lock = load_lock() if lock is None else lock
    data = (image.get('Config') or {}).get('Labels') or {}
    expected = revision(component, lock)
    if (data.get('jingjiaagent.component'), data.get('jingjiaagent.image.revision'),
        data.get('org.opencontainers.image.version')) != (component, str(expected), f'p{expected}'):
        raise SystemExit(f'Unexpected {component} image revision; expected p{expected}')
    if component in ('daemon', 'guest') and data.get('jingjiaagent.upstream.commit') != lock['commit']:
        raise SystemExit(f'Unexpected {component} upstream image revision')
    if data.get('org.opencontainers.image.source') != PRODUCT_SOURCE:
        raise SystemExit(f'Unexpected {component} product source')
    if component in ('daemon', 'guest') and data.get('jingjiaagent.upstream.source') != lock['repository']:
        raise SystemExit(f'Unexpected {component} upstream source')
    commit = data.get('org.opencontainers.image.revision', '')
    tree = data.get('jingjiaagent.source.tree.sha256', '')
    if len(commit) != 40 or any(c not in '0123456789abcdef' for c in commit) or len(tree) != 64 or any(c not in '0123456789abcdef' for c in tree):
        raise SystemExit(f'Missing {component} source build identity')


def validate_release_sources(manifest):
    """Validate schema 1's common identity or schema 2's per-component identity."""
    components = ('daemon', 'guest', 'backend', 'frontend')
    schema = manifest.get('schema')
    if schema not in (1, 2):
        raise SystemExit('Invalid JingjiaAgent release manifest schema')
    shared = {'fork_commit': manifest.get('fork_commit'),
              'source_tree_sha256': manifest.get('source_tree_sha256')}
    sources = manifest.get('component_sources') if schema == 2 else {name: shared for name in components}
    if not isinstance(sources, dict) or set(sources) != set(components):
        raise SystemExit('Release component source identities are incomplete')
    for name in components:
        try:
            data = manifest['images'][name]['labels']
            expected = {'fork_commit': data['org.opencontainers.image.revision'],
                        'source_tree_sha256': data['jingjiaagent.source.tree.sha256']}
        except (KeyError, TypeError):
            raise SystemExit('Release component source identities are incomplete') from None
        if sources[name] != expected:
            raise SystemExit('Release ' + name + ' source identity differs from its image labels')
    if sources['backend'] != sources['frontend'] or shared != sources['backend']:
        raise SystemExit('Release backend and frontend must come from the same source identity')
    return sources
