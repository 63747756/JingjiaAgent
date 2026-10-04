"""Verify an existing local deployment without filling its task capacity."""
import hashlib
import json
import os
import re
import subprocess
import urllib.parse
import urllib.request
import uuid
from linux_web_common import api, load, login, project, request, save, sql, state

cfg=load('config/server/config.yaml')
if cfg['runtime']['backend']!='agent_compose' or any(cfg.get('taskflow',{}).get(key) for key in ('server','url')):
    raise SystemExit('Expected an agent-compose-only deployment.')
admin,admin_user,_=login(team=True)
owner,user,_=login()
overview=api(admin,'/api/v1/teams/dashboard')
conversations=api(admin,'/api/v1/teams/conversations?limit=1')
seen=[]
cursor=''
while True:
    page=api(admin,'/api/v1/teams/conversations?'+urllib.parse.urlencode({'limit':1,'cursor':cursor}))
    for row in page.get('conversations') or []:
        if row['id'] in seen:raise RuntimeError('Repeated administrator conversation across pages.')
        seen.append(row['id'])
    pagination=page.get('page') or {}
    if not pagination.get('has_next_page'):break
    cursor=pagination.get('cursor')
    if not cursor or len(seen)>10000:raise RuntimeError('Invalid administrator conversation pagination.')
    # Bound daily checks on a populated environment; the contract tests cover ties.
    if len(seen)>=100:break

failed=[]
owner_id=str(uuid.UUID(user['id']))
ids=sql("SELECT task_id FROM runtime_creation_attempts WHERE state='failed' AND owner_id='"+owner_id+"' ORDER BY created_at DESC LIMIT 10;").splitlines()
for task in ids:
    history=api(owner,'/api/v1/users/tasks/rounds?id='+str(uuid.UUID(task)))
    if history.get('chunks') or history.get('has_more'):raise RuntimeError('Rejected task unexpectedly has execution history.')
    failed.append(task)

installer_scopes=[]
def verify_installer(actor, route, complete_bundle, identity, team):
    command=api(actor,route)['command']
    match=re.search(r'install\?token=([a-f0-9-]{36})',command)
    if not match:raise RuntimeError('Missing scoped installer ticket.')
    token=match.group(1)
    ticket_key='host:runtime-install:'+hashlib.sha256(token.encode()).hexdigest()
    http=urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        ticket=json.loads(subprocess.check_output(['docker','exec',project+'-redis-1','redis-cli','GET',ticket_key]))
        node=next(n for n in cfg['runtime']['nodes'] if n['id']==ticket['node'])
        if ticket['actor']!=identity or ticket['team']!=team or node.get('team_id','')!=team:
            raise RuntimeError('Installer ticket selected the wrong actor or team.')
        if not team and node['owner_id']!=identity:
            raise RuntimeError('User installer selected another account\'s private node.')
        installer_scopes.append({'actor_id':identity,'team_id':team,'node_id':node['id'],'owner_id':node['owner_id']})
        with http.open('http://127.0.0.1:47424/api/v1/users/hosts/install?token='+token,timeout=30) as response:
            script=response.read(1048576)
        if b'agent-compose' not in script:raise RuntimeError('Unexpected installer script.')
        manifest=load('config/server/installation-bundle/manifest.json')
        url='http://127.0.0.1:47424/api/v1/users/hosts/install-bundle?token='+token
        if complete_bundle:
            digest=hashlib.sha256()
            with http.open(url,timeout=60) as response:
                while chunk:=response.read(1048576):digest.update(chunk)
            if digest.hexdigest()!=manifest['sha256']:raise RuntimeError('Installer download differs from the fixed archive.')
        else:
            req=urllib.request.Request(url,headers={'Range':'bytes=0-4095'})
            with http.open(req,timeout=30) as response:
                if response.status!=206:raise RuntimeError('User installer bundle did not support authorized range download.')
                first=response.read(4097)
            archive=state/'config/server/installation-bundle'/manifest['archive']
            with archive.open('rb') as source:
                if first!=source.read(4096):raise RuntimeError('User installer download differs from the fixed archive.')
    finally:
        subprocess.run(['docker','exec',project+'-redis-1','redis-cli','DEL',ticket_key],check=True,stdout=subprocess.DEVNULL)

verify_installer(admin,'/api/v1/teams/hosts/install-command',True,admin_user['id'],admin_user['team']['id'])
verify_installer(owner,'/api/v1/users/hosts/install-command',False,user['id'],'')
denied=[]
for filename in ('web-member-account.json','web-outsider-account.json'):
    if not (state/filename).exists():continue
    other,_,_=login(filename)
    status,result=request(other,'/api/v1/users/hosts/install-command')
    if status!=403 or result.get('code')!=10235:raise RuntimeError('Unprivileged account obtained an installation command.')
    denied.append(filename)

ids=subprocess.check_output(['docker','ps','-aq','--filter','label=com.docker.compose.project='+project],text=True).split()
services=json.loads(subprocess.check_output(['docker','inspect',*ids]))
if len(services)!=8 or any(not item['State']['Running'] or item['State'].get('Health',{}).get('Status') not in (None,'healthy') for item in services):
    raise RuntimeError('Local deployment services are not ready.')
report={'backend':'agent_compose','taskflow_service_configured':False,'running_services':8,
    'dashboard':True,'conversations':True,'conversation_pages_without_duplicates':len(seen),
    'failed_task_histories':failed,'installer_command_script_bundle_verified':True,
    'user_installer_command_script_range_verified':True,'installer_unconfigured_accounts_denied':denied,
    'installer_scopes':installer_scopes,
    'installer_ticket_revoked_after_test':True,'configured_capacity':cfg['runtime']['capacity']}
if task:=os.environ.get('RUNTIME_WEB_VERIFY_TASK_ID'):
    task=str(uuid.UUID(task))
    status=sql("SELECT state FROM runtime_commands WHERE task_id='"+task+"' AND operation='task' ORDER BY turn DESC LIMIT 1;")
    if status!='complete':raise RuntimeError('The nominated real browser task has not completed.')
    if sql("SELECT cpu_millis,memory_bytes FROM runtime_reservations WHERE environment_id=(SELECT environment_id FROM runtime_task_intents WHERE task_id='"+task+"');")!='2000|8589934592':
        raise RuntimeError('Browser task did not use the default 2 CPU/8 GiB profile.')
    report['real_browser_default_resource_task']=task
save('local-verification-report.json',report)
print('Existing local deployment passed: dashboard, conversation paging, rejected task history, administrator/user scoped installers, permission denial and eight services.')
