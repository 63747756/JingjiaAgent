"""Resolve built images and create local secrets without printing credentials."""
import json
import pathlib
import secrets
import subprocess
from build_metadata import image_tag, validate_image

root = pathlib.Path(__file__).resolve().parent
state = root / '.state'
state.mkdir(exist_ok=True, mode=0o700)
for name, value in [('daemon.token', secrets.token_hex(32).encode()), ('payload.key', secrets.token_bytes(32))]:
    path = state / name
    if not path.exists():
        path.write_bytes(value)
        path.chmod(0o600)

images = {}
lock = json.loads((root / 'source.lock.json').read_text())
for kind in ('daemon', 'guest'):
    tag = image_tag(kind, lock)
    item = json.loads(subprocess.check_output(['docker', 'image', 'inspect', tag]))[0]
    validate_image(kind, item, lock)
    images[kind] = item['Id']
(state / 'images.json').write_text(json.dumps(images, indent=2), encoding='utf-8')
(state / 'compose.env').write_text('JINGJIAAGENT_RUNTIME_DAEMON_IMAGE=' + images['daemon'] + '\nJINGJIAAGENT_RUNTIME_GUEST_IMAGE=' + images['guest'] + '\n', encoding='utf-8')
print('Prepared ignored .state files; Compose uses immutable local image IDs.')
