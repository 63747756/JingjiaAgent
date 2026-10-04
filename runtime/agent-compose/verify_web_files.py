"""Verify original authenticated download APIs after the Web upload/edit flow.

This supplements browser checks; it does not certify the native save picker.
Only reads the dedicated local PoC and synthetic file fixtures.
"""
import hashlib
import http.cookiejar
import json
import os
import pathlib
import sys
import urllib.parse
import urllib.request

root = pathlib.Path(__file__).resolve().parent
state = pathlib.Path(os.environ.get('RUNTIME_WEB_STATE_DIRECTORY',str(root/'.state'))).resolve()
if not state.is_relative_to((root/'.state').resolve()):
    raise SystemExit('File acceptance state must stay in the private test directory')
if len(sys.argv) != 2 or not sys.argv[1].startswith('agent_'):
    raise SystemExit('Usage: verify_web_files.py <isolated environment ID>')
environment = sys.argv[1]
account = json.loads((state / 'web-account.json').read_text())
client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
origin = os.environ.get('RUNTIME_WEB_BASE_URL','http://127.0.0.1:47420')
if origin not in ('http://127.0.0.1:47420','http://127.0.0.1:47424'):
    raise SystemExit('Only the two named local acceptance APIs are allowed')
request = urllib.request.Request(origin + '/api/v1/users/password-login',
    data=json.dumps(account).encode(), headers={'Content-Type':'application/json'})
with client.open(request, timeout=10) as response:
    if json.load(response).get('code') != 0:
        raise SystemExit('Original user login failed')
expected = {
    '中文上传验收.txt': '中文文件编辑验收\nWEB_EDIT_OK\n'.encode(),
    '二进制验收.bin': (state / 'web-files' / '二进制验收.bin').read_bytes(),
    '空文件.txt': b'',
}
report = []
for name, content in expected.items():
    query = urllib.parse.urlencode({'id':environment,'path':'/workspace/'+name})
    with client.open(origin+'/api/v1/users/files/download?'+query,timeout=20) as response:
        result = response.read()
        length = response.headers.get('Content-Length')
        if result != content or length != str(len(content)):
            raise SystemExit('Download content/size mismatch: '+name)
    report.append({'file':name,'bytes':len(result),'sha256':hashlib.sha256(result).hexdigest(),'passed':True})
(state / 'web-files-report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8')
print(json.dumps(report,ensure_ascii=False,indent=2))
