"""Independent Gitea, private PAT and private repository for original API tests."""
import base64
import json
import pathlib
import secrets
import subprocess
import urllib.error
import urllib.request
from linux_web_common import root, state, save, load

tag = 'docker.gitea.com/gitea:1.26.4-rootless'
image = json.loads(subprocess.check_output(['docker','image','inspect',tag]))[0]['Id']
envfile = state/'git.env'
envfile.write_text('WEB_GITEA_IMAGE='+image+'\n',encoding='utf-8');envfile.chmod(0o600)
command = ['docker','compose','-p','jingjia-phase4-git','--env-file',str(envfile),'-f',str(root/'compose.git.yaml')]
subprocess.run(command+['up','-d','--wait','--wait-timeout','120'],check=True)
name = 'jingjia-phase4-git-git-1'
installed = json.loads(subprocess.check_output(['docker','inspect',name]))[0]
if installed['Image'] != image or installed['Config']['Labels'].get('com.docker.compose.project') != 'jingjia-phase4-git':
    raise RuntimeError('Unexpected Git fixture container')
if not (state/'git-account.json').exists():
    save('git-account.json',{'username':'phase4-owner','email':'phase4-git@example.invalid','password':secrets.token_urlsafe(24)+'_P1!'})
account = load('git-account.json')
http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
base = 'http://127.0.0.1:47598'
def api(route,body=None,token=None):
    headers = {'Content-Type':'application/json'}
    headers['Authorization'] = ('token '+token) if token else ('Basic '+base64.b64encode((account['username']+':'+account['password']).encode()).decode())
    req = urllib.request.Request(base+route,data=json.dumps(body).encode() if body is not None else None,headers=headers)
    try:
        with http.open(req,timeout=20) as response:return json.load(response)
    except urllib.error.HTTPError as error:
        raise RuntimeError('Git fixture API HTTP '+str(error.code)) from None
# Creating one test identity is reversible and explicitly authorized by the
# independent local test-environment request. Never reset an existing account.
listed = subprocess.run(['docker','exec',name,'gitea','admin','user','list','--config','/etc/gitea/app.ini'],stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
if listed.returncode:
    raise RuntimeError('Git fixture user inventory failed')
if account['username'] not in listed.stdout.split():
    result = subprocess.run(['docker','exec',name,'gitea','admin','user','create','--config','/etc/gitea/app.ini',
        '--username',account['username'],'--email',account['email'],'--password',account['password'],
        '--admin','--must-change-password=false'],stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
    if result.returncode:raise RuntimeError('Git fixture account creation failed; private diagnostics suppressed')
api('/api/v1/user')
if not (state/'git-pat.json').exists():
    token = api('/api/v1/users/'+account['username']+'/tokens',{'name':'phase4-original-api','scopes':['write:repository','read:user']})
    save('git-pat.json',{'token':token['sha1'],'id':token['id']})
token = load('git-pat.json')['token']
repos = api('/api/v1/user/repos',token=token)
repo_name = 'remote-agent-acceptance'
matches = [repo for repo in repos if repo['name'] == repo_name]
if len(matches)>1:raise RuntimeError('Conflicting local Git repositories')
repo = matches[0] if matches else api('/api/v1/user/repos',{'name':repo_name,'private':True,'auto_init':True,'default_branch':'main','description':'Independent Phase 4 Agent acceptance'})
if not repo['private'] or repo['owner']['login'] != account['username']:
    raise RuntimeError('Git fixture repository ownership changed')
save('git-fixture.json',{'image':image,'repository_id':repo['id'],'full_name':repo['full_name'],
    'base_url':'http://host.docker.internal:47598','url':'http://host.docker.internal:47598/'+repo['full_name']+'.git','branch':'main'})
print('Independent Gitea, dedicated account/PAT and private main-branch repository are ready; credentials retained privately.')
