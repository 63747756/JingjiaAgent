"""Check two real Web tasks, personal MCP ownership and team group permissions.

Requires the member/task created in the original Web. Gateway probes only list
tools or attempt forbidden calls; the permitted call must come from the real
Agent and appear in original history and audit. Tokens never leave private state.
"""
import argparse
import base64
import http.cookiejar
import json
import pathlib
import subprocess
import urllib.error
import urllib.parse
import urllib.request
import uuid

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('owner_task',type=uuid.UUID)
parser.add_argument('--personal-enabled',action='store_true')
parser.add_argument('--member-revoked',action='store_true')
args = parser.parse_args()
state = pathlib.Path(__file__).resolve().parent / '.state'
origin = 'http://127.0.0.1:47420'
fixture = json.loads((state / 'web-mcp.json').read_text())
member_task_id = str(uuid.UUID(json.loads((state / 'web-member-task.json').read_text())['task']))
owner_task_id = str(args.owner_task)


def request(client,route,data=None,headers=None):
    req = urllib.request.Request(origin+route,data=json.dumps(data).encode() if data is not None else None,
                                 headers={'Content-Type':'application/json',**(headers or {})})
    try:
        with client.open(req,timeout=15) as response:
            return response.status,json.load(response)
    except urllib.error.HTTPError as error:
        return error.code,None


def api(client,route):
    status,result = request(client,route)
    if status != 200 or result.get('code') != 0:
        raise SystemExit('Original authorized API request failed.')
    return result['data']


def login(name,team=False):
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    account = json.loads((state / name).read_text())
    status,result = request(client,'/api/v1/teams/users/login' if team else '/api/v1/users/password-login',account)
    if status != 200 or result.get('code') != 0:
        raise SystemExit('Original fixture login failed.')
    return client


def sql(query):
    return subprocess.check_output(['docker','exec','jingjia-runtime-tests-20261002','psql','-U','postgres',
                                    '-d','monkeycode_web_poc','-tAc',query],text=True).strip()


def gateway(token,method,params=None):
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    for attempt in range(3 if method == 'tools/list' else 1):
        try:
            return request(client,'/mcp',{'jsonrpc':'2.0','id':uuid.uuid4().hex,'method':method,'params':params or {}},
                           {'Authorization':'Bearer '+token,'Accept':'application/json, text/event-stream'})
        except ConnectionResetError:
            if method != 'tools/list' or attempt == 2:
                raise SystemExit('MCP transport reset before its permission response could be verified.') from None


owner,member,admin = login('web-account.json'),login('web-member-account.json'),login('web-account.json',team=True)
owner_task = api(owner,'/api/v1/users/tasks/'+owner_task_id)
member_task = api(member,'/api/v1/users/tasks/'+member_task_id)
if owner_task['content'] != 'MCP 网关验收初始化。仅回复 WEB_MCP_READY，不要使用工具。' or member_task['content'] != '成员隔离验收初始化。仅回复 WEB_MEMBER_READY，不要使用工具。':
    raise SystemExit('Only dedicated original Web MCP/member tasks may be inspected.')
owner_id,member_id = [str(uuid.UUID(task['user_id'])) for task in (owner_task,member_task)]
if owner_id == member_id:
    raise SystemExit('The comparison tasks belong to the same user.')
environments = [task['virtualmachine']['id'] for task in (owner_task,member_task)]
for environment in environments:
    uuid.UUID(environment.removeprefix('agent_'))
if environments[0] == environments[1]:
    raise SystemExit('Different users share the same runtime environment.')
keys = [sql("SELECT api_key FROM model_api_keys WHERE virtualmachine_id='"+environment+"' AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 1") for environment in environments]
if not all(keys) or keys[0] == keys[1]:
    raise SystemExit('The two tasks do not have independent real task credentials.')
sandboxes = [sql("SELECT sandbox_id FROM runtime_environments WHERE id='"+environment+"'") for environment in environments]
if not all(sandboxes) or sandboxes[0] == sandboxes[1]:
    raise SystemExit('The tasks do not have separate real sandboxes.')

personal = [item for item in api(owner,'/api/v1/users/mcp/upstreams')['items'] if item['name'] == fixture['name']]
team = [item for item in api(admin,'/api/v1/teams/mcp/upstreams')['items'] if item['name'] == fixture['team_name']]
if len(personal) != 1 or len(team) != 1:
    raise SystemExit('Original MCP configuration is missing or duplicated.')
personal_tool = next(t for t in personal[0]['tools'] if t['name'] == fixture['tool'])
team_tool = next(t for t in team[0]['tools'] if t['name'] == fixture['team_tool'])
if personal_tool['enabled'] != args.personal_enabled or not team_tool['enabled']:
    raise SystemExit('Original MCP tool settings do not match this scope check.')
group_ids = [str(uuid.UUID(group['id'])) for group in team[0]['groups']]
if not group_ids:
    raise SystemExit('Team MCP must keep explicit test group bindings.')
memberships = [int(sql("SELECT count(*) FROM team_group_members WHERE user_id='"+user_id+"' AND group_id IN ("+','.join("'"+gid+"'" for gid in group_ids)+")")) for user_id in (owner_id,member_id)]
if memberships[0] != 0 or (memberships[1] > 0) == args.member_revoked:
    raise SystemExit('Original group membership differs from the expected two-user state.')

for index,token in enumerate(keys):
    status,result = gateway(token,'tools/list')
    if status != 200 or 'result' not in result:
        raise SystemExit('A valid task credential failed to authenticate at the original gateway.')
    names = {item['name'] for item in result['result']['tools']}
    if (personal_tool['namespaced_name'] in names) != (index == 0 and args.personal_enabled):
        raise SystemExit('Personal MCP tool leaked across task users or its positive owner listing failed.')
    if (team_tool['namespaced_name'] in names) != (index == 1 and not args.member_revoked):
        raise SystemExit('Team MCP listing did not follow the original group membership.')

audit_file = state / 'web-mcp-audit.jsonl'
before = audit_file.read_bytes()
for token,tool in [(keys[1],personal_tool),(keys[0],team_tool)]+([(keys[1],team_tool)] if args.member_revoked else []):
    status,result = gateway(token,'tools/call',{'name':tool['namespaced_name'],'arguments':{'value':25}})
    if status != 200 or 'error' not in result or audit_file.read_bytes() != before:
        raise SystemExit('A forbidden cross-user/group tool call executed.')

history = api(member,'/api/v1/users/tasks/rounds?id='+member_task_id+'&limit=10')
chunks = [(item,base64.b64decode(item.get('data') or '').decode('utf-8')) for item in history['chunks']]
if not any(item['event']=='task-running' and '50 '+fixture['team_receipt'] in text for item,text in chunks):
    raise SystemExit('New member real Agent result is absent from product history.')
if any(item['event']=='user-input' and fixture['team_receipt'] in base64.b64decode(json.loads(text)['content']).decode('utf-8') for item,text in chunks):
    raise SystemExit('The MCP result was supplied in the member prompt.')
calls = int(sql("SELECT count(*) FROM mcp_tool_calls WHERE task_id='"+member_task_id+"' AND tool_id='"+str(uuid.UUID(team_tool['id']))+"' AND status='success' AND tool_scope_snapshot='team'"))
if calls != 1:
    raise SystemExit('Expected exactly one successful original member tool call.')
if args.member_revoked and not any(item['event']=='task-running' and 'WEB_MEMBER_MCP_REVOKED' in text for item,text in chunks):
    raise SystemExit('Real Agent group-revocation round has not completed.')

for viewer,task,environment in [(owner,member_task_id,environments[1]),(member,owner_task_id,environments[0])]:
    for route in ['/api/v1/users/tasks/'+task,'/api/v1/users/tasks/rounds?id='+task,
                  '/api/v1/users/hosts/vms/'+environment,'/api/v1/users/hosts/vms/'+environment+'/terminals',
                  '/api/v1/users/folders?'+urllib.parse.urlencode({'id':environment,'path':'/workspace'})]:
        status,result = request(viewer,route)
        if status != 403 and (not result or result.get('code') not in (10001,10002,10201,10202)):
            raise SystemExit('Same-team users can access each other\'s task/history/environment/terminal/files.')

report = {'owner_task':owner_task_id,'member_task':member_task_id,'separate_users_sandboxes_and_credentials':True,
          'both_valid_task_tokens_authenticated':True,'personal_enabled_scope_checked':args.personal_enabled,
          'personal_tool_other_task_denied':True,'team_group_owner_denied':True,
          'member_group_access_revoked':args.member_revoked,'member_real_tool_receipt_observed':True,
          'successful_member_gateway_call_records':calls,'same_team_cross_task_business_access_denied':True}
report_name = 'web-mcp-two-task-personal-report.json' if args.personal_enabled else ('web-mcp-two-task-revoked-report.json' if args.member_revoked else 'web-mcp-two-task-report.json')
(state / report_name).write_text(json.dumps(report,indent=2),encoding='utf-8')
print(json.dumps(report,ensure_ascii=False,indent=2))
