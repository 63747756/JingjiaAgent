"""Retain and verify immutable images for the isolated phase-4 deployment.

The archive contains images only, never test accounts, data volumes, API keys,
node tokens or configuration. Loading it restores the same recorded image IDs
without stopping any containers or modifying their volumes.
"""
import hashlib
import json
import pathlib
import subprocess
from linux_web_common import root, state, load, save

directory = state / 'release-bundle'
directory.mkdir(exist_ok=True)
images = load('release-candidate.json')
images['rollback_backend'] = load('release-baseline.json')['backend']
tags = []
for name, image in images.items():
    actual = json.loads(subprocess.check_output(['docker', 'image', 'inspect', image]))[0]
    if actual['Id'] != image:
        raise RuntimeError('Bundle image identity differs')
    if name in ('daemon', 'guest') and actual['Config']['Labels'].get('monkeycode.runtime.patch') != '17':
        raise RuntimeError('Bundle runtime patch differs')
    tag = 'jingjia-phase4-bundle:' + name.replace('_', '-') + '-20261004'
    subprocess.run(['docker', 'tag', image, tag], check=True)
    tags.append(tag)
archive = directory / 'images.tar'
subprocess.run(['docker', 'image', 'save', '-o', str(archive)] + tags, check=True)
digest = hashlib.sha256()
with archive.open('rb') as file:
    while chunk := file.read(1 << 20):
        digest.update(chunk)
subprocess.run(['docker', 'image', 'load', '-i', str(archive)], check=True, stdout=subprocess.DEVNULL)
for (name, image), tag in zip(images.items(), tags):
    actual = json.loads(subprocess.check_output(['docker', 'image', 'inspect', tag]))[0]['Id']
    if actual != image:
        raise RuntimeError('Loaded bundle image identity differs')
manifest = {'upstream_commit': 'c03302d15e26ad032a6df2048de5d505db5be48b',
    'patch_revision': 17, 'images': images, 'archive_bytes': archive.stat().st_size,
    'archive_sha256': digest.hexdigest(), 'reload_same_image_ids': True,
    'data_volumes_and_credentials_excluded': True,
    'rollback_backend_source_reconstructed': True}
(directory / 'manifest.json').write_text(json.dumps(manifest, indent=2), encoding='utf-8')
save('release-bundle-report.json', manifest)
print('Eight immutable images archived and reloaded with identical IDs; data volumes and private configuration excluded.', flush=True)
