"""Read-only final checks against the current backend and installed PoC node."""
import base64
import http.cookiejar
import json
import pathlib
import re
import ssl
import urllib.error
import urllib.request
import uuid

root=pathlib.Path(__file__).resolve().parent
state=root/'.state'
node=json.loads((state/'web-installer-config.json').read_text())['nodes'][0]
platform='http://127.0.0.1:47424'
def client():
    return urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def request(opener,path,body=None):
    req=urllib.request.Request(platform+path,data=json.dumps(body).encode() if body is not None else None,headers={'Content-Type':'application/json'})
    try:
        response=opener.open(req,timeout=15)
    except urllib.error.HTTPError as error:
        response=error
    with response:
        return response.code,dict(response.headers),json.load(response)
def api(opener,path,body=None):
    status,headers,result=request(opener,path,body)
    if status!=200 or result.get('code')!=0: raise SystemExit('Original API check failed: '+path)
    return result['data'],headers
admin=client(); api(admin,'/api/v1/teams/users/login',json.loads((state/'web-account.json').read_text()))
team_hosts,_=api(admin,'/api/v1/teams/hosts')
match=[h for h in team_hosts['hosts'] if h['id']==node['id']]
if len(match)!=1 or match[0]['status']!='online': raise SystemExit('Installed node is not online in the original team API.')
command,headers=api(admin,'/api/v1/teams/hosts/install-command')
if headers.get('Cache-Control')!='no-store' or not re.search(r'token=[0-9a-f-]{36}',command['command']): raise SystemExit('Install command cache or shape failed.')
personal=client(); api(personal,'/api/v1/users/password-login',json.loads((state/'web-account.json').read_text()))
# This fork has separate personal/team-administrator accounts with the same
# test email. Email equality cannot grant the personal account this team slot.
if request(personal,'/api/v1/users/hosts/install-command')[0]!=403:
    raise SystemExit('Personal account inherited a different team administrator installation scope.')
member=client(); api(member,'/api/v1/users/password-login',json.loads((state/'web-member-account.json').read_text()))
if request(member,'/api/v1/users/hosts/install-command')[0]!=403: raise SystemExit('Member obtained personal installer credentials.')
anonymous=client()
status,headers,result=request(anonymous,'/api/v1/users/hosts/install?token='+str(uuid.uuid4()))
if status!=403 or result.get('code')!=10234 or '安装票据' not in result.get('message','') or headers.get('Cache-Control')!='no-store': raise SystemExit('Current standalone installer error did not return the expected Chinese contract.')
context=ssl.create_default_context(cafile=node['ca_file'])
req=urllib.request.Request(node['url']+'/internal/jingjiaagent/node',headers={'Authorization':'Bearer '+pathlib.Path(node['token_file']).read_text().strip()})
with urllib.request.urlopen(req,context=context,timeout=5) as response: snapshot=json.load(response)
script=(state/'installer/install.sh').read_text()
match=re.search(r"base64.b64decode\('([A-Za-z0-9+/=]+)'\)",script)
if not match: raise SystemExit('Private installer evidence unavailable.')
payload=json.loads(base64.b64decode(match.group(1)))
ready,_=api(anonymous,'/api/v1/users/hosts/install-status',{'token':payload['ticket'],'instance_id':snapshot['instance_id'],'fingerprint':snapshot['fingerprint']})
if not ready.get('ready'): raise SystemExit('Completed installation failed confirmation after backend restart.')
report={'original_team_host_online':True,'original_team_command_shape':True,'command_not_cached':True,
        'personal_account_does_not_inherit_team_admin_installation':True,'ordinary_member_personal_install_denied':True,
        'current_chinese_error_contract':True,'actual_tls_node_metadata':True,'confirmation_after_backend_restart':True,
        'no_agent_or_model_called':True}
(state/'installer-current-checks.json').write_text(json.dumps(report,indent=2))
print(json.dumps(report,indent=2))
