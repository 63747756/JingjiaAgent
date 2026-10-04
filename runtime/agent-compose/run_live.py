"""Run real adapter/daemon/Guest/model checks against the isolated PoC stack.

Requires RUNTIME_TEST_DATABASE_URL pointing to an expendable PostgreSQL DB.
The test creates/drops its own schema and restarts only the named PoC daemon.
"""
import json
import os
import pathlib
import re
import subprocess
import sys

root = pathlib.Path(__file__).resolve().parent
if not os.environ.get('RUNTIME_TEST_DATABASE_URL'):
    raise SystemExit('RUNTIME_TEST_DATABASE_URL must name an isolated test database')
state = root / '.state'
images = json.loads((state / 'images.json').read_text())
env = dict(os.environ, RUNTIME_LIVE_TEST='1',
    RUNTIME_TEST_URL='http://127.0.0.1:47410',
    RUNTIME_TEST_TOKEN_FILE=str(state / 'daemon.token'),
    RUNTIME_MODEL_CONFIG=str(state / 'model.json'),
    RUNTIME_TEST_GUEST_IMAGE=images['guest'],
    RUNTIME_TEST_DAEMON_CONTAINER='jingjia-runtime-poc-daemon-1')
result = subprocess.run(['go','test','./pkg/runtimeadapter','-run','^TestLiveRuntime$','-count=1','-v','-timeout','10m'],
                        cwd=root.parent.parent / 'backend', env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
output = result.stdout.decode('utf-8', errors='replace')
for secret in [(state / 'daemon.token').read_text().strip(),json.loads((state / 'model.json').read_text())['api_key']]:
    output = output.replace(secret, '[redacted]')
output = re.sub(r'invalid header:[A-Za-z0-9+/=_-]+', 'invalid header:[redacted upstream headers]', output)
(state / 'live-test.log').write_text(output, encoding='utf-8')
print(output, end='')
sys.exit(result.returncode)
