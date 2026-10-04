"""Prepare private S3 storage for the isolated original Web acceptance fixture.

Requires an already installed, immutable MinIO image. Both API and console
stay on loopback. Docker Guests use the Docker Desktop host gateway. All keys
and fixture state remain in the ignored .state directory.
"""
import argparse
import json
import pathlib
import secrets
import subprocess
import time
import urllib.request

root = pathlib.Path(__file__).resolve().parent
state = root / '.state'
state.mkdir(exist_ok=True)
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--image', required=True, help='Installed sha256 image ID or repository digest')
args = parser.parse_args()
if not (args.image.startswith('sha256:') or '@sha256:' in args.image):
    raise SystemExit('An immutable image ID or digest is required.')
image = json.loads(subprocess.check_output(['docker', 'image', 'inspect', args.image]))[0]
image_id = image['Id']
name = 'jingjia-runtime-web-storage'
record_file = state / 'web-storage.json'
if record_file.exists():
    record = json.loads(record_file.read_text())
    if record['image'] != image_id:
        raise SystemExit('Existing storage fixture differs; preserve its data and review the configuration before changing it.')
else:
    record = {'image': image_id, 'access_key': 'webpoc-' + secrets.token_hex(10),
              'secret_key': secrets.token_urlsafe(36)}
    record_file.write_text(json.dumps(record, indent=2), encoding='utf-8')
    record_file.chmod(0o600)
env_file = state / 'web-storage.env'
env_file.write_text('MINIO_ROOT_USER=' + record['access_key'] + '\nMINIO_ROOT_PASSWORD=' + record['secret_key'] +
                   '\nMINIO_API_CORS_ALLOW_ORIGIN=http://127.0.0.1:47430\n', encoding='utf-8')
env_file.chmod(0o600)
present = subprocess.run(['docker', 'container', 'inspect', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
if present.returncode:
    subprocess.run(['docker', 'run', '-d', '--name', name, '--label', 'monkeycode.runtime.poc=1',
                    '--restart', 'unless-stopped', '--env-file', str(env_file),
                    '-p', '127.0.0.1:47590:9000',
                    '-p', '127.0.0.1:47591:9001', '--mount', 'type=volume,source=jingjia-runtime-web-storage,target=/data',
                    image_id, 'server', '/data', '--console-address', ':9001'], check=True, stdout=subprocess.DEVNULL)
else:
    details = json.loads(subprocess.check_output(['docker', 'container', 'inspect', name]))[0]
    if details['Image'] != image_id or details['Config']['Labels'].get('monkeycode.runtime.poc') != '1':
        raise SystemExit('Named container is not the expected storage fixture.')
    expected_ports = {'9000/tcp': [{'HostIp':'127.0.0.1','HostPort':'47590'}],
                      '9001/tcp': [{'HostIp':'127.0.0.1','HostPort':'47591'}]}
    if details['HostConfig']['PortBindings'] != expected_ports:
        raise SystemExit('Existing storage fixture does not use the expected loopback-only ports.')
    settings = dict(value.split('=',1) for value in details['Config']['Env'] if '=' in value)
    if settings.get('MINIO_ROOT_USER') != record['access_key'] or settings.get('MINIO_ROOT_PASSWORD') != record['secret_key']:
        raise SystemExit('Existing storage fixture credentials differ from its saved private state.')
    subprocess.run(['docker', 'start', name], check=True, stdout=subprocess.DEVNULL)
client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
for attempt in range(30):
    try:
        with client.open('http://127.0.0.1:47590/minio/health/ready', timeout=2) as response:
            if response.status == 200:
                break
    except OSError:
        time.sleep(0.2)
else:
    raise SystemExit('Storage fixture did not become ready.')
config_file = state / 'web-storage-config.json'
config_file.write_text(json.dumps({'enabled': True, 'provider': 's3', 'force_path_style': True, 'init_bucket': True,
    'endpoint': 'http://127.0.0.1:47590', 'access_endpoint': 'http://127.0.0.1:47590',
    'agent_access_endpoint': 'http://host.docker.internal:47590',
    'access_key': record['access_key'], 'access_key_secret': record['secret_key'],
    'bucket': 'monkeycode-web-poc', 'region': 'us-east-1', 'presign_expires': '1h',
    'temp_prefix': 'temp', 'avatar_prefix': 'avatar', 'spec_prefix': 'spec', 'repo_prefix': 'repo'}, indent=2), encoding='utf-8')
config_file.chmod(0o600)
print('Prepared isolated S3 storage; credentials and immutable image are recorded in ignored .state files.')
