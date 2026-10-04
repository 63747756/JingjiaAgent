"""Resolve built images and create local secrets without printing credentials."""
import json
import pathlib
import secrets
import subprocess

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
for kind, tag in [('daemon', 'jingjia-agent-runtime:c03302d-p' + str(lock['patch_revision'])), ('guest', 'jingjia-agent-guest:c03302d-p' + str(lock.get('guest_patch_revision', lock['patch_revision'])))]:
    item = json.loads(subprocess.check_output(['docker', 'image', 'inspect', tag]))[0]
    if item['Config']['Labels'].get('org.opencontainers.image.revision') != 'c03302d15e26ad032a6df2048de5d505db5be48b':
        raise SystemExit('unexpected upstream image revision')
    revision = lock.get('guest_patch_revision', lock['patch_revision']) if kind == 'guest' else lock['patch_revision']
    if item['Config']['Labels'].get('monkeycode.runtime.patch') != str(revision):
        raise SystemExit('unexpected compatibility patch revision')
    images[kind] = item['Id']
(state / 'images.json').write_text(json.dumps(images, indent=2), encoding='utf-8')
(state / 'compose.env').write_text('RUNTIME_DAEMON_IMAGE=' + images['daemon'] + '\nRUNTIME_GUEST_IMAGE=' + images['guest'] + '\n', encoding='utf-8')
print('Prepared ignored .state files; Compose uses immutable local image IDs.')
