"""Submit real Claude Web interactions, then verify actual browser replies.

This script never answers a question or approval. Use the original page to
answer; persisted prompts prevent repeated submission after a lost response.
"""
import argparse
import base64
import json
import secrets
from linux_web_common import TaskSocket, api, load, login, runtime_rpc, save, sql, state, task_output, wait

parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('stage',choices=['question','allow','deny','cancel'])
parser.add_argument('--verify',action='store_true')
args=parser.parse_args()
http,user,jar=login()
task=load('capacity-tasks-p17.json')['owner-claude']
detail=api(http,'/api/v1/users/tasks/'+task['task'])
if detail['user_id']!=user['id'] or detail['cli_name']!='claude' or detail['virtualmachine']['id']!=task['vm']:
    raise RuntimeError('Native Web control fixture scope changed')
record=load('web-native-controls.json') if (state/'web-native-controls.json').exists() else {'marker':'UI_'+secrets.token_hex(8),'stages':{}}
save('web-native-controls.json',record)
sandbox=sql("SELECT sandbox_id FROM runtime_environments WHERE id='"+task['vm']+"';")
name='/workspace/web-native-'+args.stage+'-'+record['marker']+'.txt'
prompt=("Use AskUserQuestion now to ask exactly one question '"+('阶段4页面取消问题' if args.stage=='cancel' else '阶段4页面问题')+"' with options '暂不执行' and '"+record['marker']+"'. Do not use any other tools. Wait for the user answer, then reply only with the selected label.") if args.stage in ('question','cancel') else (
    "Use Bash exactly once: printf '"+record['marker']+"' > "+name+" . Wait for approval. Do not use other tools, retry or bypass denial. Then reply "+('ALLOWED_'+record['marker'] if args.stage=='allow' else 'DENIED_'+record['marker'])+"; if the command is denied, reply DENIED_"+record['marker']+".")

def probe(code):
    result=runtime_rpc('ExecService','Exec',{'sandboxId':sandbox,'command':{'command':'python3','args':['-c',code]},'timeoutMs':5000,'maxOutputBytes':4096})['result']
    if result.get('exitCode',0)!=0:raise RuntimeError('Native Web control fixture probe failed')
    return result.get('stdout','').strip()
def pending(turn):
    for line in sql("SELECT chunk FROM runtime_events WHERE task_id='"+task['task']+"' AND turn="+str(turn)+" AND source_key LIKE 'interaction/%' ORDER BY seq DESC;").splitlines():
        chunk=json.loads(line)
        if chunk.get('event')=='task-running':
            native=json.loads(base64.b64decode(chunk['data'])).get('toolCall',{}).get('rawInput',{})
            if native.get('request_kind') in ('question','permission'):return native
    status=sql("SELECT state FROM runtime_commands WHERE task_id='"+task['task']+"' AND operation='task' AND turn="+str(turn)+";")
    if status in ('complete','failed','canceled'):raise RuntimeError('Native browser interaction was not requested before '+status)
    return False

with TaskSocket(task['task'],jar,control=True) as control:
    wait(lambda:runtime_rpc('SandboxService','GetSandbox',{'sandboxId':sandbox})['sandbox']['status']=='SANDBOX_STATUS_RUNNING',60,label='native Web task original resume')
    if args.verify:
        stage=record['stages'][args.stage]
        if args.stage=='cancel':
            wait(lambda:sql("SELECT state FROM runtime_commands WHERE task_id='"+task['task']+"' AND operation='task' AND turn="+str(stage['turn'])+";")=='canceled',180,label='original browser cancel confirmed by native Run')
            command=sql("SELECT run_id FROM runtime_commands WHERE task_id='"+task['task']+"' AND operation='task' AND turn="+str(stage['turn'])+";")
            if runtime_rpc('RunService','GetRun',{'runId':command})['run']['summary']['status']!='RUN_STATUS_CANCELED':raise RuntimeError('Browser cancel not confirmed by actual Run')
            before=sql("SELECT count(*) FROM runtime_commands WHERE task_id='"+task['task']+"' AND operation='interaction';")
            with TaskSocket(task['task'],jar) as stream:
                stream.send('reply-question',{'request_id':stage['pending_id'],'answers_json':json.dumps({'阶段4页面取消问题':[record['marker']]}),'cancelled':False})
                kind,raw=stream.read_frame(stream.reader)
                if kind!=1 or json.loads(raw).get('type')!='error':raise RuntimeError('Canceled original browser interaction was not rejected')
            if sql("SELECT count(*) FROM runtime_commands WHERE task_id='"+task['task']+"' AND operation='interaction';")!=before:raise RuntimeError('Canceled answer was admitted')
            if runtime_rpc('SandboxService','GetSandbox',{'sandboxId':sandbox})['sandbox']['status']!='SANDBOX_STATUS_RUNNING':raise RuntimeError('Cancel recycled the environment')
            stage.update(verified=True,actual_run_cancel_confirmed=True,stale_answer_rejected=True,environment_retained=True)
            save('web-native-controls.json',record)
            print('Real native cancel through the original browser confirmed; old answer rejected and environment retained.',flush=True)
            raise SystemExit(0)
        wait(lambda:sql("SELECT state FROM runtime_commands WHERE task_id='"+task['task']+"' AND operation='task' AND turn="+str(stage['turn'])+";")=='complete',180,label='original browser answer resumes native Run')
        expected=record['marker'] if args.stage=='question' else ('ALLOWED_' if args.stage=='allow' else 'DENIED_')+record['marker']
        if expected not in task_output(task['task']):raise RuntimeError('Selected browser answer missing from durable output')
        if args.stage!='question':
            actual=probe('import pathlib,json; p=pathlib.Path('+repr(name)+'); print(json.dumps(p.read_text() if p.exists() else None))')
            if json.loads(actual)!=(record['marker'] if args.stage=='allow' else None):raise RuntimeError('Browser approval allowed/denied side effect differs')
        stage['verified']=True
        save('web-native-controls.json',record)
        print('Real native '+args.stage+' answered through the original browser and verified.',flush=True)
    else:
        if args.stage not in record['stages']:
            with TaskSocket(task['task'],jar) as stream:
                stream.send('disable-auto-approve',{})
                wait(lambda:probe('import pathlib,json; p=pathlib.Path("/data/state/monkeycode-policies/'+task['task']+'.json"); print(json.dumps(json.loads(p.read_text())["enabled"] if p.exists() else False))')=='false',label='manual native browser approval policy')
                already=any(base64.b64encode(prompt.encode()) in base64.b64decode(json.loads(line).get('data',''))
                    for line in sql("SELECT chunk FROM runtime_events WHERE task_id='"+task['task']+"' AND chunk->>'event'='user-input';").splitlines())
                previous=int(sql("SELECT max(turn) FROM runtime_commands WHERE task_id='"+task['task']+"' AND operation='task';"))
                if not already:
                    stream.send('user-input',{'content':base64.b64encode(prompt.encode()).decode(),'attachments':[]})
                    wait(lambda:int(sql("SELECT max(turn) FROM runtime_commands WHERE task_id='"+task['task']+"' AND operation='task';"))>previous,label='native Web control durable turn')
                record['stages'][args.stage]={'turn':int(sql("SELECT max(turn) FROM runtime_commands WHERE task_id='"+task['task']+"' AND operation='task';"))}
                save('web-native-controls.json',record)
        stage=record['stages'][args.stage]
        native=wait(lambda:pending(stage['turn']),120,label='real native browser question/approval')
        if native['request_kind']!=('question' if args.stage in ('question','cancel') else 'permission'):raise RuntimeError('Native browser control kind differs')
        if args.stage not in ('question','cancel') and probe('import pathlib; print(pathlib.Path('+repr(name)+').exists())')!='False':raise RuntimeError('Native tool ran before browser approval')
        stage['pending_id']=native['request_id']
        stage['no_prior_side_effect']=True
        save('web-native-controls.json',record)
        print('Real native '+args.stage+' waits in the original browser; no tool executed before approval.',flush=True)
