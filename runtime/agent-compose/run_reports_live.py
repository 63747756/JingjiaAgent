"""Focused real Docker reports/preparation faults; no model invocation or daemon restart."""
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
env = dict(os.environ, RUNTIME_REPORT_LIVE_TEST='1', RUNTIME_TEST_URL='http://127.0.0.1:47410',
           RUNTIME_TEST_TOKEN_FILE=str(state / 'daemon.token'), RUNTIME_TEST_GUEST_IMAGE=images['guest'],
           GOTMPDIR=str(root / '.build' / 'go-tmp'))
result = subprocess.run(['go', 'test', './pkg/runtimeadapter', '-run', '^TestLiveRuntimeReports$',
                         '-count=1', '-v', '-timeout', '5m'], cwd=root.parent.parent / 'backend', env=env,
                        stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
output = result.stdout.decode('utf-8', errors='replace')
output = output.replace((state / 'daemon.token').read_text().strip(), '[redacted]')
(state / 'reports-live.log').write_text(output, encoding='utf-8')
print(output, end='')
sys.exit(result.returncode)
