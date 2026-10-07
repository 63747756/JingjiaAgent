"""Verify an image-only release, then create a fresh local agent-compose installation."""
import argparse
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import sys
from build_metadata import ROOT, load_lock, revision, validate_image, validate_release_sources
from linux_web_security import prepare_ad_secret_key, require_empty_deployment

parser = argparse.ArgumentParser()
parser.add_argument('--bundle', type=pathlib.Path, required=True)
parser.add_argument('--model-config', type=pathlib.Path, required=True)
parser.add_argument('--verify-only', action='store_true')
args = parser.parse_args()
bundle = args.bundle.resolve()
manifest = json.loads((bundle / 'manifest.json').read_text(encoding='utf-8-sig'))
lock = load_lock()
components = ('daemon', 'guest', 'backend', 'frontend')
if manifest.get('product') != 'jingjiaagent' or manifest.get('schema') not in (1, 2):
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
sources = validate_release_sources(manifest)
subprocess.run(['docker', 'image', 'load', '-i', str(archive)], check=True, stdout=subprocess.DEVNULL)
for name, record in manifest['images'].items():
    image = json.loads(subprocess.check_output(['docker', 'image', 'inspect', record['tag']]))[0]
    if image['Id'] != record['id'] or image['Os'] + '/' + image['Architecture'] != record['platform']:
        raise SystemExit('Release image identity or platform mismatch')
    if name in components:
        validate_image(name, image, lock)
        data = image['Config']['Labels']
        if data['org.opencontainers.image.revision'] != sources[name]['fork_commit'] or data['jingjiaagent.source.tree.sha256'] != sources[name]['source_tree_sha256']:
            raise SystemExit('Loaded component source identity differs from the release')
if args.verify_only:
    print('Archive checksum, component revisions and reloaded image identities verified.')
    raise SystemExit(0)
require_empty_deployment('jingjiaagent')
if (ROOT / '.state/linux-web').exists():
    raise SystemExit('Fresh installation requires empty JingjiaAgent data volumes and private installation state. Existing data must be managed separately.')
state = ROOT / '.state'
if not args.model_config.is_file():
    raise SystemExit('Provide a private model configuration file outside the release bundle')
# Establish the fresh-install key before the runtime bundle creates state files.
prepare_ad_secret_key(state / 'linux-web', 'jingjiaagent')
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
