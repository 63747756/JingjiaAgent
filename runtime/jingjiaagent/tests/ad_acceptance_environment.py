"""Prepare/start a separate AD acceptance stack without changing the current stack.

This source-only helper uses fresh project volumes and private .state material.
Its LDAPS service is a test fixture; it is never delivered in an installation.
"""
import argparse
import contextlib
import http.cookiejar
import io
import json
import os
import pathlib
import subprocess
import sys
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from linux_web_security import prepare_ad_secret_key, require_security_config
from prepare_ad_fixture import prepare as prepare_directory

PROJECT = 'jingjiaagent-ad-acceptance'
STATE = ROOT / '.state/ad-acceptance'
ORIGIN = 'http://127.0.0.1:47426'
PORTS = {'web': 47426, 'preview': 47427, 'runtime': 47428, 'runtime_tls': 47429}


def private(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) if not isinstance(value, str) else value,
                    encoding='utf-8', newline='\n')
    path.chmod(0o600)


def environment():
    return dict(os.environ, JINGJIAAGENT_RUNTIME_WEB_PROJECT=PROJECT,
                JINGJIAAGENT_RUNTIME_WEB_STATE_DIRECTORY=str(STATE),
                JINGJIAAGENT_RUNTIME_WEB_BASE_URL=ORIGIN,
                JINGJIAAGENT_RUNTIME_WEB_ADMIN_EMAIL='ad-acceptance-admin@example.invalid',
                JINGJIAAGENT_RUNTIME_WEB_TEAM_NAME='AD 独立验收团队',
                JINGJIAAGENT_RUNTIME_WEB_FIXTURE_LABEL='AD acceptance',
                JINGJIAAGENT_RUNTIME_WEB_MEMBER_PREFIX='ad-acceptance')


def prepare_private_fixture(model_config):
    source = model_config.resolve()
    if not source.is_file() or not source.is_relative_to((ROOT / '.state').resolve()):
        raise SystemExit('Use an existing ignored local model configuration; credentials are not printed.')
    target = STATE / 'model.json'
    model = json.loads(source.read_text(encoding='utf-8'))
    if target.exists() and json.loads(target.read_text(encoding='utf-8')) != model:
        raise SystemExit('The independent model configuration already differs; do not replace private configuration implicitly.')
    # Only directory fixtures may precede the first install. Existing business
    # state or project storage must fail before writing models/endpoints/fixtures.
    prepare_ad_secret_key(STATE, PROJECT,
                          initial_entries=('fixture', 'model.json', 'acceptance-endpoints.json'))
    private(target, model)
    fixture = STATE / 'fixture'
    if not fixture.exists():
        prepare_directory(fixture)
    if not (fixture / 'directory.json').is_file():
        raise SystemExit('Independent fixture state is incomplete; preserve existing material and inspect it privately.')
    for name in ('directory', 'users'):
        baseline = fixture / (name + '.initial.json')
        if not baseline.exists():
            private(baseline, json.loads((fixture / (name + '.json')).read_text(encoding='utf-8')))
    private(STATE / 'acceptance-endpoints.json', {'project': PROJECT, 'origin': ORIGIN, 'ports': PORTS,
                                                'real_ad_verified': False})


def prepare_stack(model_config):
    prepare_private_fixture(model_config)
    # The production generator obtains immutable image IDs and verifies component
    # revisions. Restrict all its writes to this independent state directory.
    import prepare_linux_web
    previous = dict(os.environ)
    os.environ.update(environment())
    try:
        with contextlib.redirect_stdout(io.StringIO()):
            prepare_linux_web.main(ROOT)
    finally:
        os.environ.clear()
        os.environ.update(previous)
    # Keep the copied model consistent with the source used by the generator.
    model = json.loads(model_config.read_text(encoding='utf-8'))
    private(STATE / 'model.json', model)
    path = STATE / 'config/server/config.yaml'
    config = json.loads(path.read_text(encoding='utf-8'))
    config['server']['base_url'] = ORIGIN
    config['object_storage']['access_endpoint'] = ORIGIN + '/oss'
    config['runtime']['preview']['base_url'] = 'http://localhost:' + str(PORTS['preview'])
    config['runtime']['installer_base_url'] = ORIGIN
    private(path, config)
    # The public URL also receives local lifecycle callbacks in the backend/Web
    # shared namespace. Keep the original Guest listener, and expose this test
    # environment's public port inside that namespace as well.
    nginx_path = STATE / 'nginx.conf'
    nginx = nginx_path.read_text(encoding='utf-8')
    if nginx.count('listen 47424;') != 1:
        raise SystemExit('Production Web listener changed; inspect independent callback routing.')
    private(nginx_path, nginx.replace('listen 47424;', 'listen 47424; listen 47426;'))
    compose = (ROOT / 'compose.web.yaml').read_text(encoding='utf-8')
    for old, new in [('127.0.0.1:47424:47424', '127.0.0.1:47426:47424'),
                     ('127.0.0.1:47425:47425', '127.0.0.1:47427:47425'),
                     ('127.0.0.1:47418:7410', '127.0.0.1:47428:7410'),
                     ('127.0.0.1:47419:7411', '127.0.0.1:47429:7411')]:
        if compose.count(old) != 1:
            raise SystemExit('Production Compose endpoint changed; update the dedicated test override before starting.')
        compose = compose.replace(old, new)
    private(STATE / 'compose.web.yaml', compose)
    fixture_image = json.loads(subprocess.check_output(['docker', 'image', 'inspect', 'python:3.13-bookworm']))[0]['Id']
    values = (STATE / 'compose.env').read_text(encoding='utf-8')
    values += 'JINGJIAAGENT_AD_FIXTURE_IMAGE=' + fixture_image + '\n'
    values += 'JINGJIAAGENT_AD_FIXTURE_DIRECTORY=' + (STATE / 'fixture').as_posix() + '\n'
    private(STATE / 'compose.env', values)
    require_security_config(STATE)


def compose_command():
    require_security_config(STATE)
    return ['docker', 'compose', '--project-directory', str(ROOT), '-p', PROJECT,
            '--env-file', str(STATE / 'compose.env'), '-f', str(STATE / 'compose.web.yaml'),
            '-f', str(ROOT / 'tests/compose.ad-fixture.yaml')]


def run_public(command, env=None):
    result = subprocess.run(command, env=env, capture_output=True, text=True, encoding='utf-8', errors='replace')
    if result.returncode:
        # Child output can contain startup configuration. Keep failure evidence
        # privately for inspection and only print the stage/exit code.
        private(STATE / 'last-stage.log', result.stdout + '\n' + result.stderr)
        raise SystemExit('Independent AD environment stage failed (exit ' + str(result.returncode)
                         + '); diagnostics retained in its private state directory.')


def api(client, path, data=None):
    request = urllib.request.Request(ORIGIN + path, headers={'Content-Type': 'application/json'},
                                     data=json.dumps(data).encode() if data is not None else None)
    try:
        with client.open(request, timeout=60) as response:
            body = json.load(response)
    except urllib.error.HTTPError as error:
        raise SystemExit('Independent fixture API failed: HTTP ' + str(error.code)) from None
    if body.get('code') != 0:
        raise SystemExit('Independent fixture API failed: business code ' + str(body.get('code')))
    return body.get('data')


def seed_local_accounts():
    jar = http.cookiejar.CookieJar()
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))
    account = json.loads((STATE / 'web-account.json').read_text(encoding='utf-8'))
    api(client, '/api/v1/teams/users/login', account)
    for label in ('member', 'outsider'):
        path = STATE / ('web-' + label + '-account.json')
        if path.exists():
            continue
        email = 'ad-acceptance-' + label + '@example.invalid'
        data = api(client, '/api/v1/teams/users/with-password', {'emails': [email]})
        matches = [item for item in data['passwords'] if item['email'] == email]
        if len(matches) != 1:
            raise SystemExit('Independent member creation did not return exactly one private credential.')
        private(path, matches[0])


def configure_test_member_limit():
    database = PROJECT + '-postgres-1'
    count = subprocess.check_output(['docker', 'exec', database, 'psql', '-U', 'postgres', '-d', 'jingjiaagent',
                                     '-Atc', 'SELECT count(*) FROM teams;'], text=True).strip()
    if count != '1':
        raise SystemExit('Independent fixture must contain exactly one initialized team.')
    subprocess.run(['docker', 'exec', database, 'psql', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'jingjiaagent',
                     '-c', 'UPDATE teams SET member_limit=32;'], check=True, stdout=subprocess.DEVNULL)
    private(STATE / 'test-team-policy.json', {'member_limit': 32, 'fixture_only': True,
                                             'production_team_policy_changed': False})


def start_stack(model_config):
    prepare_stack(model_config)
    run_public(compose_command() + ['up', '-d', '--wait', '--wait-timeout', '180'])
    run_public([sys.executable, str(ROOT / 'seed_web.py')], environment())
    configure_test_member_limit()
    seed_local_accounts()
    # Owner/team IDs learned through the authenticated setup are passed back to
    # the runtime registry. Recreate only this project's namespace consumers.
    prepare_stack(model_config)
    run_public(compose_command() + ['up', '-d', '--force-recreate', '--wait', '--wait-timeout', '180', 'backend', 'web'])
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    auth = api(client, '/api/v1/users/auth-config')
    if auth.get('mode') == 'ad':
        raise SystemExit('Fresh AD acceptance must start with AD disabled; existing state was retained.')
    private(STATE / 'environment-ready.json', {'project': PROJECT, 'origin': ORIGIN,
                                              'fresh_ad_disabled': True, 'real_ad_verified': False})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('stage', choices=['fixture', 'prepare', 'start', 'status'])
    parser.add_argument('--model-config', type=pathlib.Path, default=ROOT / '.state/linux-web/model.json')
    args = parser.parse_args()
    if args.stage == 'fixture':
        prepare_private_fixture(args.model_config)
    elif args.stage == 'prepare':
        prepare_stack(args.model_config)
    elif args.stage == 'start':
        start_stack(args.model_config)
    else:
        run_public(compose_command() + ['ps'])
    print('Independent AD acceptance ' + args.stage + ' completed; Web ' + ORIGIN + '. No credentials printed.')


if __name__ == '__main__':
    main()
