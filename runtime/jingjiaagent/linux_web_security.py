"""Fail-closed configuration and migration checks for the Linux Web fixture."""
import hmac
import json
import re
import subprocess
from build_metadata import validate_image


SECURITY_VERSION = 1
MIGRATION_GUIDE = 'docs/remote-runtime/network-isolation.md'


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
