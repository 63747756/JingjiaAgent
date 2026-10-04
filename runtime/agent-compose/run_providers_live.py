"""Real pinned provider CLIs on an independent node; private config stays local."""
import json
import os
import pathlib
import re
import subprocess
import sys

root = pathlib.Path(__file__).resolve().parent
state = root / '.state'
if not os.environ.get('RUNTIME_TEST_DATABASE_URL'):
    raise SystemExit('Set an isolated RUNTIME_TEST_DATABASE_URL.')
images = {}
revision = json.loads((root / 'source.lock.json').read_text())['patch_revision']
for kind, tag in [('daemon', f'jingjia-agent-runtime:c03302d-p{revision}'), ('guest', f'jingjia-agent-guest:c03302d-p{revision}')]:
    item = json.loads(subprocess.check_output(['docker', 'image', 'inspect', tag]))[0]
    if item['Config']['Labels'].get('monkeycode.runtime.patch') != str(revision):
        raise SystemExit('Unexpected provider image revision.')
    images[kind] = item['Id']
(state / 'provider-images.json').write_text(json.dumps(images, indent=2), encoding='utf-8')
envfile = state / 'providers.env'
envfile.write_text(f"RUNTIME_DAEMON_IMAGE={images['daemon']}\nRUNTIME_GUEST_IMAGE={images['guest']}\nRUNTIME_PORT=47416\n", encoding='utf-8')
subprocess.run(['docker', 'compose', '-p', 'jingjia-runtime-parity', '--env-file', str(envfile), '-f', str(root / 'compose.yaml'), 'up', '-d', '--wait', '--wait-timeout', '60'], check=True)
env = dict(os.environ, RUNTIME_PROVIDERS_LIVE_TEST='1', RUNTIME_TEST_URL='http://127.0.0.1:47416',
           RUNTIME_TEST_TOKEN_FILE=str(state / 'daemon.token'), RUNTIME_MODEL_CONFIG=str(state / 'model.json'),
           RUNTIME_TEST_GUEST_IMAGE=images['guest'], GOTMPDIR=str(root / '.build/go-tmp'))
result = subprocess.run(['go', 'test', './pkg/runtimeadapter', '-run', '^TestLiveRuntimeProviders$', '-count=1', '-v', '-timeout', '7m'], cwd=root.parent.parent / 'backend', env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
output = result.stdout.decode('utf-8', errors='replace')
for secret in [(state / 'daemon.token').read_text().strip(), json.loads((state / 'model.json').read_text())['api_key']]:
    output = output.replace(secret, '[redacted]')
output = re.sub(r'invalid header:[A-Za-z0-9+/=_-]+', 'invalid header:[redacted upstream headers]', output)
(state / 'providers-live.log').write_text(output, encoding='utf-8')
mode = 'controls' if env.get('RUNTIME_PROVIDER_CONTROLS_LIVE_TEST') == '1' else 'assets' if env.get('RUNTIME_PROVIDER_ASSETS_LIVE_TEST') == '1' else 'basic'
(state / f"providers-p{revision}-{mode}.log").write_text(output, encoding='utf-8')
print(output, end='')
sys.exit(result.returncode)
