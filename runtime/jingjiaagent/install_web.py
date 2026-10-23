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
parser.add_argument('--work-agent', action='store_true', help='Prepare the office profile with bundled dependencies on a fresh installation')
args = parser.parse_args()
bundle = args.bundle.resolve()
manifest = json.loads((bundle / 'manifest.json').read_text(encoding='utf-8-sig'))
lock = load_lock()
components = ('daemon', 'guest', 'backend', 'frontend')
if 'converter' in manifest.get('images', {}):
    components += ('converter',)
if args.work_agent and 'converter' not in components:
    raise SystemExit('Work mode requires a release containing the locked converter image')
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
skills = manifest.get('work_skills')
if args.work_agent and not isinstance(skills, dict):
    raise SystemExit('Work mode requires verified official Skill packages')
if skills is not None:
    folder = bundle / 'work-skills'
    if folder.is_symlink() or not folder.is_dir() or not folder.resolve().is_relative_to(bundle):
        raise SystemExit('Invalid official Skill directory')
    skill_manifest = folder / 'manifest.json'
    if skill_manifest.is_symlink() or not skill_manifest.is_file() or hashlib.sha256(skill_manifest.read_bytes()).hexdigest() != manifest.get('work_skills_manifest_sha256') or json.loads(skill_manifest.read_text()) != skills:
        raise SystemExit('Official Skill manifest integrity mismatch')
    for record in skills.get('skills', []):
        filename = record.get('archive', '')
        if not filename or pathlib.Path(filename).name != filename:
            raise SystemExit('Invalid official Skill archive path')
        skill_archive = folder / filename
        if skill_archive.is_symlink() or not skill_archive.is_file() or skill_archive.stat().st_size != record.get('bytes'):
            raise SystemExit('Official Skill archive integrity mismatch')
        with skill_archive.open('rb') as stream:
            if hashlib.file_digest(stream, 'sha256').hexdigest() != record.get('sha256'):
                raise SystemExit('Official Skill archive integrity mismatch')
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
if args.work_agent:
    env['JINGJIAAGENT_WORK_AGENT'] = '1'
for name in ('build_install_bundle.py', 'prepare_linux_web.py', 'start_linux_web.py', 'seed_web.py', 'seed_linux_web.py', 'prepare_linux_web.py'):
    subprocess.run([sys.executable, str(ROOT / name)], env=env, check=True)
subprocess.run([sys.executable, str(ROOT / 'start_linux_web.py'), '--recreate-backend'], env=env, check=True)
print('Fresh JingjiaAgent installation initialized with agent_compose.')
