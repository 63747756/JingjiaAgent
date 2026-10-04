"""Run only the new real MCP transport/runtime acceptance against isolated state."""
import json
import os
import pathlib
import subprocess
import sys

root = pathlib.Path(__file__).resolve().parent
state = root / '.state'
if not os.environ.get('RUNTIME_TEST_DATABASE_URL'):
    raise SystemExit('RUNTIME_TEST_DATABASE_URL must name the isolated test database.')
images = json.loads((state / 'images.json').read_text())
env = dict(os.environ, RUNTIME_MCP_LIVE_TEST='1', RUNTIME_TEST_URL='http://127.0.0.1:47410',
    RUNTIME_TEST_TOKEN_FILE=str(state / 'daemon.token'), RUNTIME_MODEL_CONFIG=str(state / 'model.json'),
    RUNTIME_TEST_GUEST_IMAGE=images['guest'], RUNTIME_MCP_TRANSPORT_CONFIG=str(state / 'mcp-transports.json'),
    GOTMPDIR=str(root / '.build' / 'go-tmp'))
result = subprocess.run(['go', 'test', './pkg/runtimeadapter', '-run', '^TestLiveMCPTransports$', '-count=1', '-v', '-timeout', '8m'],
    cwd=root.parent.parent / 'backend', env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
output = result.stdout.decode('utf-8', errors='replace')
for secret in [(state / 'daemon.token').read_text().strip(),
    json.loads((state / 'model.json').read_text())['api_key'],
    json.loads((state / 'mcp-transports.json').read_text())['token']]:
    output = output.replace(secret, '[redacted]')
(state / 'mcp-transports-live.log').write_text(output, encoding='utf-8')
print(output, end='')
sys.exit(result.returncode)
