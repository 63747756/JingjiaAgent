"""Task-scoped Git credential helper; no caching or broad node credentials."""
import json
import pathlib
import sys
import urllib.request
from urllib.parse import urlsplit, quote, unquote


def target_matches(repo, values):
    expected = urlsplit(repo)
    actual = urlsplit(values.get('protocol', '')+'://'+values.get('host', '')+'/'+values.get('path', '').lstrip('/'))
    clean = lambda path: quote(unquote(path).rstrip('/').removesuffix('.git'), safe='/')
    return (expected.scheme in ('http', 'https') and actual.scheme == expected.scheme
        and expected.netloc.lower() == actual.netloc.lower() and not expected.username and not actual.username
        and not expected.query and not actual.query and not expected.fragment and not actual.fragment
        and clean(expected.path) == clean(actual.path) and clean(expected.path) != '/')


def get(config, values):
    if not target_matches(config['repo'], values):
        return None
    payload = {key: values.get(key, '') for key in ('protocol', 'host', 'path')}
    payload.update(task_id=config['task_id'], vm_id=config['vm_id'])
    req = urllib.request.Request(config['endpoint'], data=json.dumps(payload).encode(),
        headers={'Content-Type': 'application/json', 'Authorization': 'Bearer '+config['token']})
    # Never follow redirects with a credential or use inherited HTTP proxies.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *_): return None
    http = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    with http.open(req, timeout=15) as response:
        result = json.load(response)
    data = result.get('data') or {}
    if result.get('code') != 0 or data.get('error') or not data.get('username') or not data.get('password'):
        return None
    if any('\n' in data[key] or '\r' in data[key] for key in ('username','password')):
        return None
    return data


def main():
    if len(sys.argv) != 2 or sys.argv[1] != 'get':
        return # Git store/erase cannot persist a PAT.
    values = {}
    for line in sys.stdin.read(65537).splitlines():
        key, separator, value = line.partition('=')
        if separator: values[key] = value
    try:
        result = get(json.loads(pathlib.Path(__file__).with_suffix('.json').read_text()), values)
        if result:
            print('username='+result['username'])
            print('password='+result['password'])
            print()
    except Exception:
        return # Do not put authentication headers or provider errors in logs.


if __name__ == '__main__':
    main()
