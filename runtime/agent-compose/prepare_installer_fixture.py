"""Add one owned installation slot to the isolated local Web PoC.

Only ignored PoC configuration is changed. Existing nodes, tasks and credentials
are preserved. The generated self-signed TLS material is for local acceptance.
"""
import datetime
import ipaddress
import json
import pathlib
import secrets
import uuid
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.x509.oid import NameOID

root = pathlib.Path(__file__).resolve().parent
state = root / '.state'
fixture_dir = state / 'installer'
fixture_dir.mkdir(exist_ok=True)
identity_file = fixture_dir / 'node.json'
if not identity_file.exists():
    identity_file.write_text(json.dumps({'id': str(uuid.uuid4())}))
identity = json.loads(identity_file.read_text())
owner = json.loads((state / 'web-node.json').read_text())
manifest = json.loads((state / 'installation-bundle/manifest.json').read_text())
token_file = fixture_dir / 'node.token'
if not token_file.exists():
    token_file.write_text(secrets.token_hex(32) + '\n'); token_file.chmod(0o600)
key_file, cert_file = fixture_dir / 'node.key', fixture_dir / 'node.crt'
if not key_file.exists() and not cert_file.exists():
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, 'local-runtime-installer')])
    now = datetime.datetime.now(datetime.timezone.utc)
    cert = (x509.CertificateBuilder().subject_name(name).issuer_name(name).public_key(key.public_key())
            .serial_number(x509.random_serial_number()).not_valid_before(now - datetime.timedelta(minutes=5))
            .not_valid_after(now + datetime.timedelta(days=7))
            .add_extension(x509.SubjectAlternativeName([x509.IPAddress(ipaddress.ip_address('127.0.0.1'))]), critical=False)
            .add_extension(x509.BasicConstraints(ca=True, path_length=0), critical=True)
            .sign(key, hashes.SHA256()))
    key_file.write_bytes(key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))
    cert_file.write_bytes(cert.public_bytes(serialization.Encoding.PEM))
    key_file.chmod(0o600); cert_file.chmod(0o600)
if not key_file.exists() or not cert_file.exists():
    raise SystemExit('Incomplete local TLS fixture. Restore both files without rotating a deployed node silently.')
node = {'id': identity['id'], 'url': 'https://127.0.0.1:47412', 'token_file': str(token_file),
        'ca_file': str(cert_file), 'guest_image': manifest['guest_image'], 'owner_id': owner['owner_id'],
        'team_id': owner['team_id'], 'install': True, 'install_listen': '127.0.0.1:47412',
        'install_cert_file': str(cert_file), 'install_key_file': str(key_file)}
configuration = {'nodes': [node], 'installer_manifest_file': str(state / 'installation-bundle/manifest.json'),
                 'installer_base_url': 'http://host.docker.internal:47420'}
(state / 'web-installer-config.json').write_text(json.dumps(configuration, indent=2))
cfg_file = state / 'web/config/server/config.yaml'
cfg = json.loads(cfg_file.read_text())
cfg['runtime']['nodes'] = [n for n in cfg['runtime']['nodes'] if n['id'] != node['id']] + [node]
for field in ('installer_manifest_file','installer_base_url'):
    cfg['runtime'][field] = configuration[field]
cfg_file.write_text(json.dumps(cfg, indent=2)); cfg_file.chmod(0o600)
print('Prepared one isolated HTTPS installation slot; original node configuration retained.')
