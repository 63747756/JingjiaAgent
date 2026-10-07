"""Test-only LDAPv3/TLS directory. Never install this service in production.

Implements the small BER surface used by the AD adapter, not an AD server.
State is re-read for every operation to exercise rename, move and failure cases.
Passwords, bind packets and search filters are deliberately never logged.
"""
import argparse
import base64
import hmac
import json
import pathlib
import re
import socket
import socketserver
import ssl
import time


MAX_PACKET = 1 << 20


def ber(tag, value):
    size = len(value)
    length = bytes([size]) if size < 128 else bytes([0x80 | ((size.bit_length() + 7) // 8)]) + size.to_bytes((size.bit_length() + 7) // 8, 'big')
    return bytes([tag]) + length + value


def number(value, tag=0x02):
    data = value.to_bytes(max(1, (value.bit_length() + 7) // 8), 'big')
    if data[0] & 0x80:
        data = b'\0' + data
    return ber(tag, data)


def octet(value):
    return ber(0x04, value.encode('utf-8') if isinstance(value, str) else value)


def unpack(data):
    """Return (tag, content, rest), rejecting truncated/unbounded BER values."""
    if len(data) < 2:
        raise ValueError('Truncated BER')
    count = data[1] & 0x7f
    offset = 2
    if data[1] & 0x80:
        if count == 0 or count > 4 or len(data) < 2 + count:
            raise ValueError('Invalid BER size')
        size = int.from_bytes(data[2:2 + count], 'big')
        offset += count
    else:
        size = count
    if size > MAX_PACKET or offset + size > len(data):
        raise ValueError('Truncated or oversized BER')
    return data[0], data[offset:offset + size], data[offset + size:]


def children(data):
    result = []
    while data:
        tag, value, data = unpack(data)
        result.append((tag, value))
    return result


def read_packet(connection):
    def exact(size):
        result = b''
        while len(result) < size:
            piece = connection.recv(size - len(result))
            if not piece:
                raise EOFError
            result += piece
        return result
    first = exact(2)
    if first[0] != 0x30:
        raise ValueError('LDAP envelope required')
    if first[1] & 0x80:
        count = first[1] & 0x7f
        if not 1 <= count <= 4:
            raise ValueError('Invalid BER envelope')
        extra = exact(count)
        size = int.from_bytes(extra, 'big')
        first += extra
    else:
        size = first[1]
    if size > MAX_PACKET:
        raise ValueError('Oversized LDAP envelope')
    return first + exact(size)


def message(identifier, operation):
    return ber(0x30, number(identifier) + operation)


def result(identifier, operation, code=0):
    return message(identifier, ber(operation, number(code, 0x0a) + octet('') + octet('')))


def values(entry, name):
    for key, value in entry.get('attributes', {}).items():
        if key.casefold() == name.casefold():
            if isinstance(value, dict) and 'base64' in value:
                return [base64.b64decode(value['base64'], validate=True)]
            return [item.encode('utf-8') for item in (value if isinstance(value, list) else [str(value)])]
    if name.casefold() == 'distinguishedname':
        return [entry['dn'].encode('utf-8')]
    return []


def split_dn(value, separator):
    parts, start, position = [], 0, 0
    while position < len(value):
        if value[position] == '\\':
            position += 2
            continue
        if value[position] == separator:
            parts.append(value[start:position])
            start = position + 1
        position += 1
    parts.append(value[start:])
    return parts


def decoded_dn_value(value):
    data = bytearray()
    position = 0
    while position < len(value):
        if value[position] != '\\':
            data.extend(value[position].encode('utf-8'))
            position += 1
        elif position + 2 < len(value) and re.fullmatch(r'[0-9a-fA-F]{2}', value[position + 1:position + 3]):
            data.append(int(value[position + 1:position + 3], 16))
            position += 3
        elif position + 1 < len(value):
            data.extend(value[position + 1].encode('utf-8'))
            position += 2
        else:
            raise ValueError('Invalid DN escape')
    return data.decode('utf-8').casefold()


def canonical_dn(value):
    """Normalize RFC4514 hex UTF-8 escapes and equivalent attribute casing."""
    result = []
    for rdn in split_dn(value, ','):
        attributes = []
        for attribute in split_dn(rdn, '+'):
            name, found, encoded = attribute.partition('=')
            if not found:
                raise ValueError('Invalid DN attribute')
            attributes.append((name.strip().casefold(), decoded_dn_value(encoded)))
        result.append(tuple(sorted(attributes)))
    return tuple(result)


def recursive_membership(entry, target, entries, visited=None):
    visited = set() if visited is None else visited
    for group in values(entry, 'memberOf'):
        dn = canonical_dn(group.decode('utf-8'))
        if dn == canonical_dn(target):
            return True
        if dn in visited:
            continue
        visited.add(dn)
        parent = next((item for item in entries if canonical_dn(item['dn']) == dn), None)
        if parent and recursive_membership(parent, target, entries, visited):
            return True
    return False


def matches(entry, tag, content, entries):
    if tag in (0xa0, 0xa1):
        checks = [matches(entry, kind, data, entries) for kind, data in children(content)]
        return all(checks) if tag == 0xa0 else any(checks)
    if tag == 0xa2:
        parts = children(content)
        return len(parts) == 1 and not matches(entry, *parts[0], entries)
    if tag == 0x87:
        return bool(values(entry, content.decode('utf-8')))
    if tag in (0xa3, 0xa5, 0xa6, 0xa8):
        parts = children(content)
        if len(parts) != 2:
            return False
        name, expected = parts[0][1].decode('utf-8'), parts[1][1]
        return any(value.lower() == expected.lower() for value in values(entry, name))
    if tag == 0xa9:
        parts = dict(children(content))
        rule = parts.get(0x81, b'').decode('utf-8')
        name = parts.get(0x82, b'').decode('utf-8')
        expected = parts.get(0x83, b'')
        if rule == '1.2.840.113556.1.4.1941' and name.casefold() == 'memberof':
            return recursive_membership(entry, expected.decode('utf-8'), entries)
        if rule == '1.2.840.113556.1.4.803':
            try:
                mask = int(expected)
                return any(int(value) & mask == mask for value in values(entry, name))
            except ValueError:
                return False
        return False
    # The adapter does not issue substring matching; unsupported filters fail closed.
    return False


def in_scope(dn, base, scope):
    dn, base = canonical_dn(dn), canonical_dn(base)
    if scope == 0:
        return dn == base
    if scope == 2:
        return len(dn) >= len(base) and dn[-len(base):] == base
    if scope == 1:
        return len(dn) == len(base) + 1 and dn[-len(base):] == base
    return False


def entry_response(identifier, entry, requested):
    names = list(entry['attributes']) + ['distinguishedName'] if not requested or '*' in requested else requested
    attrs = []
    for name in names:
        found = values(entry, name)
        if found:
            attrs.append(ber(0x30, octet(name) + ber(0x31, b''.join(octet(value) for value in found))))
    return message(identifier, ber(0x64, octet(entry['dn']) + ber(0x30, b''.join(attrs))))


class DirectoryHandler(socketserver.BaseRequestHandler):
    def handle(self):
        self.request.settimeout(20)
        bound = False
        while True:
            try:
                _, envelope, _ = unpack(read_packet(self.request))
                packets = children(envelope)
                identifier = int.from_bytes(packets[0][1], 'big')
                operation, content = packets[1]
                state = json.loads(self.server.state_file.read_text(encoding='utf-8'))
                time.sleep(min(float(state.get('delay_seconds', 0)), 30))
                if operation == 0x60:
                    fields = children(content)
                    username = fields[1][1].decode('utf-8')
                    password = fields[2][1]
                    entry = next((item for item in state['entries'] if canonical_dn(item['dn']) == canonical_dn(username)), None)
                    if canonical_dn(username) == canonical_dn(state['bind_dn']):
                        expected = state['bind_password'].encode('utf-8')
                        valid = bool(password) and hmac.compare_digest(password, expected)
                    else:
                        valid = bool(entry and password and not entry.get('bind_disabled'))
                        if valid:
                            valid = hmac.compare_digest(password, entry.get('password', '').encode('utf-8'))
                    bound = bool(valid)
                    self.request.sendall(result(identifier, 0x61, 0 if valid else 49))
                elif operation == 0x63:
                    if not bound:
                        self.request.sendall(result(identifier, 0x65, 50))
                        continue
                    fields = children(content)
                    base = fields[0][1].decode('utf-8')
                    scope = int.from_bytes(fields[1][1], 'big')
                    filter_tag, filter_data = fields[6]
                    requested = [data.decode('utf-8') for _, data in children(fields[7][1])]
                    if state.get('fail_ou_query') and base.casefold().startswith('ou=') and scope == 0:
                        self.request.sendall(result(identifier, 0x65, 50))
                        continue
                    found_base = any(canonical_dn(item['dn']) == canonical_dn(base) for item in state['entries'])
                    if scope == 0 and not found_base:
                        self.request.sendall(result(identifier, 0x65, 32))
                        continue
                    selected = [item for item in state['entries'] if in_scope(item['dn'], base, scope)
                                and matches(item, filter_tag, filter_data, state['entries'])]
                    for entry in selected:
                        self.request.sendall(entry_response(identifier, entry, requested))
                    self.request.sendall(result(identifier, 0x65))
                elif operation == 0x42:
                    return
                elif operation == 0x50:
                    continue
                else:
                    return
            except (EOFError, OSError, ValueError, KeyError, IndexError, TypeError):
                return


class DirectoryServer(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True

    def get_request(self):
        connection, address = super().get_request()
        try:
            return self.tls.wrap_socket(connection, server_side=True), address
        except (ssl.SSLError, OSError):
            connection.close()
            raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--state', type=pathlib.Path, required=True)
    parser.add_argument('--listen', default='127.0.0.1:1636')
    parser.add_argument('--acknowledge-test-fixture', action='store_true', required=True)
    args = parser.parse_args()
    host, port = args.listen.rsplit(':', 1)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    context.load_cert_chain(args.state / 'server.crt', args.state / 'server.key')
    with DirectoryServer((host, int(port)), DirectoryHandler) as server:
        server.state_file = args.state / 'directory.json'
        server.tls = context
        print('Test-only LDAPS fixture ready.', flush=True)
        server.serve_forever()


if __name__ == '__main__':
    main()
