"""Exercise original authenticated Git creation APIs in the isolated Web PoC.

Negative probes must not enqueue work. An optional single positive task uses a
real local smart HTTP repository and the configured real Agent/model. Stored
task IDs/content checks prevent this helper from blindly replaying submissions.
"""
import argparse
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
parser.add_argument('--create-task', action='store_true')
parser.add_argument('--check-revocation', action='store_true')
args = parser.parse_args()
git = json.loads((state/'web-git.json').read_text(encoding='utf-8'))
fixture = json.loads((state/'web-fixture.json').read_text(encoding='utf-8'))
project_name = 'Local authenticated Git collaboration acceptance'
task_content = 'Git 协作验收。请读取仓库中的中文验收.txt，原样返回完整内容。不要修改文件或提交代码。'


def sql(query):
    return subprocess.check_output(['docker','exec','jingjiaagent-postgres-1','psql','-U','postgres',
                                    '-d','jingjiaagent','-tAc',query],text=True).strip()


def api(client,route,data=None,method=None,internal=False):
    headers = {'Content-Type':'application/json'}
    if internal:
        headers['Authorization'] = 'Bearer '+(state/'daemon.token').read_text().strip()
    req = urllib.request.Request(origin+route, data=json.dumps(data).encode() if data is not None else None,
                                 headers=headers,method=method)
    try:
        with client.open(req,timeout=30) as response:
            return response.status,json.load(response)
    except urllib.error.HTTPError as error:
        return error.code,None


def success(client,route,data=None,method=None):
    status,result = api(client,route,data,method)
    if status != 200 or result.get('code') != 0:
        raise SystemExit('Original API failed for '+route+'; credentials are not printed.')
    return result['data']


def login(filename):
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    account = json.loads((state/filename).read_text())
    user = success(client,'/api/v1/users/password-login',account)
    return client,str(uuid.UUID(user['id']))


owner,owner_id = login('web-account.json')
member,member_id = login('web-member-account.json')
for label,uid in [('owner',owner_id),('member',member_id)]:
    identity = str(uuid.UUID(git[label+'_identity']))
    if sql("SELECT count(*) FROM git_identities WHERE id='"+identity+"' AND user_id='"+uid+"'") != '1':
        raise SystemExit('Scoped local Git credential fixture is required.')
before = sql('SELECT json_build_array((SELECT count(*) FROM tasks),(SELECT count(*) FROM virtualmachines),'
             '(SELECT count(*) FROM projects),(SELECT count(*) FROM project_collaborators),'
             '(SELECT count(*) FROM runtime_commands),(SELECT count(*) FROM model_api_keys))')
task = {'content':task_content,'host_id':fixture['node_id'],'image_id':fixture['image_id'],
        'model_id':fixture['model_id'],'cli_name':'opencode','task_type':'develop',
        'repo':{'repo_url':git['url'],'branch':'main'},'resource':{'core':1,'memory':2*1024**3,'life':3600}}
for actor,foreign in [(owner,git['member_identity']),(member,git['owner_identity'])]:
    probes = [
        ('/api/v1/users/tasks',dict(task,git_identity_id=foreign)),
        ('/api/v1/users/hosts/vms',{'name':'Denied Git credential fixture','host_id':fixture['node_id'],
          'image_id':fixture['image_id'],'model_id':fixture['model_id'],'repo':task['repo'],
          'resource':{'cpu':1,'memory':2*1024**3},'git_identity_id':foreign}),
        ('/api/v1/users/projects',{'name':'Denied Git credential fixture','platform':'gitlab',
          'repo_url':git['url'],'git_identity_id':foreign}),
    ]
    for route,data in probes:
        status,result = api(actor,route,data)
        if status != 200 or result.get('code') != 10002 or result.get('data'):
            raise SystemExit('Foreign Git credential was not explicitly denied: '+route)
after = sql('SELECT json_build_array((SELECT count(*) FROM tasks),(SELECT count(*) FROM virtualmachines),'
            '(SELECT count(*) FROM projects),(SELECT count(*) FROM project_collaborators),'
            '(SELECT count(*) FROM runtime_commands),(SELECT count(*) FROM model_api_keys))')
if before != after:
    raise SystemExit('A denied Git credential probe left business rows or queued work.')
report = {'foreign_task_identity_denied_both_directions':True,'foreign_environment_identity_denied_both_directions':True,
          'foreign_project_identity_denied_both_directions':True,'denied_requests_no_persisted_side_effects':True}
if args.create_task:
    matches = sql("SELECT id FROM projects WHERE user_id='"+owner_id+"' AND name='"+project_name+"'").splitlines()
    if len(matches) > 1:
        raise SystemExit('Conflicting Git acceptance projects; do not repeat creation.')
    if matches:
        project_id = str(uuid.UUID(matches[0]))
    else:
        saved = success(owner,'/api/v1/users/projects',{'name':project_name,'platform':'gitlab','repo_url':git['url'],
                         'git_identity_id':git['owner_identity'],'image_id':fixture['image_id']})
        project_id = str(uuid.UUID(saved['id']))
    if sql("SELECT count(*) FROM projects WHERE id='"+project_id+"' AND git_identity_id='"+git['owner_identity']+"' AND repo_url='"+git['url']+"'") != '1':
        raise SystemExit('Existing project no longer matches the dedicated Git fixture.')
    success(owner,'/api/v1/users/projects/'+project_id,{'collaborators':[
        {'user_id':owner_id,'permission':'read_write'},{'user_id':member_id,'permission':'read_write'}]},'PUT')
    success(member,'/api/v1/users/projects/'+project_id)
    matches = sql("SELECT id FROM tasks WHERE user_id='"+member_id+"' AND content='"+task_content+"'").splitlines()
    if len(matches) > 1:
        raise SystemExit('Conflicting Git acceptance tasks; do not replay submission.')
    if matches:
        task_id = str(uuid.UUID(matches[0]))
    else:
        # This direct credential is foreign. Project access chooses its existing,
        # authorized identity rather than blindly using a user-supplied ID.
        task['git_identity_id'] = git['owner_identity']
        task['extra'] = {'project_id':project_id}
        saved = success(member,'/api/v1/users/tasks',task)
        task_id = str(uuid.UUID(saved['id']))
    (state/'web-git-task.json').write_text(json.dumps({'task':task_id,'project':project_id,'content':task_content},indent=2),encoding='utf-8')
    report.update({'task':task_id,'project':project_id,'project_created_original_api':True,
                   'collaborator_added_original_api':True,'collaborator_task_admitted_original_api':True})
if args.check_revocation:
    saved = json.loads((state/'web-git-task.json').read_text(encoding='utf-8'))
    task_id,project_id = str(uuid.UUID(saved['task'])),str(uuid.UUID(saved['project']))
    if saved['content'] != task_content or sql("SELECT count(*) FROM projects WHERE id='"+project_id+"' AND user_id='"+owner_id+"' AND name='"+project_name+"'") != '1':
        raise SystemExit('Only the dedicated original API Git project may be used for revocation.')
    members = sql("SELECT user_id FROM project_collaborators WHERE project_id='"+project_id+"' AND deleted_at IS NULL ORDER BY user_id").splitlines()
    if sorted(members) != sorted([owner_id,member_id]):
        raise SystemExit('The acceptance project no longer has exactly its two test collaborators.')
    restored = {'collaborators':[{'user_id':owner_id,'permission':'read_write'},{'user_id':member_id,'permission':'read_write'}]}
    success(owner,'/api/v1/users/projects/'+project_id,{'collaborators':[{'user_id':owner_id,'permission':'read_write'}]},'PUT')
    try:
        status,denied = api(owner,'/internal/git-credential',{'task_id':task_id},internal=True)
        if status != 200 or denied.get('code') != 0 or not denied['data'].get('error') or denied['data'].get('password'):
            raise SystemExit('Removed collaborator still received a task/project credential.')
        listings = success(member,'/api/v1/users/projects')
        if any(item['id'] == project_id for item in listings['projects']):
            raise SystemExit('Soft-deleted collaborator still sees the project listing.')
    finally:
        success(owner,'/api/v1/users/projects/'+project_id,restored,'PUT')
    status,allowed = api(owner,'/internal/git-credential',{'task_id':task_id},internal=True)
    if status != 200 or allowed.get('code') != 0 or allowed['data'].get('error') or allowed['data'].get('password') != git['owner_token']:
        raise SystemExit('Restored original collaborator could not obtain its project credential.')
    report.update({'task':task_id,'project':project_id,'removed_collaborator_hidden_from_project_list':True,'removed_collaborator_callback_denied':True,
                   'restored_collaborator_callback_allowed':True})
report_name = 'web-git-revocation-report.json' if args.check_revocation else 'web-git-admission-report.json'
(state/report_name).write_text(json.dumps(report,indent=2),encoding='utf-8')
print(json.dumps(report,ensure_ascii=False,indent=2))
