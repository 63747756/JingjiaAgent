"""Verify a real Linux 0600 root secret is read before irreversible UID drop."""
import os
from pathlib import Path
import subprocess
import sys
import time
import urllib.request

assert os.geteuid() == 0, "Run with startup-only SETUID/SETGID/DAC_OVERRIDE capabilities"
token = Path('/tmp/startup-token')
token.write_text('fixture-token-only-' + 'x' * 40)
token.chmod(0o600)
process = subprocess.Popen([sys.executable, '/opt/office/converter.py', '--token-file', str(token),
                            '--listen', '127.0.0.1'], start_new_session=True)
client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
try:
    for _ in range(50):
        try:
            with client.open('http://127.0.0.1:8090/health', timeout=1) as response:
                assert response.status == 200
            break
        except OSError:
            time.sleep(.1)
    else:
        raise RuntimeError('server did not start with private token')
    status = dict(line.split(':', 1) for line in Path(f'/proc/{process.pid}/status').read_text().splitlines() if ':' in line)
    assert status['Uid'].split() == ['10001'] * 4
    assert int(status['CapEff'].strip(), 16) == 0
    os.setgroups([]); os.setgid(10001); os.setuid(10001)
    try:
        token.read_text()
    except PermissionError:
        pass
    else:
        raise RuntimeError('worker identity can still read root-only secret')
    print('Linux 0600 token startup passed; server UID10001 CapEff0; worker secret read denied')
finally:
    # Parent drops to the same final UID, so it needs no signal capability.
    if os.geteuid() == 0:
        os.setgroups([]); os.setgid(10001); os.setuid(10001)
    process.terminate()
    process.wait(timeout=5)
