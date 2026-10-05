"""Verify the original member-create Web fixture and existing authentication.

The fixture is created in the original manager page, not seeded in SQL. Requests
here probe only expected rejections (duplicate, quota and foreign group); no
password resets, emails or permitted account creation are submitted.
"""
import http.cookiejar
import json
import pathlib
import subprocess
import urllib.error
import urllib.request
import uuid

from linux_web_common import state
origin = 'http://127.0.0.1:47424'
member = json.loads((state / 'web-member-account.json').read_text(encoding='utf-8'))
if member['email'] != 'runtime-member@example.invalid':
    raise SystemExit('Only the original Web member acceptance fixture is allowed.')


def request(client, route, data=None, method=None):
    req = urllib.request.Request(origin+route, data=json.dumps(data).encode() if data is not None else None,
                                 headers={'Content-Type':'application/json'}, method=method)
    try:
        with client.open(req, timeout=15) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, None


def login(account, team=False):
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    status, result = request(client, '/api/v1/teams/users/login' if team else '/api/v1/users/password-login', account)
    if status != 200 or result.get('code') != 0:
        raise SystemExit('Original fixture login failed.')
    return client, result['data']


def sql(query):
    return subprocess.check_output(['docker','exec','jingjiaagent-postgres-1','psql','-U','postgres',
                                    '-d','jingjiaagent','-tAc',query], text=True).strip()


owner_account = json.loads((state / 'web-account.json').read_text())
admin, owner = login(owner_account, team=True)
employee, created = login(member)
team_id = str(uuid.UUID(owner['team']['id']))
member_id = str(uuid.UUID(created['id']))
status, employee_status = request(employee, '/api/v1/users/status')
if status != 200 or employee_status.get('code') != 0:
    raise SystemExit('Original authenticated user status failed.')
if created['role'] != 'subaccount' or not any(item['team_id'] == team_id and item['team_role'] == 'user' for item in employee_status['data']['teams']):
    raise SystemExit('Created member logged into the wrong role or team.')
status, listing = request(admin, '/api/v1/teams/users?role=user')
if status != 200 or listing.get('code') != 0:
    raise SystemExit('Original manager member list failed.')
matches = [item for item in listing['data']['members'] if item['user']['id'] == member_id]
if len(matches) != 1:
    raise SystemExit('Created fixture is missing or duplicated in the member list.')
status, groups = request(admin, '/api/v1/teams/groups')
if status != 200 or groups.get('code') != 0:
    raise SystemExit('Original manager group list failed.')
group_items = groups['data']['groups']
if not any(item['name'] == '默认分组' and any(user['id'] == member_id for user in item.get('users') or []) for item in group_items):
    raise SystemExit('Original member creation did not assign the default group.')

old_hash = sql("SELECT password FROM users WHERE id='"+member_id+"'")
before_count = sql("SELECT count(*) FROM users WHERE role='subaccount'")
status, result = request(admin, '/api/v1/teams/users/with-password', {'emails':[member['email']]})
if status != 200 or result.get('code') != 10503 or result.get('data'):
    raise SystemExit('Duplicate creation returned credentials or an unexpected result.')
if sql("SELECT password FROM users WHERE id='"+member_id+"'") != old_hash:
    raise SystemExit('Duplicate creation replaced the original password.')
scope_fixture = json.loads((state / 'web-member-scope.json').read_text())
foreign_group = str(uuid.UUID(scope_fixture['group']))
foreign_team = str(uuid.UUID(scope_fixture['team']))
if foreign_team == team_id or sql("SELECT count(*) FROM team_groups WHERE id='"+foreign_group+"' AND team_id='"+foreign_team+"'") != '1':
    raise SystemExit('An existing isolated foreign-group fixture is required for the scope check.')
status, result = request(admin, '/api/v1/teams/users/with-password',
                         {'emails':['runtime-denied-member@example.invalid'],'group_id':foreign_group})
if status != 200 or result.get('code') not in (10002,10100) or result.get('data'):
    raise SystemExit('Cross-team group request was not explicitly denied.')
for route, data, method in [
    ('/api/v1/teams/groups/'+foreign_group+'/users', None, 'GET'),
    ('/api/v1/teams/groups/'+foreign_group, {'name':'forged foreign group'}, 'PUT'),
    ('/api/v1/teams/groups/'+foreign_group+'/users', {'user_ids':[member_id]}, 'PUT'),
    ('/api/v1/teams/groups/'+foreign_group, None, 'DELETE'),
]:
    status, result = request(admin, route, data, method)
    if status != 200 or result.get('code') != 10002:
        raise SystemExit('Foreign group read/update/membership/delete route did not explicitly reject access.')
if sql("SELECT count(*) FROM team_groups WHERE id='"+foreign_group+"' AND team_id='"+foreign_team+"' AND name='Isolated foreign member-scope group'") != '1' or sql("SELECT count(*) FROM team_group_members WHERE group_id='"+foreign_group+"'") != '0':
    raise SystemExit('Foreign group denial probes changed the fixture.')
outsider = json.loads((state / 'web-outsider-account.json').read_text())
outsider_id = str(uuid.UUID(outsider['id']))
old_name = sql("SELECT name FROM users WHERE id='"+outsider_id+"'")
status, result = request(admin, '/api/v1/teams/users/'+outsider_id, {'name':'forged foreign member'}, 'PUT')
if status != 200 or result.get('code') != 10002 or sql("SELECT name FROM users WHERE id='"+outsider_id+"'") != old_name:
    raise SystemExit('Foreign user update was not rejected without changing its account.')
quota_emails = ['runtime-quota-'+letter+'@example.invalid' for letter in 'abcd']
if len(listing['data']['members'])+len(quota_emails) <= listing['data']['member_limit']:
    raise SystemExit('Quota denial probe no longer exceeds the fixture limit.')
status, result = request(admin, '/api/v1/teams/users/with-password', {'emails':quota_emails})
if status != 200 or result.get('code') != 10500 or result.get('data'):
    raise SystemExit('Over-quota batch was not explicitly denied.')
if sql("SELECT count(*) FROM users WHERE role='subaccount'") != before_count:
    raise SystemExit('Rejected creation probes partially committed accounts.')
status, result = request(employee, '/api/v1/teams/users/with-password', {'emails':['runtime-denied-member@example.invalid']})
if status != 401:
    raise SystemExit('Console-only cookie reached the team member creation route.')

audit = sql("SELECT response FROM audits WHERE operation='add_team_user_with_password' AND request LIKE '%runtime-member@example.invalid%' ORDER BY created_at")
if not audit or member['password'] in audit or old_hash in audit or '********' not in audit:
    raise SystemExit('Original creation audit is missing or retained the generated credential.')
login(member)
report = {'member':member_id,'team':team_id,'created_in_original_web':True,'original_password_login':True,
          'default_group_assigned':True,'duplicate_denied_password_unchanged':True,'quota_batch_atomic_denial':True,
          'cross_team_group_denied':True,'foreign_group_management_denied':True,'foreign_user_update_denied':True,
          'console_cookie_cannot_create_members':True,'audit_password_masked':True}
(state / 'web-member-report.json').write_text(json.dumps(report,indent=2),encoding='utf-8')
print(json.dumps(report,ensure_ascii=False,indent=2))
