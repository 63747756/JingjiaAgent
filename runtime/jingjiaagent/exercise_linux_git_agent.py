"""Real private Git clone/push and project revocation through original Web APIs."""
import base64
import argparse
import json
import secrets
import urllib.parse
import urllib.request
import uuid
from linux_web_common import TaskSocket, api, load, login, request, runtime_rpc, save, sql, state, task_output, wait

owner, owner_user, _ = login()
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--retry-push',action='store_true',help='Explicit new user turn after repairing a failed Git push')
args=parser.parse_args()
member, member_user, jar = login('web-member-account.json')
fixture, git, project = load('web-fixture.json'), load('git-fixture.json'), load('git-web.json')
record = load('git-agent.json') if (state/'git-agent.json').exists() else {'marker':'PUSH_'+secrets.token_hex(12)}
save('git-agent.json',record)
report = load('git-web-report.json')

# Release precisely the named disposable capacity task, via the original stop
# API. Do not delete an arbitrary sandbox or alter a reservation in SQL.
capacity = load('capacity-tasks.json')['member-opencode']
if not record.get('capacity_released'):
    if sql("SELECT active FROM runtime_reservations WHERE environment_id='"+capacity['vm']+"';") == 't':
        api(member,'/api/v1/users/tasks/stop',{'id':capacity['task']},'PUT')
    wait(lambda:sql("SELECT active FROM runtime_reservations WHERE environment_id='"+capacity['vm']+"';")=='f',label='stopped environment capacity release')
    record['capacity_released']=True
    report['web_stop_releases_capacity']=True
    save('git-agent.json',record)

content = 'Git 实际协作验收 '+record['marker']+'。读取中文验收.txt，原样回复完整内容；不要修改文件或提交代码。'
if not record.get('task'):
    matches = sql("SELECT id FROM tasks WHERE content='"+content+"' AND user_id='"+str(uuid.UUID(member_user['id']))+"';").splitlines()
    if len(matches)>1:raise RuntimeError('Duplicate Git acceptance task')
    if matches:record['task']=matches[0]
    else:
        task = api(member,'/api/v1/users/tasks',{'content':content,'host_id':fixture['node_id'],
            'image_id':fixture['image_id'],'model_id':fixture['model_id'],'cli_name':'opencode','task_type':'develop',
            'repo':{'repo_url':git['url'],'branch':'main'},'extra':{'project_id':project['project']},
            'resource':{'core':1,'memory':2<<30,'life':3600}})
        record['task']=task['id']
    save('git-agent.json',record)
task_id=str(uuid.UUID(record['task']))
detail=api(member,'/api/v1/users/tasks/'+task_id)
if detail['user_id']!=member_user['id'] or detail['content']!=content:
    raise RuntimeError('Git acceptance actor/content changed')
def completed():
    value=sql("SELECT state FROM runtime_commands WHERE task_id='"+task_id+"' AND operation='task' ORDER BY turn DESC LIMIT 1;")
    if value in ('failed','canceled'):raise RuntimeError('Git business task '+value)
    return value=='complete'
wait(completed,240,label='private Git clone/read')
if project['marker'] not in task_output(task_id):raise RuntimeError('Real Agent did not read the private clone')
report['real_agent_private_clone']=True
print('Actual private Gitea clone/read passed for a project collaborator.',flush=True)

if not record.get('remote_confirmed'):
    if record.get('push_complete') and not args.retry_push:
        raise RuntimeError('Previous push did not verify remotely; repair it before an explicit new user turn')
    # Explicit original task policy, restricted to this test. Native manual
    # approvals are accepted separately; this does not replace that coverage.
    with TaskSocket(task_id,jar,control=True) as control, TaskSocket(task_id,jar) as stream:
        sandbox=sql("SELECT sandbox_id FROM runtime_environments WHERE id='"+detail['virtualmachine']['id']+"';")
        wait(lambda:runtime_rpc('SandboxService','GetSandbox',{'sandboxId':sandbox})['sandbox']['status']=='SANDBOX_STATUS_RUNNING',
            60,label='original task control resumes the hibernated environment')
        if detail['virtualmachine'].get('status')=='hibernated':
            report['original_control_resumes_hibernated_guest']=True
        stream.send('auto-approve',{})
        def policy_matches(enabled):
            result=runtime_rpc('ExecService','Exec',{'sandboxId':sandbox,'command':{'command':'python3','args':['-c',
                "import json,pathlib; p=pathlib.Path('/data/state/jingjiaagent-policies/"+task_id+".json'); print(json.dumps(json.loads(p.read_text())['enabled'] if p.exists() else None))"]},'timeoutMs':5000,'maxOutputBytes':128})['result']
            return result.get('exitCode',0)==0 and json.loads(result['stdout']) is enabled
        wait(lambda:policy_matches(True),label='original task approval policy')
        prefix='凭证桥修复后请完成实际 Git 推送验收：' if args.retry_push else '请完成实际 Git 推送验收：'
        push_content = (prefix+'在仓库创建 phase4-push.txt，内容仅为 '+record['marker']+
            ' 加换行。执行 git add phase4-push.txt，然后 git -c user.name=Phase4 -c user.email=phase4-owner@example.invalid commit -m "Phase 4 private push acceptance"，再 git push origin main。'
            '成功后回复 '+record['marker']+'。仅修改这个验收文件；如果已经存在相同内容且已推送，不要重复提交。')
        already_sent=False
        for line in sql("SELECT chunk FROM runtime_events WHERE task_id='"+task_id+"' AND chunk->>'event'='user-input';").splitlines():
            raw=base64.b64decode(json.loads(line).get('data',''))
            if base64.b64encode(push_content.encode()) in raw:already_sent=True
        if not already_sent:
            turn=int(sql("SELECT max(turn) FROM runtime_commands WHERE task_id='"+task_id+"' AND operation='task';"))
            stream.send('user-input',{'content':base64.b64encode(push_content.encode()).decode(),'attachments':[]})
            wait(lambda:int(sql("SELECT max(turn) FROM runtime_commands WHERE task_id='"+task_id+"' AND operation='task';"))>turn,label='durable Git follow-up')
    # Browser disconnection must leave the persisted Run executing.
    wait(completed,240,label='real Agent Git commit/push')
    with TaskSocket(task_id,jar,control=True) as control, TaskSocket(task_id,jar) as stream:
        stream.send('disable-auto-approve',{})
        wait(lambda:policy_matches(False),label='restore manual policy')
    record['push_complete']=True
    save('git-agent.json',record)

http=urllib.request.build_opener(urllib.request.ProxyHandler({}))
req=urllib.request.Request('http://127.0.0.1:47598/api/v1/repos/'+git['full_name']+'/contents/phase4-push.txt?ref=main',
    headers={'Authorization':'token '+load('git-pat.json')['token']})
with http.open(req,timeout=20) as response:remote=json.load(response)
if base64.b64decode(remote['content']) != (record['marker']+'\n').encode():
    raise RuntimeError('Remote private repository push content differs')
record['remote_confirmed']=True
save('git-agent.json',record)
report['real_agent_commit_push']=True
report['browser_detach_does_not_cancel_git']=True
with TaskSocket(task_id,jar,control=True) as control:
    files=control.call('repo_file_list',{'path':'/workspace','include_hidden':False})
    if not any(item['name']=='phase4-push.txt' for item in files['files']):raise RuntimeError('Original file control did not list the clone')
    changes=control.call('repo_file_changes',{})
    if changes.get('changes'):raise RuntimeError('Push left unexpected working-tree changes')
report['original_git_file_and_changes_controls']=True

# Directly supplying another user's credential remains denied even though the
# same collaborator can use its project-granted credential through that project.
before=sql('SELECT count(*) FROM runtime_commands;')
status,value=request(member,'/api/v1/users/tasks',{'content':'Denied foreign credential '+record['marker'],
    'host_id':fixture['node_id'],'image_id':fixture['image_id'],'model_id':fixture['model_id'],'cli_name':'opencode',
    'task_type':'develop','git_identity_id':project['identity'],'repo':{'repo_url':git['url'],'branch':'main'},
    'resource':{'core':1,'memory':2<<30,'life':3600}})
if value.get('code')!=10002 or sql('SELECT count(*) FROM runtime_commands;')!=before:
    raise RuntimeError('Direct foreign credential request was not rejected before admission')
report['real_gitea_foreign_credential_denied']=True
restored={'collaborators':[{'user_id':owner_user['id'],'permission':'read_write'},{'user_id':member_user['id'],'permission':'read_write'}]}
api(owner,'/api/v1/users/projects/'+project['project'],{'collaborators':[restored['collaborators'][0]]},'PUT')
try:
    status,value=request(member,'/api/v1/users/projects/'+project['project'])
    if status==200 and value.get('code')==0:raise RuntimeError('Removed Git collaborator retained project access')
    token=(state/'daemon.token').read_text().strip()
    callback=urllib.request.Request('http://127.0.0.1:47424/internal/git-credential',data=json.dumps({'task_id':task_id}).encode(),
        headers={'Content-Type':'application/json','Authorization':'Bearer '+token})
    with http.open(callback,timeout=20) as response:value=json.load(response)
    if not value['data'].get('error') or value['data'].get('password'):raise RuntimeError('Revoked collaborator received Git credentials')
finally:
    api(owner,'/api/v1/users/projects/'+project['project'],restored,'PUT')
report['real_gitea_collaborator_revocation']=True
save('git-web-report.json',report)
print('Actual Agent commit/push, clean changes, credential isolation and collaborator revocation passed.',flush=True)
