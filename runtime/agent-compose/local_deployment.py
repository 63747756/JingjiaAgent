"""Manage the final local agent-compose deployment; no historical cleanup."""
import argparse
import json
import os
import pathlib
import subprocess
import sys

root = pathlib.Path(__file__).resolve().parent
state = root / '.state/local-deployment'
project = 'jingjia-agent-local'
parser = argparse.ArgumentParser()
parser.add_argument('action', choices=['prepare', 'start', 'seed', 'test', 'capacity', 'status'])
args = parser.parse_args()
env = dict(os.environ, PYTHONDONTWRITEBYTECODE='1', RUNTIME_WEB_PROJECT=project,
    RUNTIME_WEB_STATE_DIRECTORY=str(state), RUNTIME_WEB_BASE_URL='http://127.0.0.1:47424',
    RUNTIME_WEB_BACKEND_IMAGE='jingjia-monkeycode-web:local-final-20261004-r7',
    RUNTIME_WEB_STATIC_DIRECTORY='.build/web-static-local-final-20261004-r7b',
    RUNTIME_WEB_ADMIN_EMAIL='admin@jingjia.local', RUNTIME_WEB_TEAM_NAME='Jingjia Agent 本机测试',
    RUNTIME_WEB_MAX_CPU_MILLIS='12000', RUNTIME_WEB_MAX_MEMORY_BYTES=str(20<<30),
    RUNTIME_WEB_ENABLE_INSTALLER='1', RUNTIME_INSTALL_BUNDLE_DIRECTORY=str(state/'config/server/installation-bundle'),
    RUNTIME_WEB_FIXTURE_LABEL='本机 DeepSeek', RUNTIME_WEB_MEMBER_PREFIX='local')
def run(name):
    subprocess.run([sys.executable, str(root / name)], env=env, check=True)

if args.action == 'prepare':
    # This generator always selects agent_compose and preserves an existing
    # deployment's credentials, node identity and encryption key.
    run('build_install_bundle.py')
    run('prepare_linux_web.py')
    cfg = json.loads((state / 'config/server/config.yaml').read_text(encoding='utf-8'))
    if cfg['runtime']['backend'] != 'agent_compose' or any(cfg.get('taskflow', {}).get(key) for key in ('server', 'url')):
        raise SystemExit('Final local deployment must use agent-compose exclusively.')
    print('Final local configuration prepared; backend=agent_compose.')
elif args.action == 'start':
    run('start_linux_web.py')
elif args.action == 'seed':
    run('seed_web.py')
    run('seed_linux_web.py')
    run('prepare_linux_web.py')
    # Node ownership comes from original team login, then the background
    # registry enrolls it when the backend loads this configuration.
    compose = ['docker', 'compose', '-p', project, '--env-file', str(state / 'compose.env'), '-f', str(root / 'compose.web.yaml')]
    subprocess.run(compose + ['up', '-d', '--force-recreate', '--wait', '--wait-timeout', '120', 'backend'], check=True)
    run('start_linux_web.py')
elif args.action == 'test':
    run('verify_local_deployment.py')
elif args.action == 'capacity':
    run('configure_local_capacity.py')
else:
    compose = ['docker', 'compose', '-p', project, '--env-file', str(state / 'compose.env'), '-f', str(root / 'compose.web.yaml')]
    subprocess.run(compose + ['ps'], check=True)
