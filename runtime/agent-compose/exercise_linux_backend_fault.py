"""Kill only the isolated Linux business backend during a real Agent tool.

The runtime and Guest keep running. A saved admission is reconciled, never
replayed; the test resumes verification after an interrupted runner.
"""
import base64
import json
import secrets
import subprocess
import time
import uuid
from linux_web_common import TaskSocket, api, load, login, root, runtime_rpc, save, sql, state, task_output, wait

http, user, jar = login()
task = load('capacity-tasks-p17.json')['owner-opencode']
task_id = str(uuid.UUID(task['task']))
detail = api(http, '/api/v1/users/tasks/'+task_id)
if detail['user_id'] != user['id'] or detail['virtualmachine']['id'] != task['vm']:
    raise RuntimeError('Dedicated backend-fault task scope changed')
record = load('backend-fault.json') if (state/'backend-fault.json').exists() else {'name':'backend-fault-'+secrets.token_hex(8), 'receipt':secrets.token_hex(20)}
save('backend-fault.json', record)
sandbox = sql("SELECT sandbox_id FROM runtime_environments WHERE id='"+task['vm']+"';")

def guest(code):
    result = runtime_rpc('ExecService', 'Exec', {'sandboxId':sandbox,
        'command':{'command':'python3','args':['-c',code]},'timeoutMs':5000,'maxOutputBytes':4096})['result']
    if result.get('exitCode',0) != 0:raise RuntimeError('Dedicated fault fixture probe failed')
    return result.get('stdout','').strip()

script = '/workspace/'+record['name']+'.py'
counter = '/workspace/'+record['name']+'.count'
prompt = '执行后台重启验收：只运行一次 python3 '+script+'，等待其结束并原样回复输出；不要查看脚本内容、不要重复执行，不修改其他文件。'

if not record.get('turn'):
    with TaskSocket(task_id,jar,control=True) as control, TaskSocket(task_id,jar) as stream:
        wait(lambda:runtime_rpc('SandboxService','GetSandbox',{'sandboxId':sandbox})['sandbox']['status']=='SANDBOX_STATUS_RUNNING',60,label='fault task resume')
        source = 'import pathlib,time\np=pathlib.Path('+repr(counter)+')\nwith p.open("a") as f: f.write("once\\n"); f.flush()\ntime.sleep(70)\nprint('+repr(record['receipt'])+')\n'
        guest('import pathlib,base64; p=pathlib.Path('+repr(script)+'); p.write_bytes(base64.b64decode('+repr(base64.b64encode(source.encode()).decode())+'))')
        stream.send('auto-approve',{})
        def policy(enabled):
            return guest('import pathlib,json; p=pathlib.Path("/data/state/monkeycode-policies/'+task_id+'.json"); print(json.dumps(json.loads(p.read_text())["enabled"] if p.exists() else None))') == json.dumps(enabled)
        wait(lambda:policy(True),label='fault fixture explicit approval policy')
        # Recover an ambiguous Web follow-up by the exact saved prompt rather
        # than submitting another turn.
        already = any(base64.b64encode(prompt.encode()) in base64.b64decode(json.loads(line).get('data',''))
            for line in sql("SELECT chunk FROM runtime_events WHERE task_id='"+task_id+"' AND chunk->>'event'='user-input';").splitlines())
        previous = int(sql("SELECT coalesce(max(turn),0) FROM runtime_commands WHERE task_id='"+task_id+"' AND operation='task';"))
        if not already:
            stream.send('user-input',{'content':base64.b64encode(prompt.encode()).decode(),'attachments':[]})
            wait(lambda:int(sql("SELECT max(turn) FROM runtime_commands WHERE task_id='"+task_id+"' AND operation='task';"))>previous,label='fault durable admission')
        record['turn'] = int(sql("SELECT max(turn) FROM runtime_commands WHERE task_id='"+task_id+"' AND operation='task';"))
        save('backend-fault.json',record)

where = "task_id='"+task_id+"' AND operation='task' AND turn="+str(record['turn'])
if not record.get('killed'):
    wait(lambda:guest('import pathlib; p=pathlib.Path('+repr(counter)+'); print(p.read_text().count("once\\n") if p.exists() else 0)')=='1',180,label='real Agent started its single tool')
    command = json.loads(sql('SELECT row_to_json(c) FROM (SELECT id,run_id,state,event_offset FROM runtime_commands WHERE '+where+') c;'))
    if command['state']=='complete' or not command['run_id']:
        raise RuntimeError('Tool completed before backend fault; do not replay it')
    record['command'] = command
    record['watermark'] = int(sql("SELECT coalesce(max(seq),0) FROM runtime_events WHERE task_id='"+task_id+"';"))
    record['killed'] = True
    save('backend-fault.json',record)
    project = subprocess.check_output(['docker','inspect','--format','{{index .Config.Labels "com.docker.compose.project"}}','jingjia-phase4-web-backend-1'],text=True).strip()
    if project != 'jingjia-phase4-web':raise RuntimeError('Refusing to kill a different backend')
    subprocess.run(['docker','kill','--signal','KILL','jingjia-phase4-web-backend-1'],check=True,stdout=subprocess.DEVNULL)
    print('Killed the isolated Linux backend while the real Agent tool was running.',flush=True)
    # The node remains reachable while the business API is unavailable.
    if runtime_rpc('SandboxService','GetSandbox',{'sandboxId':sandbox})['sandbox']['status']!='SANDBOX_STATUS_RUNNING':
        raise RuntimeError('Backend failure also stopped the Guest')

if not record.get('recovered'):
    subprocess.run([__import__('sys').executable,str(root/'start_linux_web.py')],check=True,stdout=subprocess.DEVNULL)
    record['recovered']=True
    save('backend-fault.json',record)

wait(lambda:sql('SELECT state FROM runtime_commands WHERE '+where+';')=='complete',240,label='fresh backend Worker drains the admitted Run')
fresh=json.loads(sql('SELECT row_to_json(c) FROM (SELECT id,run_id,state,event_offset FROM runtime_commands WHERE '+where+') c;'))
if fresh['id']!=record['command']['id'] or fresh['run_id']!=record['command']['run_id']:
    raise RuntimeError('Backend restart changed or replayed the admitted Run')
if guest('import pathlib; print(pathlib.Path('+repr(counter)+').read_text().count("once\\n"))')!='1':
    raise RuntimeError('Agent tool was executed more than once')
if record['receipt'] not in task_output(task_id):raise RuntimeError('Durable output lost the real tool receipt')
if int(sql("SELECT max(seq) FROM runtime_events WHERE task_id='"+task_id+"';"))<=record['watermark']:
    raise RuntimeError('No events were recovered after the backend restart')
if sql("SELECT active FROM runtime_reservations WHERE environment_id='"+task['vm']+"';")!='t':
    raise RuntimeError('Backend restart released live environment capacity')
with TaskSocket(task_id,jar,control=True) as control, TaskSocket(task_id,jar) as stream:
    stream.send('disable-auto-approve',{})
    wait(lambda:guest('import json; print(json.dumps(json.load(open("/data/state/monkeycode-policies/'+task_id+'.json"))["enabled"]))')=='false',label='restore fault task manual approval')
report={'actual_backend_sigkill':True,'same_command_and_run':True,'tool_executed_once':True,
    'durable_events_recovered':True,'browser_detach_does_not_cancel':True,'capacity_retained':True,'manual_approval_restored':True}
save('backend-fault-report.json',report)
print('Fresh backend Worker recovered the same real Run, complete output and one tool execution; capacity retained.',flush=True)
