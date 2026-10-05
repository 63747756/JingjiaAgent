"""Verify an image-only release, then create a fresh local agent-compose installation."""
import argparse
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import sys
from build_metadata import ROOT, load_lock, revision, validate_image

parser = argparse.ArgumentParser()
parser.add_argument('--bundle', type=pathlib.Path, required=True)
parser.add_argument('--model-config', type=pathlib.Path, required=True)
parser.add_argument('--verify-only', action='store_true')
args = parser.parse_args()
bundle = args.bundle.resolve()
manifest = json.loads((bundle / 'manifest.json').read_text(encoding='utf-8-sig'))
lock = load_lock()
components = ('daemon', 'guest', 'backend', 'frontend')
if manifest.get('product') != 'jingjiaagent' or manifest.get('schema') != 1:
    raise SystemExit('Invalid JingjiaAgent release manifest')
if manifest.get('upstream_commit') != lock['commit'] or manifest.get('component_revisions') != {name: revision(name, lock) for name in components}:
    raise SystemExit('Release component revisions differ from the source lock')
archive = bundle / manifest['archive']
if archive.name != manifest['archive'] or not archive.resolve().is_relative_to(bundle):
    raise SystemExit('Invalid release archive path')
with archive.open('rb') as stream:
    checksum = hashlib.file_digest(stream, 'sha256').hexdigest()
if checksum != manifest['archive_sha256']:
    raise SystemExit('Release archive checksum mismatch')
for name in components:
    validate_image(name, {'Config': {'Labels': manifest['images'][name]['labels']}}, lock)
    data = manifest['images'][name]['labels']
    if data['org.opencontainers.image.revision'] != manifest.get('fork_commit') or data['jingjiaagent.source.tree.sha256'] != manifest.get('source_tree_sha256'):
        raise SystemExit('Release component source identities differ')
subprocess.run(['docker', 'image', 'load', '-i', str(archive)], check=True, stdout=subprocess.DEVNULL)
for name, record in manifest['images'].items():
    image = json.loads(subprocess.check_output(['docker', 'image', 'inspect', record['tag']]))[0]
    if image['Id'] != record['id'] or image['Os'] + '/' + image['Architecture'] != record['platform']:
        raise SystemExit('Release image identity or platform mismatch')
    if name in components:
        validate_image(name, image, lock)
        data = image['Config']['Labels']
        if data['org.opencontainers.image.revision'] != manifest['fork_commit'] or data['jingjiaagent.source.tree.sha256'] != manifest['source_tree_sha256']:
            raise SystemExit('Loaded component source identity differs from the release')
if args.verify_only:
    print('Archive checksum, component revisions and reloaded image identities verified.')
    raise SystemExit(0)
existing = subprocess.check_output(['docker', 'ps', '-aq', '--filter', 'label=com.docker.compose.project=jingjiaagent'], text=True).strip()
if existing:
    raise SystemExit('This entry point creates a fresh installation. An existing jingjiaagent deployment must be managed separately.')
existing_volumes = subprocess.check_output(['docker', 'volume', 'ls', '-q', '--filter', 'label=com.docker.compose.project=jingjiaagent'], text=True).strip()
if existing_volumes or (ROOT / '.state/linux-web').exists():
    raise SystemExit('Fresh installation requires empty JingjiaAgent data volumes and private installation state. Existing data must be managed separately.')
state = ROOT / '.state'
state.mkdir(exist_ok=True)
if not args.model_config.is_file():
    raise SystemExit('Provide a private model configuration file outside the release bundle')
target = state / 'model.json'
if args.model_config.resolve() != target.resolve():
    shutil.copyfile(args.model_config, target)
target.chmod(0o600)
env = dict(os.environ, JINGJIAAGENT_RUNTIME_WEB_PROJECT='jingjiaagent',
           JINGJIAAGENT_RUNTIME_WEB_STATE_DIRECTORY=str(state / 'linux-web'),
           JINGJIAAGENT_RUNTIME_WEB_BASE_URL='http://127.0.0.1:47424',
           JINGJIAAGENT_RUNTIME_WEB_ENABLE_INSTALLER='1',
           JINGJIAAGENT_RUNTIME_INSTALL_BUNDLE_DIRECTORY=str(state/'linux-web/config/server/installation-bundle'))
for name in ('build_install_bundle.py', 'prepare_linux_web.py', 'start_linux_web.py', 'seed_web.py', 'seed_linux_web.py', 'prepare_linux_web.py'):
    subprocess.run([sys.executable, str(ROOT / name)], env=env, check=True)
subprocess.run([sys.executable, str(ROOT / 'start_linux_web.py'), '--recreate-backend'], env=env, check=True)
print('Fresh JingjiaAgent installation initialized with agent_compose.')
