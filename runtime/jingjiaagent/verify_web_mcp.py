"""Verify the original Web MCP management, gateway and real Agent evidence.

Does not submit Agent runs or create test permissions. The foreign-account
setting update is an expected-denial probe; gateway calls in disabled mode must
be rejected before reaching the isolated calculation tool.
"""
import argparse
import base64
import http.cookiejar
import json
import pathlib
import subprocess
import urllib.error
import urllib.request
import uuid

from linux_web_common import state
origin = 'http://127.0.0.1:47424'
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('task_id', type=uuid.UUID)
parser.add_argument('--expect-disabled', action='store_true')
parser.add_argument('--team', action='store_true')
parser.add_argument('--expect-unbound', action='store_true')
args = parser.parse_args()
if args.expect_unbound and not args.team:
    parser.error('--expect-unbound requires --team')
task_id = str(args.task_id)
fixture = json.loads((state / 'web-mcp.json').read_text(encoding='utf-8'))
scope = 'team' if args.team else 'user'
selected_name = fixture['team_name'] if args.team else fixture['name']
selected_tool = fixture['team_tool'] if args.team else fixture['tool']
selected_receipt = fixture['team_receipt'] if args.team else fixture['receipt']


def login(name, team=False):
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    account = json.loads((state / name).read_text())
    login_path = '/api/v1/teams/users/login' if team else '/api/v1/users/password-login'
    request = urllib.request.Request(origin+login_path, data=json.dumps(account).encode(), headers={'Content-Type':'application/json'})
    with client.open(request, timeout=10) as response:
        if json.load(response).get('code') != 0:
            raise SystemExit('Original fixture login failed.')
    return client


def api(client, route, data=None, method=None, expected_error=False):
    request = urllib.request.Request(origin+route, data=json.dumps(data).encode() if data is not None else None,
                                     method=method, headers={'Content-Type':'application/json'})
    with client.open(request, timeout=20) as response:
        result = json.load(response)
    if (result.get('code') != 0) != expected_error:
        raise SystemExit('Original API returned an unexpected permission result.')
    return result.get('data')


def sql(query):
    return subprocess.check_output(['docker','exec','jingjiaagent-postgres-1','psql','-U','postgres',
                                    '-d','jingjiaagent','-tAc',query], text=True).strip()


def gateway(client, method, token=None, params=None):
    request = urllib.request.Request(origin+'/mcp', data=json.dumps({'jsonrpc':'2.0','id':uuid.uuid4().hex,
                                    'method':method,'params':params or {}}).encode(),
                                    headers={'Content-Type':'application/json','Accept':'application/json, text/event-stream',
                                             **({'Authorization':'Bearer '+token} if token else {})})
    for attempt in range(3 if method == 'tools/list' else 1):
        try:
            with client.open(request, timeout=15) as response:
                return response.status, json.load(response)
        except urllib.error.HTTPError as error:
            return error.code, None
        except ConnectionResetError:
            if method != 'tools/list' or attempt == 2:
                raise SystemExit('MCP transport reset before its permission response could be verified.') from None


owner, outsider = login('web-account.json'), login('web-outsider-account.json')
task = api(owner, '/api/v1/users/tasks/'+task_id)
if task['content'] != 'MCP 网关验收初始化。仅回复 JINGJIAAGENT_WEB_MCP_READY，不要使用工具。':
    raise SystemExit('Only the dedicated MCP browser acceptance fixture may be inspected.')
manager = login('web-account.json', team=True) if args.team else owner
manager_route = '/api/v1/teams/mcp/upstreams' if args.team else '/api/v1/users/mcp/upstreams'
upstreams = api(manager, manager_route)['items']
matches = [item for item in upstreams if item['name'] == selected_name]
if len(matches) != 1:
    raise SystemExit('Original MCP upstream is missing or duplicated.')
upstream = matches[0]
if upstream['scope'] != scope or upstream['sync_status'] != 'success' or upstream['health_status'] != 'healthy' or not upstream['enabled']:
    raise SystemExit('Original user MCP registration/synchronization did not succeed.')
tools = [item for item in upstream['tools'] if item['name'] == selected_tool]
if len(tools) != 1 or tools[0]['scope'] != scope or tools[0]['enabled'] != (not args.expect_disabled):
    raise SystemExit('Original tool setting differs from the expected Web state.')
tool = tools[0]
foreign = api(outsider, '/api/v1/users/mcp/upstreams')['items']
if any(item['id'] == upstream['id'] or any(t['id'] == tool['id'] for t in item.get('tools') or []) for item in foreign):
    raise SystemExit('Another account can enumerate the private MCP configuration.')
api(outsider, '/api/v1/users/mcp/tools/'+tool['id'], {'enabled': not tool['enabled']}, 'PUT', expected_error=True)
if args.expect_unbound:
    api(owner, '/api/v1/users/mcp/tools/'+tool['id'], {'enabled':False}, 'PUT', expected_error=True)
after = api(manager, manager_route)['items']
if not any(t['id'] == tool['id'] and t['enabled'] == tool['enabled'] for item in after for t in item.get('tools') or []):
    raise SystemExit('Foreign-account setting update changed the owner tool.')
environment = task['virtualmachine']['id']
uuid.UUID(environment.removeprefix('agent_'))
token = sql("SELECT api_key FROM model_api_keys WHERE virtualmachine_id='"+environment+"' AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 1")
if not token:
    raise SystemExit('The original task-bound MCP credential is absent.')
plain = urllib.request.build_opener(urllib.request.ProxyHandler({}))
for client, credential in [(plain,None),(outsider,None),(plain,'invalid-fixture-key')]:
    if gateway(client,'tools/list',credential)[0] != 401:
        raise SystemExit('MCP gateway accepted an absent or invalid task credential.')
status, listing = gateway(plain,'tools/list',token)
if status != 200 or 'result' not in listing:
    raise SystemExit('Original task credential did not reach the real MCP gateway.')
present = any(t['name'] == tool['namespaced_name'] for t in listing['result']['tools'])
denied = args.expect_disabled or args.expect_unbound
if present != (not denied):
    raise SystemExit('Gateway tools/list does not enforce the original tool setting.')
history = api(owner, '/api/v1/users/tasks/rounds?id='+task_id+'&limit=10')
chunks = [(item,base64.b64decode(item.get('data') or '').decode('utf-8')) for item in history['chunks']]
if not any(item['event']=='task-running' and selected_receipt in text for item,text in chunks):
    raise SystemExit('The real Agent MCP receipt is absent from product history.')
for item,text in chunks:
    if item['event']=='user-input' and selected_receipt in base64.b64decode(json.loads(text)['content']).decode('utf-8'):
        raise SystemExit('The MCP receipt was supplied in a prompt.')
audit_file = state / 'web-mcp-audit.jsonl'
audit = [json.loads(line) for line in audit_file.read_text().splitlines()]
if not any(item['value'] == (23 if args.team else 21) and selected_receipt in json.dumps(item['result']) for item in audit):
    raise SystemExit('Real MCP fixture execution did not compute the requested result.')
calls = int(sql("SELECT count(*) FROM mcp_tool_calls WHERE task_id='"+task_id+"' AND tool_id='"+str(uuid.UUID(tool['id']))+"' AND status='success' AND tool_scope_snapshot='"+scope+"'"))
if calls < 1:
    raise SystemExit('The original gateway did not record a successful task-scoped tool call.')
if denied:
    before = audit_file.read_bytes()
    status, response = gateway(plain,'tools/call',token,{'name':tool['namespaced_name'],'arguments':{'value':21}})
    if status != 200 or 'error' not in response or audit_file.read_bytes() != before:
        raise SystemExit('Disabled tool was executed or its denial could not be proved.')
    marker = 'JINGJIAAGENT_WEB_TEAM_MCP_DENIED' if args.expect_unbound else 'JINGJIAAGENT_WEB_MCP_DISABLED'
    if not any(item['event']=='task-running' and marker in text for item,text in chunks):
        raise SystemExit('The original Web disabled-tool round has not completed.')
if args.team:
    group_ids = [str(uuid.UUID(group['id'])) for group in upstream['groups']]
    if not group_ids:
        raise SystemExit('Team fixture must retain explicit group bindings.')
    membership = int(sql("SELECT count(*) FROM team_group_members WHERE user_id='"+str(uuid.UUID(task['user_id']))+"' AND group_id IN ("+','.join("'"+value+"'" for value in group_ids)+")"))
    if (membership > 0) == args.expect_unbound:
        raise SystemExit('The real team group membership does not match the tested permission state.')
    baseline_file = state / 'web-team-mcp-report.json'
    if args.expect_unbound and calls != json.loads(baseline_file.read_text())['successful_gateway_call_records']:
        raise SystemExit('Team tool executed after group access was removed.')
report = {'task':task_id,'environment':environment,'scope':scope,'upstream':upstream['id'],'tool':tool['namespaced_name'],
          'web_registration_and_sync':True,'model_receipt_observed':True,'receipt_not_in_prompt':True,
          'successful_gateway_call_records':calls,'other_account_hidden':True,'foreign_setting_update_denied':True,
          'invalid_or_missing_task_credential_denied':True,'enabled':tool['enabled'],
          'disabled_listing_and_execution_denied':args.expect_disabled,'group_access_removed':args.expect_unbound,
          'group_listing_and_execution_denied':args.expect_unbound}
if args.team:
    report['groups'] = [group['name'] for group in upstream['groups']]
report_file = 'web-team-mcp-denied-report.json' if args.expect_unbound else ('web-team-mcp-report.json' if args.team else 'web-mcp-report.json')
(state / report_file).write_text(json.dumps(report,indent=2),encoding='utf-8')
print(json.dumps(report,ensure_ascii=False,indent=2))
