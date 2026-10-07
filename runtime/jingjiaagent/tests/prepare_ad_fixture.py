"""Create private mock-directory credentials and TLS material for AD acceptance."""
import argparse
import base64
import datetime
import ipaddress
import json
import pathlib
import secrets
import uuid

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.x509.oid import ExtendedKeyUsageOID, NameOID


def prepare(state):
    state.mkdir(parents=True, exist_ok=True)
    if any(state.iterdir()):
        raise SystemExit('Fixture directory must be empty; existing credentials and scenario state are preserved.')

    def write(name, content):
        path = state / name
        path.write_text(content, encoding='utf-8', newline='\n')
        path.chmod(0o600)

    now = datetime.datetime.now(datetime.timezone.utc)
    ca_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, 'JingjiaAgent isolated AD test CA')])
    ca = (x509.CertificateBuilder().subject_name(subject).issuer_name(subject).public_key(ca_key.public_key())
          .serial_number(x509.random_serial_number()).not_valid_before(now-datetime.timedelta(minutes=5))
          .not_valid_after(now+datetime.timedelta(days=14))
          .add_extension(x509.BasicConstraints(ca=True, path_length=0), critical=True)
          .add_extension(x509.SubjectKeyIdentifier.from_public_key(ca_key.public_key()), critical=False)
          .add_extension(x509.AuthorityKeyIdentifier.from_issuer_public_key(ca_key.public_key()), critical=False)
          .add_extension(x509.KeyUsage(digital_signature=True, content_commitment=False, key_encipherment=False,
                                       data_encipherment=False, key_agreement=False, key_cert_sign=True,
                                       crl_sign=True, encipher_only=None, decipher_only=None), critical=True)
          .sign(ca_key, hashes.SHA256()))
    server_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    server = (x509.CertificateBuilder().subject_name(x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, 'ad-fixture')]))
              .issuer_name(subject).public_key(server_key.public_key()).serial_number(x509.random_serial_number())
              .not_valid_before(now-datetime.timedelta(minutes=5)).not_valid_after(now+datetime.timedelta(days=14))
              .add_extension(x509.BasicConstraints(ca=False, path_length=None), critical=True)
              .add_extension(x509.SubjectKeyIdentifier.from_public_key(server_key.public_key()), critical=False)
              .add_extension(x509.AuthorityKeyIdentifier.from_issuer_public_key(ca_key.public_key()), critical=False)
              .add_extension(x509.SubjectAlternativeName([x509.DNSName('ad-fixture'), x509.DNSName('localhost'),
                                                          x509.IPAddress(ipaddress.ip_address('127.0.0.1'))]), critical=False)
              .add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.SERVER_AUTH]), critical=False)
              .sign(ca_key, hashes.SHA256()))
    write('ca.pem', ca.public_bytes(serialization.Encoding.PEM).decode())
    write('server.crt', server.public_bytes(serialization.Encoding.PEM).decode())
    write('server.key', server_key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8,
                                                serialization.NoEncryption()).decode())
    base = 'DC=fixture,DC=test'
    company = 'OU=公司,' + base
    research = 'OU=研发中心,' + company
    software = 'OU=软件部,' + research
    support = 'OU=技术支持,' + company
    same_name = 'OU=软件部,' + support
    escaped = 'OU=研发\\,工具组,' + company
    allowed = 'CN=JingjiaAgent Allowed,' + base
    nested = 'CN=Nested Employees,' + base
    entries = [{'dn': base, 'attributes': {'objectClass': ['top', 'domain']}}]

    def guid(key):
        return {'base64': base64.b64encode(uuid.uuid5(uuid.NAMESPACE_URL, 'https://fixture.invalid/' + key).bytes_le).decode()}

    for dn in (company, research, software, support, same_name, escaped):
        entries.append({'dn': dn, 'attributes': {'objectClass': ['top', 'organizationalUnit'], 'objectGUID': guid(dn)}})
    entries.extend([
        {'dn': allowed, 'attributes': {'objectClass': ['top', 'group']}},
        {'dn': nested, 'attributes': {'objectClass': ['top', 'group'], 'memberOf': [allowed]}},
    ])
    users = {}
    for name, department, groups, disabled, email in [
        ('alice', software, [allowed], False, 'alice@fixture.test'),
        ('bob', software, [nested], False, 'bob@fixture.test'),
        ('charlie', same_name, [allowed], False, 'charlie@fixture.test'),
        ('escaped', escaped, [allowed], False, 'escaped@fixture.test'),
        ('noou', 'CN=Users,'+base, [allowed], False, 'noou@fixture.test'),
        ('noemail', software, [allowed], False, None),
        ('disabled', software, [allowed], True, 'disabled@fixture.test'),
        ('outsider', software, [], False, 'outsider@fixture.test'),
        ('locked', software, [allowed], False, 'locked@fixture.test'),
    ]:
        password = secrets.token_urlsafe(20) + '_Test!'
        attributes = {'objectClass': ['top', 'person', 'organizationalPerson', 'user'], 'objectCategory': 'person',
                      'objectGUID': guid('user/' + name), 'sAMAccountName': name,
                      'displayName': '测试员工 ' + name, 'userAccountControl': '514' if disabled else '512',
                      'memberOf': groups}
        if email:
            attributes['mail'] = email
        entries.append({'dn': 'CN=' + name + ',' + department, 'password': password,
                        'bind_disabled': disabled or name == 'locked', 'attributes': attributes})
        users[name] = {'username': name, 'password': password}
    directory = {'bind_dn': 'CN=Directory Reader,' + base, 'bind_password': secrets.token_urlsafe(28),
                 'base_dn': base, 'allowed_group_dns': [allowed], 'entries': entries}
    write('directory.json', json.dumps(directory, ensure_ascii=False, indent=2))
    write('users.json', json.dumps(users, ensure_ascii=False, indent=2))
    write('config.json', json.dumps({'enabled': False, 'display_name': '测试域账号',
                                     'url': 'ldaps://ad-fixture:1636', 'base_dn': base,
                                     'bind_dn': directory['bind_dn'], 'bind_password': directory['bind_password'],
                                     'ca_pem': ca.public_bytes(serialization.Encoding.PEM).decode(),
                                     'allowed_group_dns': [allowed]}, ensure_ascii=False, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--state', type=pathlib.Path, required=True)
    args = parser.parse_args()
    prepare(args.state.resolve())
    print('Private AD test fixture prepared. Credentials are only in the private fixture directory.')


if __name__ == '__main__':
    main()
