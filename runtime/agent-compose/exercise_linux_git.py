"""Bind actual private Gitea PAT/repository using the original user/project APIs."""
import base64
import json
import secrets
import urllib.parse
import urllib.request
from linux_web_common import api, load, login, save, state

owner, user, _ = login()
member, colleague, _ = login('web-member-account.json')
git = load('git-fixture.json')
account = load('git-account.json')
pat = load('git-pat.json')['token']
fixture = load('web-fixture.json')
record = load('git-web.json') if (state/'git-web.json').exists() else {'marker':'GIT_'+secrets.token_hex(12)}
headers={'Content-Type':'application/json','Authorization':'token '+pat}
http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
content = '中文私有仓库验收\n'+record['marker']+'\n'
if not record.get('seeded'):
    path = '/api/v1/repos/'+git['full_name']+'/contents/'+urllib.parse.quote('中文验收.txt')
    req = urllib.request.Request('http://127.0.0.1:47598'+path,
        data=json.dumps({'content':base64.b64encode(content.encode()).decode(),'message':'Initialize dedicated acceptance receipt','branch':'main'}).encode(),headers=headers)
    with http.open(req,timeout=20) as response:
        if response.status not in (200,201):raise RuntimeError('Private repository initialization failed')
    record['seeded']=True
    save('git-web.json',record)
identities = api(owner,'/api/v1/users/git-identities') or []
matches = [item for item in identities if item['remark']=='Phase 4 actual private Gitea']
if len(matches)>1:raise RuntimeError('Duplicate private Git credential binding')
if matches:
    identity = matches[0]
    if identity['username']!=account['username'] or identity['base_url']!=git['base_url']:
        raise RuntimeError('Existing Git binding differs from the isolated fixture')
else:
    identity = api(owner,'/api/v1/users/git-identities',{'platform':'gitea','base_url':git['base_url'],
        'access_token':pat,'username':account['username'],'email':account['email'],'remark':'Phase 4 actual private Gitea'})
record['identity']=identity['id']
save('git-web.json',record)
detail = api(owner,'/api/v1/users/git-identities/'+identity['id']+'?flush=true')
if not any(repo['full_name']==git['full_name'] for repo in detail.get('authorized_repositories',[])):
    raise RuntimeError('Original Git provider did not list the real private repository')
branches = api(owner,'/api/v1/users/git-identities/'+identity['id']+'/'+urllib.parse.quote(git['full_name'],safe='')+'/branches')
if not any(item['name']=='main' for item in branches):
    raise RuntimeError('Original Git branch selector did not list main')
if not record.get('project'):
    project = api(owner,'/api/v1/users/projects',{'name':'Phase 4 private Gitea collaboration','platform':'gitea',
        'repo_url':git['url'],'git_identity_id':identity['id'],'image_id':fixture['image_id']})
    record['project']=project['id']
    save('git-web.json',record)
api(owner,'/api/v1/users/projects/'+record['project'],{'collaborators':[
    {'user_id':user['id'],'permission':'read_write'},{'user_id':colleague['id'],'permission':'read_write'}]},'PUT')
api(member,'/api/v1/users/projects/'+record['project'])
save('git-web-report.json',{'actual_gitea_pat_bound_original_api':True,'private_repo_listed':True,
    'main_branch_listed':True,'project_created_original_api':True,'collaborator_access_original_api':True})
print('Real private Gitea PAT binding, repository/branch selectors and original project collaboration APIs passed.')
