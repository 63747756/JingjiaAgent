"""Isolated capacity daemons built from the current component lock."""
import json
import os
import pathlib
import subprocess
import sys
from build_metadata import image_tag, validate_image

root = pathlib.Path(__file__).resolve().parent
state = root / '.state'
if not os.environ.get('JINGJIAAGENT_RUNTIME_TEST_DATABASE_URL'):
    raise SystemExit('Set JINGJIAAGENT_RUNTIME_TEST_DATABASE_URL to the isolated test database.')
images = {}
for kind in ('daemon','guest'):
    tag = image_tag(kind)
    item = json.loads(subprocess.check_output(['docker', 'image', 'inspect', tag]))[0]
    validate_image(kind, item)
    images[kind] = item['Id']
(state / 'capacity-images.json').write_text(json.dumps(images, indent=2), encoding='utf-8')
for suffix, port in [('a', 47414), ('b', 47415)]:
    envfile = state / ('capacity-' + suffix + '.env')
    envfile.write_text(f"JINGJIAAGENT_RUNTIME_DAEMON_IMAGE={images['daemon']}\nJINGJIAAGENT_RUNTIME_GUEST_IMAGE={images['guest']}\nJINGJIAAGENT_RUNTIME_PORT={port}\n", encoding='utf-8')
    subprocess.run(['docker', 'compose', '-p', 'jingjiaagent-runtime-capacity-' + suffix, '--env-file', str(envfile),
                    '-f', str(root / 'compose.yaml'), 'up', '-d', '--wait', '--wait-timeout', '60'], check=True)
env = dict(os.environ, JINGJIAAGENT_RUNTIME_CAPACITY_LIVE_TEST='1', JINGJIAAGENT_RUNTIME_TEST_URL='http://127.0.0.1:47414',
           JINGJIAAGENT_RUNTIME_TEST_SECOND_URL='http://127.0.0.1:47415', JINGJIAAGENT_RUNTIME_TEST_TOKEN_FILE=str(state / 'daemon.token'),
           JINGJIAAGENT_RUNTIME_TEST_GUEST_IMAGE=images['guest'], GOTMPDIR=str(root / '.build' / 'go-tmp'))
result = subprocess.run(['go', 'test', './pkg/runtimeadapter', '-run', '^TestLiveRuntimeCapacity$', '-count=1', '-v', '-timeout', '6m'],
                        cwd=root.parent.parent / 'backend', env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
output = result.stdout.decode('utf-8', errors='replace').replace((state / 'daemon.token').read_text().strip(), '[redacted]')
(state / 'capacity-live.log').write_text(output, encoding='utf-8')
print(output, end='')
sys.exit(result.returncode)
