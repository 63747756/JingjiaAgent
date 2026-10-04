"""Real Web API provider and resource-capacity acceptance; never replay a saved task."""
import base64
import json
import secrets
import uuid
from linux_web_common import api, load, login, request, save, sql, state, wait

owner, owner_user, _ = login()
member, member_user, _ = login('web-member-account.json')
fixture = load('web-fixture.json')
record_file = 'capacity-tasks-p17.json'
record = load(record_file) if (state/record_file).exists() else {}
report = load('web-acceptance-report.json') if (state/'web-acceptance-report.json').exists() else {}

def persist():
    save(record_file, record)
    save('web-acceptance-report.json', report)

def held():
    return int(sql("SELECT count(*) FROM runtime_reservations WHERE active;"))

def task(label, cli, http, actor):
    if label not in record:
        record[label] = {'marker':'WEB_'+secrets.token_hex(12),'actor':actor,'cli':cli}
        persist()
    item = record[label]
    if item['cli'] != cli or item['actor'] != actor:
        raise RuntimeError('Existing acceptance task does not match its actor/provider')
    content = '只回复下面的标记，不要使用工具：' + item['marker']
    if not item.get('task'):
        # Recover a lost response by the exact random fixture content. Do not
        # resend an ambiguous create request and start a duplicate environment.
        found = sql("SELECT id FROM tasks WHERE content='"+content+"' AND user_id='"+str(uuid.UUID(actor))+"';").splitlines()
        if len(found) > 1:
            raise RuntimeError('Duplicate acceptance task')
        if found:
            item['task'] = found[0]
        else:
            model = fixture['model_id'] if cli == 'opencode' else fixture[cli+'_model_id']
            saved = api(http,'/api/v1/users/tasks',{'content':content,'host_id':fixture['node_id'],
                'image_id':fixture['image_id'],'model_id':model,'cli_name':cli,'task_type':'develop',
                'repo':{'repo_url':'','branch':'main'},'resource':{'core':1,'memory':2<<30,'life':3600}})
            item['task'] = str(uuid.UUID(saved['id']))
        persist()
    detail = api(http,'/api/v1/users/tasks/'+item['task'])
    if detail['content'] != content or detail['user_id'] != actor or detail['cli_name'] != cli:
        raise RuntimeError('Saved acceptance task ownership/content changed')
    item['vm'] = detail['virtualmachine']['id']
    persist()
    return item

wait(lambda: any(host['id']==fixture['node_id'] and host['status']=='online'
    for host in api(owner,'/api/v1/users/hosts').get('hosts',[])),label='runtime node')
for label, cli, http, actor in [('owner-opencode','opencode',owner,owner_user['id']),
    ('owner-claude','claude',owner,owner_user['id']),('member-codex','codex',member,member_user['id']),
    ('member-opencode','opencode',member,member_user['id'])]:
    item = task(label,cli,http,actor)
    def completed():
        status = sql("SELECT state FROM runtime_commands WHERE task_id='"+item['task']+"' AND operation='task' ORDER BY turn DESC LIMIT 1;")
        if status in ('failed','canceled'):
            raise RuntimeError(cli+' business Run '+status+'; inspect redacted runtime diagnostics')
        return status == 'complete'
    wait(completed,240,label=cli+' Web task')
    rows = sql("SELECT chunk FROM runtime_events WHERE task_id='"+item['task']+"' ORDER BY seq;").splitlines()
    # Each ACP message contains one text delta. Joining serialized JSON would
    # miss markers split across deltas (the native CLIs stream tiny fragments).
    output = ''
    for line in rows:
        chunk = json.loads(line)
        if chunk.get('event') != 'task-running':
            continue
        data = base64.b64decode(chunk.get('data','')).decode('utf-8',errors='replace')
        if chunk.get('kind') == 'acp_event':
            update = json.loads(data).get('update',{})
            if update.get('sessionUpdate') == 'agent_message_chunk':
                output += update.get('content',{}).get('text','')
        else:
            output += data
    if item['marker'] not in output:
        raise RuntimeError(cli+' real model output missing from durable business events')
    report[cli+'_web_real_model'] = True
    persist()
    print(cli+' real Web task passed (1 CPU/2 GiB environment retained).',flush=True)
if held() != 4:
    raise RuntimeError('Expected exactly four dedicated environment reservations')
report['four_retained_reservations'] = True
counts = 'SELECT json_build_array((SELECT count(*) FROM runtime_environments),(SELECT count(*) FROM runtime_commands),(SELECT count(*) FROM virtualmachines WHERE deleted_at IS NULL),(SELECT count(*) FROM model_api_keys WHERE deleted_at IS NULL));'
before = sql(counts)
status, result = request(owner,'/api/v1/users/tasks',{'content':'Phase 4 capacity rejection '+secrets.token_hex(8),
    'host_id':fixture['node_id'],'image_id':fixture['image_id'],'model_id':fixture['model_id'],
    'cli_name':'opencode','task_type':'develop','repo':{'repo_url':'','branch':'main'},
    'resource':{'core':1,'memory':2<<30,'life':3600}})
after = sql(counts)
if status != 409 or result.get('code') != 10237 or before != after or held() != 4:
    raise RuntimeError('Over-budget Web task was not rejected atomically')
report['capacity_rejection_atomic'] = True
for http, own, foreign in [(owner,'owner-opencode','member-codex'),(member,'member-codex','owner-opencode')]:
    hosts = api(http,'/api/v1/users/hosts')['hosts']
    listed = {vm['id'] for host in hosts for vm in host.get('virtualmachines',[])}
    if record[own]['vm'] not in listed or record[foreign]['vm'] in listed:
        raise RuntimeError('Original host list crossed user environment scope')
    status, value = request(http,'/api/v1/users/hosts/vms/'+record[foreign]['vm'])
    if status == 200 and value.get('code') == 0:
        raise RuntimeError('Foreign environment detail authorized')
report['two_user_vm_scope'] = True
persist()
print('Four retained environments, atomic capacity rejection and two-user VM scope passed.',flush=True)
