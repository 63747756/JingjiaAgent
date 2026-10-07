"""Fail-closed configuration and migration checks for the Linux Web fixture."""
import hmac
import json
import os
import re
import secrets
import stat
import subprocess
from build_metadata import validate_image


SECURITY_VERSION = 1
MIGRATION_GUIDE = 'docs/remote-runtime/network-isolation.md'
AD_SECRET_CONTAINER_PATH = '/run/secrets/ad-secret-key'


def ensure_ad_secret_key(state):
    """Validate an existing installation key. Recovery must never generate a key."""
    path = state / 'ad-secret.key'
    try:
        metadata = path.lstat()
        if not stat.S_ISREG(metadata.st_mode):
            raise ValueError('not a regular file')
        if os.name == 'posix' and metadata.st_mode & 0o077:
            raise ValueError('non-private permissions')
        if len(path.read_bytes()) != 32:
            raise ValueError('invalid length')
    except (OSError, ValueError):
        raise SystemExit('AD secret configuration is incomplete or invalid; restore the original '
                         '32-byte ad-secret.key with private permissions. Do not generate a '
                         'replacement for an existing deployment.') from None
    return path


def require_empty_deployment(project, check_output=None):
    """Read-only evidence for first install, including restored unlabelled volumes."""
    if not re.fullmatch(r'[a-z0-9][a-z0-9_-]*', project):
        raise SystemExit('Invalid deployment project name.')
    check_output = check_output or subprocess.check_output
    services = ('runtime', 'runtime-proxy', 'backend', 'web', 'postgres', 'redis', 'storage', 'clickhouse')
    try:
        containers = check_output(['docker', 'ps', '-a', '--format',
            '{{.Names}}|{{.Label "com.docker.compose.project"}}'], text=True)
        volumes = check_output(['docker', 'volume', 'ls', '--format',
            '{{.Name}}|{{.Label "com.docker.compose.project"}}'], text=True)
        for kind, records in (('container', containers), ('volume', volumes)):
            for record in records.splitlines():
                name, separator, owner = record.partition('|')
                if not separator or not name:
                    raise ValueError('invalid Docker inventory')
                named = name.startswith(project + '_') if kind == 'volume' else any(
                    name.startswith(project + delimiter + service + delimiter)
                    for service in services for delimiter in ('-', '_'))
                # This legacy local Redis container does not have Compose labels.
                named = named or (kind == 'container' and name == project + '-runtime-web-redis')
                if owner == project or (not owner and named):
                    raise SystemExit('Existing deployment containers or data volumes were found. '
                                     'Restore the original ad-secret.key; no private state was changed.')
    except (OSError, ValueError, subprocess.SubprocessError):
        raise SystemExit('Cannot verify empty deployment data through Docker; '
                         'no AD key or private state was created.') from None


def prepare_ad_secret_key(state, project, *, initial_entries=()):
    """Create only after proving empty data; otherwise require the original key.

    initial_entries is reserved for the dedicated source-only directory fixture;
    production callers require an empty state directory. It cannot permit a
    deployment configuration, credentials or business data.
    """
    path = state / 'ad-secret.key'
    if state.is_symlink() or (state.exists() and not state.is_dir()):
        raise SystemExit('Invalid private state directory; restore the original AD key.')
    if path.exists() or path.is_symlink():
        return ensure_ad_secret_key(state)
    if state.exists() and any(entry.name not in initial_entries for entry in state.iterdir()):
        raise SystemExit('Existing private deployment state is missing ad-secret.key; '
                         'restore the original key before preparing or recovering it. '
                         'No private state was changed.')
    require_empty_deployment(project)
    state.mkdir(parents=True, exist_ok=True)
    try:
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, 'wb') as stream:
            stream.write(secrets.token_bytes(32))
    except FileExistsError:
        pass
    return ensure_ad_secret_key(state)


def redis_password(keys):
    password = keys.get('redis_password', '')
    if not isinstance(password, str) or not re.fullmatch(r'[A-Za-z0-9_-]{32,128}', password):
        raise SystemExit('Missing or invalid private Redis password. Existing deployments must '
                         'follow ' + MIGRATION_GUIDE + '; credentials are not rotated automatically.')
    return password


def validate_runtime_image(name, image, lock):
    if name not in ('daemon', 'guest', 'backend', 'frontend'):
        return
    validate_image(name, image, lock)


def require_security_config(state):
    """Do not let `start` silently upgrade legacy configuration or credentials."""
    try:
        version = json.loads((state / 'network-security.json').read_text())['version']
        password = redis_password(json.loads((state / 'credentials.json').read_text()))
        secret_bytes = (state / 'redis.password').read_bytes()
        if secret_bytes != (password + '\n').encode('utf-8'):
            raise ValueError('Redis secret must use canonical LF bytes')
        secret = secret_bytes.decode('utf-8').strip()
        configured = json.loads((state / 'config/server/config.yaml').read_text())['redis']['pass']
    except (OSError, ValueError, KeyError, TypeError):
        raise SystemExit('Network isolation configuration is incomplete; follow ' + MIGRATION_GUIDE) from None
    if version != SECURITY_VERSION or not isinstance(configured, str) or not all(
            hmac.compare_digest(password, candidate) for candidate in (secret, configured)):
        raise SystemExit('Redis isolation configuration differs; follow ' + MIGRATION_GUIDE)
    try:
        configured_ad = json.loads((state / 'config/server/config.yaml').read_text())['ad']['secret_key_file']
    except (OSError, ValueError, KeyError, TypeError):
        raise SystemExit('AD secret configuration is incomplete; restore the original '
                         'configuration and installation-owned ad-secret.key.') from None
    ensure_ad_secret_key(state)
    if configured_ad != AD_SECRET_CONTAINER_PATH:
        raise SystemExit('AD secret configuration differs; restore the original 32-byte key '
                         'and the configured read-only backend mount.')


def validate_existing_networks(project, containers, networks):
    """Changing container networks requires an explicit operator migration."""
    expected = {
        'runtime': {'sandbox'}, 'runtime-proxy': {'sandbox'},
        'backend': {'sandbox', 'business'},
        'postgres': {'business'}, 'redis': {'business'},
        'storage': {'business'}, 'clickhouse': {'business'},
    }
    owned_networks = {network['Name'] for network in networks} | {project + '_sandbox', project + '_business'}
    for container in containers:
        labels = container.get('Config', {}).get('Labels') or {}
        service = labels.get('com.docker.compose.service')
        actual = set((container.get('NetworkSettings') or {}).get('Networks') or {})
        if labels.get('agent-compose.sandbox_id') and actual & owned_networks:
            if actual != {project + '_sandbox'}:
                raise SystemExit('Guest (including stopped Guests) still uses a legacy or unexpected network. '
                                 'No containers were changed; follow ' + MIGRATION_GUIDE)
        if service not in expected:
            continue  # Web shares backend's network namespace.
        if actual != {project + '_' + name for name in expected[service]}:
            raise SystemExit(f'{service} still uses a legacy or unexpected network. '
                             'No containers were changed; follow ' + MIGRATION_GUIDE)
    allowed = {project + '_sandbox', project + '_business'}
    for network in networks:
        if network['Name'] not in allowed and network.get('Containers'):
            raise SystemExit('Legacy project network still has attached containers (possibly existing Guests). '
                             'No containers were changed; follow ' + MIGRATION_GUIDE)


def check_existing_networks(project, check_output=subprocess.check_output):
    selector = 'label=com.docker.compose.project=' + project
    containers = check_output(['docker', 'ps', '-aq', '--filter', selector], text=True).split()
    networks = check_output(['docker', 'network', 'ls', '-q', '--filter', selector], text=True).split()
    # Docker network inspect omits stopped endpoints. Inspect dynamic Guest
    # containers too, then scope validation by this project's actual networks.
    if networks:
        guests = check_output(['docker', 'ps', '-aq', '--filter', 'label=agent-compose.sandbox_id'], text=True).split()
        containers = list(dict.fromkeys(containers + guests))
    inspected_containers = json.loads(check_output(['docker', 'inspect', *containers])) if containers else []
    inspected_networks = json.loads(check_output(['docker', 'network', 'inspect', *networks])) if networks else []
    validate_existing_networks(project, inspected_containers, inspected_networks)
