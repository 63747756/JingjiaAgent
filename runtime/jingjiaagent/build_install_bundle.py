"""Export already built, fixed Linux images. The bundle contains no node credentials."""
import hashlib
import json
import pathlib
import os
import subprocess
from build_metadata import image_tag, validate_image, revision

root = pathlib.Path(__file__).resolve().parent
state = root / '.state'
lock = json.loads((root / 'source.lock.json').read_text())
images = {kind:json.loads(subprocess.check_output(['docker','image','inspect',image_tag(kind,lock)]))[0]['Id']
    for kind in ('daemon','guest')}
proxy = json.loads((root / 'installer-proxy.lock.json').read_text())
manifest = {'schema': 1, 'product': 'jingjiaagent', 'archive': 'images.tar', 'daemon_image': images['daemon'],
            'guest_image': images['guest'], 'proxy_image': proxy['image'],
            'daemon_revision': revision('daemon',lock), 'guest_revision': revision('guest',lock),
            'upstream_commit': lock['commit']}
for kind in ('daemon', 'guest', 'proxy'):
    image_id = manifest[kind + '_image']
    actual = json.loads(subprocess.check_output(['docker', 'image', 'inspect', image_id]))[0]
    if actual['Id'] != image_id or actual['Os'] != 'linux':
        raise SystemExit('Expected an immutable Linux image.')
    if kind != 'proxy':
        validate_image(kind,actual,lock)
        source = actual['Config']['Labels']['org.opencontainers.image.revision']
        if manifest.get('source_commit',source) != source:
            raise SystemExit('Runtime components have different fork source commits.')
        manifest['source_commit'] = source
        manifest[kind+'_source_tree_sha256'] = actual['Config']['Labels']['jingjiaagent.source.tree.sha256']
        if kind == 'guest' and manifest['guest_source_tree_sha256'] != manifest['daemon_source_tree_sha256']:
            raise SystemExit('Runtime components have different source tree identities.')
    if manifest.get('architecture', actual['Architecture']) != actual['Architecture']:
        raise SystemExit('Runtime bundle architectures differ.')
    manifest['architecture'] = actual['Architecture']
if proxy['platform'] != 'linux/' + manifest['architecture']:
    raise SystemExit('Proxy lock platform does not match the runtime.')
bundle = pathlib.Path(os.environ.get('JINGJIAAGENT_RUNTIME_INSTALL_BUNDLE_DIRECTORY', str(state / 'installation-bundle'))).resolve()
if not bundle.is_relative_to(state.resolve()):
    raise SystemExit('Installer bundle must remain inside ignored private state.')
bundle.mkdir(parents=True, exist_ok=True)
archive = bundle / manifest['archive']
if archive.exists() and (bundle/'manifest.json').exists():
    previous = json.loads((bundle/'manifest.json').read_text())
    with archive.open('rb') as stream:
        checksum = hashlib.file_digest(stream, 'sha256').hexdigest()
    if all(previous.get(key) == value for key,value in manifest.items()) and previous.get('sha256') == checksum:
        print('Existing fixed runtime installer bundle verified and retained.')
        raise SystemExit(0)
subprocess.run(['docker', 'image', 'save', '-o', str(archive), manifest['daemon_image'], manifest['guest_image'], manifest['proxy_image']], check=True)
with archive.open('rb') as stream:
    manifest['sha256'] = hashlib.file_digest(stream, 'sha256').hexdigest()
(bundle / 'manifest.json').write_text(json.dumps(manifest, indent=2))
print('Created checksum-verified runtime installer bundle from fixed images.')
