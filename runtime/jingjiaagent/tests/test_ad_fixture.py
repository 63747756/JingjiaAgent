"""Wire-level checks for the test directory; these are not real AD acceptance."""
import base64
import json
import pathlib
import socket
import ssl
import tempfile
import threading
import unittest

import ad_fixture as fixture
from prepare_ad_fixture import prepare


class FixtureWireTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='jingjiaagent-ad-fixture-')
        self.addCleanup(self.temporary.cleanup)
        self.state = pathlib.Path(self.temporary.name)
        prepare(self.state)
        self.directory = json.loads((self.state / 'directory.json').read_text(encoding='utf-8'))
        self.users = json.loads((self.state / 'users.json').read_text(encoding='utf-8'))
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        context.load_cert_chain(self.state / 'server.crt', self.state / 'server.key')
        self.server = fixture.DirectoryServer(('127.0.0.1', 0), fixture.DirectoryHandler)
        self.server.tls = context
        self.server.state_file = self.state / 'directory.json'
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.close_server)

    def close_server(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(2)

    def connect(self, trusted=True):
        context = ssl.create_default_context(cafile=str(self.state / 'ca.pem') if trusted else None)
        plain = socket.create_connection(self.server.server_address, timeout=2)
        try:
            result = context.wrap_socket(plain, server_hostname='localhost')
        except Exception:
            plain.close()
            raise
        self.addCleanup(result.close)
        return result

    def receive(self, connection):
        _, envelope, _ = fixture.unpack(fixture.read_packet(connection))
        return fixture.children(envelope)[1]

    def bind(self, connection, dn=None, password=None):
        if dn is None:
            dn, password = self.directory['bind_dn'], self.directory['bind_password']
        request = fixture.ber(0x60, fixture.number(3) + fixture.octet(dn) + fixture.ber(0x80, password.encode()))
        connection.sendall(fixture.message(1, request))
        operation, reply = self.receive(connection)
        self.assertEqual(operation, 0x61)
        return int.from_bytes(fixture.children(reply)[0][1], 'big')

    def search(self, connection, base, filter_bytes, scope=2, attributes=None):
        attrs = ['objectGUID', 'sAMAccountName', 'userAccountControl', 'mail'] if attributes is None else attributes
        search = fixture.ber(0x63, fixture.octet(base) + fixture.number(scope, 0x0a) + fixture.number(0, 0x0a)
                             + fixture.number(2) + fixture.number(5) + fixture.ber(0x01, b'\0') + filter_bytes
                             + fixture.ber(0x30, b''.join(fixture.octet(attr) for attr in attrs)))
        connection.sendall(fixture.message(2, search))
        entries = []
        while True:
            operation, reply = self.receive(connection)
            if operation == 0x65:
                return entries, int.from_bytes(fixture.children(reply)[0][1], 'big')
            self.assertEqual(operation, 0x64)
            fields = fixture.children(reply)
            entry = {'dn': fields[0][1].decode('utf-8'), 'attributes': {}}
            for _, attr in fixture.children(fields[1][1]):
                name, values = fixture.children(attr)
                entry['attributes'][name[1].decode()] = [value for _, value in fixture.children(values[1])]
            entries.append(entry)

    @staticmethod
    def equality(name, value):
        return fixture.ber(0xa3, fixture.octet(name) + fixture.octet(value))

    def user_filter(self, name):
        in_group = fixture.ber(0xa9, fixture.ber(0x81, b'1.2.840.113556.1.4.1941')
                               + fixture.ber(0x82, b'memberOf')
                               + fixture.ber(0x83, self.directory['allowed_group_dns'][0].encode()))
        disabled = fixture.ber(0xa9, fixture.ber(0x81, b'1.2.840.113556.1.4.803')
                               + fixture.ber(0x82, b'userAccountControl') + fixture.ber(0x83, b'2'))
        return fixture.ber(0xa0, self.equality('objectCategory', 'person') + self.equality('objectClass', 'user')
                           + self.equality('sAMAccountName', name) + fixture.ber(0xa2, disabled)
                           + fixture.ber(0xa1, in_group))

    def test_nested_membership_raw_guid_and_independent_user_bind(self):
        query = self.connect()
        self.assertEqual(self.bind(query), 0)
        found, code = self.search(query, self.directory['base_dn'], self.user_filter('bob'))
        self.assertEqual(code, 0)
        self.assertEqual(len(found), 1)
        self.assertEqual(len(found[0]['attributes']['objectGUID'][0]), 16)
        employee = self.connect()
        self.assertEqual(self.bind(employee, found[0]['dn'], self.users['bob']['password']), 0)
        self.assertEqual(self.bind(employee, found[0]['dn'], 'wrong-password'), 49)

    def test_disabled_outsider_and_noemail(self):
        query = self.connect()
        self.assertEqual(self.bind(query), 0)
        for username in ('disabled', 'outsider', 'missing'):
            found, code = self.search(query, self.directory['base_dn'], self.user_filter(username))
            self.assertEqual((found, code), ([], 0))
        found, code = self.search(query, self.directory['base_dn'], self.user_filter('noemail'))
        self.assertEqual((len(found), code), (1, 0))
        self.assertNotIn('mail', found[0]['attributes'])

    def test_base_ou_lookup_and_failure_code(self):
        query = self.connect()
        self.assertEqual(self.bind(query), 0)
        ou = 'OU=研发\\,工具组,OU=公司,' + self.directory['base_dn']
        found, code = self.search(query, ou, self.equality('objectClass', 'organizationalUnit'), 0, ['objectGUID'])
        self.assertEqual((len(found), code), (1, 0))
        expected = next(entry for entry in self.directory['entries'] if entry['dn'] == ou)
        self.assertEqual(found[0]['attributes']['objectGUID'][0], base64.b64decode(expected['attributes']['objectGUID']['base64']))
        # go-ldap DN.String hex-escapes each non-ASCII UTF-8 byte.
        canonical_go_dn = ''.join('\\' + format(byte, '02x') if byte >= 128 else chr(byte)
                                 for byte in ou.encode('utf-8')).replace('OU=', 'ou=').replace('DC=', 'dc=')
        found_go, code_go = self.search(query, canonical_go_dn,
                                       self.equality('objectClass', 'organizationalUnit'), 0, ['objectGUID'])
        self.assertEqual((len(found_go), code_go), (1, 0))
        self.assertEqual(found_go[0]['attributes']['objectGUID'], found[0]['attributes']['objectGUID'])
        self.directory['fail_ou_query'] = True
        (self.state / 'directory.json').write_text(json.dumps(self.directory), encoding='utf-8')
        self.assertEqual(self.search(query, ou, self.equality('objectClass', 'organizationalUnit'), 0), ([], 50))

    def test_wrong_credentials_search_without_bind_and_unknown_ca_fail(self):
        query = self.connect()
        self.assertEqual(self.bind(query, self.directory['bind_dn'], 'wrong'), 49)
        self.assertEqual(self.search(query, self.directory['base_dn'], fixture.ber(0x87, b'objectClass')), ([], 50))
        with self.assertRaises(ssl.SSLCertVerificationError):
            self.connect(trusted=False)

    def test_fixture_prepare_never_replaces_existing_state(self):
        before = {path.name: path.read_bytes() for path in self.state.iterdir()}
        with self.assertRaises(SystemExit):
            prepare(self.state)
        self.assertEqual(before, {path.name: path.read_bytes() for path in self.state.iterdir()})


if __name__ == '__main__':
    unittest.main()
