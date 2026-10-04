"""Fault injection against the independent p8 fixture; never restarts Web nodes."""
import json
import os
import pathlib
import re
import subprocess
import sys

root = pathlib.Path(__file__).resolve().parent
state = root / '.state'
if not os.environ.get('RUNTIME_TEST_DATABASE_URL'):
    raise SystemExit('Set the isolated RUNTIME_TEST_DATABASE_URL.')
images = json.loads((state / 'capacity-images.json').read_text())
item = json.loads(subprocess.check_output(['docker', 'inspect', 'jingjia-runtime-capacity-a-daemon-1']))[0]
if item['Image'] != images['daemon'] or item['Config']['Labels'].get('com.docker.compose.project') != 'jingjia-runtime-capacity-a':
    raise SystemExit('Unexpected independent daemon fixture.')
env = dict(os.environ, RUNTIME_AGENT_FAULT_LIVE_TEST='1', RUNTIME_TEST_URL='http://127.0.0.1:47414',
           RUNTIME_TEST_TOKEN_FILE=str(state / 'daemon.token'), RUNTIME_MODEL_CONFIG=str(state / 'model.json'),
           RUNTIME_TEST_GUEST_IMAGE=images['guest'], GOTMPDIR=str(root / '.build' / 'go-tmp'))
result = subprocess.run(['go', 'test', './pkg/runtimeadapter', '-run', '^TestLiveAgentFaultRecovery$', '-count=1', '-v', '-timeout', '6m'],
                        cwd=root.parent.parent / 'backend', env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
output = result.stdout.decode('utf-8', errors='replace')
for secret in [(state / 'daemon.token').read_text().strip(), json.loads((state / 'model.json').read_text())['api_key']]:
    output = output.replace(secret, '[redacted]')
output = re.sub(r'invalid header:[A-Za-z0-9+/=_-]+', 'invalid header:[redacted upstream headers]', output)
(state / 'agent-faults-live.log').write_text(output, encoding='utf-8')
print(output, end='')
sys.exit(result.returncode)
