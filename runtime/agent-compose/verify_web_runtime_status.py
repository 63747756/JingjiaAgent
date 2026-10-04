"""Read-only checks of original authenticated host/environment APIs after adapter changes."""
import http.cookiejar
import json
import pathlib
import urllib.error
import urllib.request
import uuid

state = pathlib.Path(__file__).resolve().parent / '.state'
origin = 'http://127.0.0.1:47420'


def request(client, route, data=None):
    req = urllib.request.Request(origin + route, data=json.dumps(data).encode() if data is not None else None,
                                 headers={'Content-Type': 'application/json'})
    try:
        with client.open(req, timeout=20) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as response:
        return response.code, {}


def success(client, route, data=None):
    status, result = request(client, route, data)
    if status != 200 or result.get('code') != 0:
        raise SystemExit('Original authenticated status API failed: ' + route)
    return result['data']


clients = []
for name in ('web-account.json', 'web-member-account.json'):
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}),
              urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    success(client, '/api/v1/users/password-login', json.loads((state / name).read_text()))
    clients.append(client)
task_ids = ['96594b1d-4859-4e60-b902-1e4df75a1659',
            str(uuid.UUID(json.loads((state / 'web-member-task.json').read_text())['task']))]
tasks = [success(client, '/api/v1/users/tasks/' + task) for client, task in zip(clients, task_ids)]
if tasks[0]['content'] != 'MCP 网关验收初始化。仅回复 WEB_MCP_READY，不要使用工具。' or tasks[1]['content'] != '成员隔离验收初始化。仅回复 WEB_MEMBER_READY，不要使用工具。':
    raise SystemExit('Only existing dedicated acceptance tasks may be inspected.')
vm_ids = [task['virtualmachine']['id'] for task in tasks]
if vm_ids[0] == vm_ids[1] or tasks[0]['user_id'] == tasks[1]['user_id']:
    raise SystemExit('Status comparison does not use two independent user environments.')
counts = []
for index, client in enumerate(clients):
    listing = success(client, '/api/v1/users/hosts')
    hosts = listing.get('hosts') or []
    vms = [vm for host in hosts for vm in host.get('virtualmachines', [])]
    if not hosts or not any(host['status'] == 'online' for host in hosts):
        raise SystemExit('Configured real runtime host is not online in original list.')
    if vm_ids[index] not in {vm['id'] for vm in vms} or vm_ids[1-index] in {vm['id'] for vm in vms}:
        raise SystemExit('Original host list lost the own VM or exposed another user VM.')
    detail = success(client, '/api/v1/users/hosts/vms/' + vm_ids[index])
    if detail['id'] != vm_ids[index] or detail['status'] not in ('online', 'hibernated', 'offline', 'pending'):
        raise SystemExit('Original VM detail shape/status changed.')
    status, result = request(client, '/api/v1/users/hosts/vms/' + vm_ids[1-index])
    if status == 200 and result.get('code') == 0:
        raise SystemExit('Cross-user original VM detail was authorized.')
    counts.append({'hosts': len(hosts), 'own_vms': len(vms), 'own_detail_status': detail['status']})
anonymous = urllib.request.build_opener(urllib.request.ProxyHandler({}))
if request(anonymous, '/api/v1/users/hosts')[0] != 401:
    raise SystemExit('Anonymous host listing was accepted.')
report = {'checks': {'original_logins': True, 'host_online_lists': True,
                    'own_environment_details': True, 'per_user_list_isolation': True,
                    'cross_user_detail_denied': True, 'anonymous_list_denied': True},
          'samples': counts,
          'boundary': 'Read-only original APIs. No process/resource UI exists in this fork.'}
(state / 'web-runtime-status-report.json').write_text(json.dumps(report, indent=2), encoding='utf-8')
print(json.dumps(report, ensure_ascii=False))
