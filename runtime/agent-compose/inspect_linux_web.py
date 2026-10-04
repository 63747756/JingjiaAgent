"""Print bounded, credential-redacted diagnostics for the named fixture only."""
import json
import re
import subprocess
import uuid
from linux_web_common import load, runtime_rpc, sql, state

values = [load('model.json')['api_key'],(state/'daemon.token').read_text().strip()]
values += sql('SELECT api_key FROM model_api_keys;').splitlines()
for name in ('web-account.json','web-member-account.json','web-outsider-account.json','git-account.json'):
    if (state/name).exists():values.append(load(name)['password'])
if (state/'git-pat.json').exists():values.append(load('git-pat.json')['token'])
def redacted(text):
    for value in values:
        if value:text = text.replace(value,'[redacted]')
    return re.sub(r'invalid header:[A-Za-z0-9+/=_-]+','invalid header:[redacted upstream headers]',text)

rows = sql("SELECT json_build_object('task',task_id,'operation',operation,'state',state,'run',run_id) FROM runtime_commands WHERE state NOT IN ('complete','failed','canceled');").splitlines()
for row in rows:
    item = json.loads(row)
    print(json.dumps(item))
    if item['run']:
        detail = runtime_rpc('RunService','GetRun',{'runId':item['run']})['run']
        print(redacted(json.dumps(detail.get('summary',{})))[:1500])
        events = runtime_rpc('RunService','ListRunEvents',{'runId':item['run'],'limit':30})
        print(redacted(json.dumps(events))[:4500])
output = subprocess.check_output(['docker','logs','--tail','12','jingjia-phase4-web-backend-1'],stderr=subprocess.STDOUT,text=True)
print(redacted(output)[:5500])
