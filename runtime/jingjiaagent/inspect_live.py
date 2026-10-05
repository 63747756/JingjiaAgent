"""Print only run lifecycle diagnostics, redacting configured credentials."""
import json
import pathlib
import re
import urllib.request

from linux_web_common import state
token = (state / 'daemon.token').read_text().strip()
key = json.loads((state / 'model.json').read_text())['api_key']

def safe(value):
    output = json.dumps(value, ensure_ascii=False).replace(key,'[redacted]').replace(token,'[redacted]')
    # Some gateways reflect a base64 HTTP request, including auth headers.
    return re.sub(r'invalid header:[A-Za-z0-9+/=_-]+','invalid header:[redacted upstream headers]',output)

def rpc(method, body):
    req = urllib.request.Request('http://127.0.0.1:47410/agentcompose.v2.RunService/' + method,
        data=json.dumps(body).encode(), headers={'Content-Type':'application/json','Authorization':'Bearer '+token})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.load(r)

for run in rpc('ListRuns', {'limit':8}).get('runs', []):
    rid = run['runId']
    print(json.dumps({k:run.get(k) for k in ['runId','sandboxId','status','createdAt']}, ensure_ascii=False))
    detail = rpc('GetRun', {'runId':rid}).get('run', {})
    if run.get('status') == 'RUN_STATUS_FAILED':
        print(safe(detail)[:6000])
    events = rpc('ListRunEvents', {'runId':rid, 'limit':500}).get('events',[])
    for event in events:
        if run.get('status') == 'RUN_STATUS_FAILED':
            print(safe(event)[:3000])
