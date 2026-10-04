"""Isolated p8 daemons on one Docker engine; leaves deployed p7 fixtures intact."""
import json
import os
import pathlib
import subprocess
import sys

root = pathlib.Path(__file__).resolve().parent
state = root / '.state'
if not os.environ.get('RUNTIME_TEST_DATABASE_URL'):
    raise SystemExit('Set RUNTIME_TEST_DATABASE_URL to the isolated test database.')
images = {}
for kind, tag in [('daemon', 'jingjia-agent-runtime:c03302d-p8'), ('guest', 'jingjia-agent-guest:c03302d-p8')]:
    item = json.loads(subprocess.check_output(['docker', 'image', 'inspect', tag]))[0]
    labels = item['Config'].get('Labels', {})
    if labels.get('monkeycode.runtime.patch') != '8' or labels.get('org.opencontainers.image.revision') != 'c03302d15e26ad032a6df2048de5d505db5be48b':
        raise SystemExit('Unexpected capacity image revision.')
    images[kind] = item['Id']
(state / 'capacity-images.json').write_text(json.dumps(images, indent=2), encoding='utf-8')
for suffix, port in [('a', 47414), ('b', 47415)]:
    envfile = state / ('capacity-' + suffix + '.env')
    envfile.write_text(f"RUNTIME_DAEMON_IMAGE={images['daemon']}\nRUNTIME_GUEST_IMAGE={images['guest']}\nRUNTIME_PORT={port}\n", encoding='utf-8')
    subprocess.run(['docker', 'compose', '-p', 'jingjia-runtime-capacity-' + suffix, '--env-file', str(envfile),
                    '-f', str(root / 'compose.yaml'), 'up', '-d', '--wait', '--wait-timeout', '60'], check=True)
env = dict(os.environ, RUNTIME_CAPACITY_LIVE_TEST='1', RUNTIME_TEST_URL='http://127.0.0.1:47414',
           RUNTIME_TEST_SECOND_URL='http://127.0.0.1:47415', RUNTIME_TEST_TOKEN_FILE=str(state / 'daemon.token'),
           RUNTIME_TEST_GUEST_IMAGE=images['guest'], GOTMPDIR=str(root / '.build' / 'go-tmp'))
result = subprocess.run(['go', 'test', './pkg/runtimeadapter', '-run', '^TestLiveRuntimeCapacity$', '-count=1', '-v', '-timeout', '6m'],
                        cwd=root.parent.parent / 'backend', env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
output = result.stdout.decode('utf-8', errors='replace').replace((state / 'daemon.token').read_text().strip(), '[redacted]')
(state / 'capacity-live.log').write_text(output, encoding='utf-8')
print(output, end='')
sys.exit(result.returncode)
