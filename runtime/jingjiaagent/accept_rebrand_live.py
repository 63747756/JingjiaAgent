"""Live checks on the newly installed, private JingjiaAgent Web fixture.

Uses genuine model responses and existing business APIs. Saves admission intent
before sending follow-ups so reruns reconcile an uncertain response.
"""
import argparse
import base64
import hashlib
import json
import secrets
import socket
import urllib.parse
import urllib.request
from linux_web_common import TaskSocket, api, load, login, origin, request, runtime_rpc, save, sql, state, task_output, wait
from preview_fixture import frame, read_frame

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('stage', choices=['files', 'model', 'claude-resume', 'mcp', 'terminal', 'isolation', 'persistence'])
args = parser.parse_args()
owner, user, jar = login()
member, _, member_jar = login('web-member-account.json')
tasks = load('capacity-tasks.json')
record = load('rebrand-live.json') if (state/'rebrand-live.json').exists() else {'marker': secrets.token_hex(12), 'stages': {}}
save('rebrand-live.json', record)

def followup(task, actor_jar, prompt, label):
    entry = record['stages'].setdefault(label, {'prompt': prompt})
    if entry['prompt'] != prompt:
        raise RuntimeError('Saved live fixture intent differs')
    save('rebrand-live.json', record)
    if not entry.get('turn'):
        encoded = base64.b64encode(prompt.encode())
        admitted = []
        rows = sql("SELECT json_build_object('turn',turn,'chunk',chunk) FROM runtime_events WHERE task_id='"+task['task']+"' AND chunk->>'event'='user-input';").splitlines()
        for row in rows:
            item=json.loads(row)
            if encoded in base64.b64decode(item['chunk'].get('data','')):
                admitted.append(item['turn'])
        if len(set(admitted)) > 1:
            raise RuntimeError('Duplicate acceptance follow-up')
        if admitted:
            entry['turn'] = admitted[0]
        else:
            previous = int(sql("SELECT max(turn) FROM runtime_commands WHERE task_id='"+task['task']+"' AND operation='task';"))
            with TaskSocket(task['task'],actor_jar) as stream:
                stream.send('user-input', {'content': encoded.decode(), 'attachments': []})
                wait(lambda:int(sql("SELECT max(turn) FROM runtime_commands WHERE task_id='"+task['task']+"' AND operation='task';"))>previous, label='persisted acceptance follow-up')
            entry['turn'] = int(sql("SELECT max(turn) FROM runtime_commands WHERE task_id='"+task['task']+"' AND operation='task';"))
        save('rebrand-live.json', record)
    def complete():
        status = sql("SELECT state FROM runtime_commands WHERE task_id='"+task['task']+"' AND operation='task' AND turn="+str(entry['turn'])+";")
        if status in ('failed','canceled'):
            raise RuntimeError('Acceptance follow-up '+status)
        return status == 'complete'
    wait(complete,240,label='real Agent acceptance follow-up')
    return entry

if args.stage == 'files':
    task=tasks['owner-opencode']; vm=task['vm']
    expected = {'中文上传验收.txt':'中文文件上传验收\n'.encode(), '二进制验收.bin':bytes(range(256))*16, '空文件.txt':b''}
    with TaskSocket(task['task'],jar,control=True):
        for name, data in expected.items():
            boundary='fixture-'+secrets.token_hex(12)
            query=urllib.parse.urlencode({'id':vm,'path':'/workspace/'+name})
            body=(('--'+boundary+'\r\nContent-Disposition: form-data; name="file"; filename="'+name+'"\r\nContent-Type: application/octet-stream\r\n\r\n').encode()+data+('\r\n--'+boundary+'--\r\n').encode())
            req=urllib.request.Request(origin+'/api/v1/users/files/upload?'+query,data=body,headers={'Content-Type':'multipart/form-data; boundary='+boundary})
            with owner.open(req,timeout=60) as response:
                if json.load(response).get('code')!=0:raise RuntimeError('Original file upload failed')
            with owner.open(origin+'/api/v1/users/files/download?'+query,timeout=60) as response:
                if response.read()!=data or response.headers.get('Content-Length')!=str(len(data)):raise RuntimeError('Original file content/length differs')
        edited='中文文件编辑验收\nWEB_EDIT_OK\n'.encode()
        api(owner,'/api/v1/users/files/save',{'id':vm,'path':'/workspace/中文上传验收.txt','content':edited.decode()},'PUT')
        query=urllib.parse.urlencode({'id':vm,'path':'/workspace/中文上传验收.txt'})
        with owner.open(origin+'/api/v1/users/files/download?'+query,timeout=60) as response:
            if response.read()!=edited:raise RuntimeError('Original file edit differs')
    record['stages']['files']={'passed':True,'chinese_binary_empty':True,'edit':True}
elif args.stage in ('model','claude-resume'):
    task=tasks['owner-opencode' if args.stage=='model' else 'owner-claude']
    with TaskSocket(task['task'],jar,control=True) as control:
        if args.stage=='model':
            fixture=load('web-fixture.json')
            if not fixture.get('switch_model_id'):
                admin,_,_=login(team=True); private=load('model.json')
                remark='JingjiaAgent model switch acceptance'
                models=api(admin,'/api/v1/teams/models')['models'] or []
                existing=[m for m in models if m.get('remark')==remark]
                target=existing[0] if existing else api(admin,'/api/v1/teams/models',{'provider':'DeepSeek','api_key':private['api_key'],'base_url':private['base_url'],'model':private['model'],'interface_type':'openai_chat','temperature':.3,'remark':remark,'support_image':False})
                fixture['switch_model_id']=target['id'];save('web-fixture.json',fixture)
            if not record['stages'].get('model_control'):
                result=control.call('switch_model',{'model_id':fixture['switch_model_id'],'load_session':True})
                if not result.get('success'):raise RuntimeError('Model switch incomplete')
                detail=api(owner,'/api/v1/users/tasks/'+task['task'])
                if detail['model']['id']!=fixture['switch_model_id']:raise RuntimeError('Business model did not reconcile')
                record['stages']['model_control']={'passed':True};save('rebrand-live.json',record)
        elif not record['stages'].get('claude_restart'):
            if not control.call('restart',{'load_session':True}).get('success'):raise RuntimeError('Claude restart incomplete')
            record['stages']['claude_restart']={'passed':True};save('rebrand-live.json',record)
    prompt='只回复此标记，不要使用工具：REBRAND_'+args.stage+'_'+record['marker']
    entry=followup(task,jar,prompt,args.stage)
    if 'REBRAND_'+args.stage+'_'+record['marker'] not in task_output(task['task']):raise RuntimeError('Real post-control response missing')
    entry['passed']=True
elif args.stage=='mcp':
    transport=load('web-mcp-transports.json');task=tasks['member-opencode']
    with TaskSocket(task['task'],member_jar,control=True), TaskSocket(task['task'],member_jar) as stream:
        stream.send('auto-approve',{})
    prompt=('使用 MCP 工具 '+transport['stream_tool']['namespaced_name']+'，参数 value=31；再使用 '+transport['legacy_tool']['namespaced_name']+'，参数 value=32。各调用一次，回复工具返回的完整文本。不要创建或修改任何文件。')
    entry=followup(task,member_jar,prompt,'mcp')
    output=task_output(task['task']);fixture=load('mcp-transports.json')
    for which,value in [('stream',31),('legacy',32)]:
        if str(value*2)+' '+fixture[which+'_receipt'] not in output:raise RuntimeError('Real MCP tool receipt missing')
        tool_id=transport[which+'_tool']['id']
        executions=[json.loads(line) for line in (state/'mcp-transports-audit.jsonl').read_text().splitlines()]
        if len([item for item in executions if item['transport']==which and item['value']==value])!=1:
            raise RuntimeError('Real MCP upstream was not executed exactly once for this acceptance value')
        if sql("SELECT count(*) FROM mcp_tool_calls WHERE task_id='"+task['task']+"' AND tool_id='"+tool_id+"' AND args_json->>'value'='"+str(value)+"' AND status='success';")!='1':
            raise RuntimeError('Real MCP task audit missing or duplicated')
    with TaskSocket(task['task'],member_jar) as stream:stream.send('disable-auto-approve',{})
    entry['passed']=True
elif args.stage=='persistence':
    task=tasks['owner-opencode']
    sandbox=sql("SELECT sandbox_id FROM runtime_environments WHERE id='"+task['vm']+"';")
    session=sql("SELECT session_id FROM runtime_task_sessions WHERE task_id='"+task['task']+"';")
    if not session:raise RuntimeError('Real native session missing')
    if not record['stages'].get('sandbox_restart'):
        if sql("SELECT count(*) FROM runtime_commands WHERE task_id='"+task['task']+"' AND state NOT IN ('complete','failed','canceled');")!='0':
            raise RuntimeError('Do not stop a fixture with an executing command')
        runtime_rpc('SandboxService','StopSandbox',{'sandboxId':sandbox})
        wait(lambda:runtime_rpc('SandboxService','GetSandbox',{'sandboxId':sandbox})['sandbox']['status']=='SANDBOX_STATUS_STOPPED',label='actual Sandbox stop')
        runtime_rpc('SandboxService','ResumeSandbox',{'sandboxId':sandbox})
        wait(lambda:runtime_rpc('SandboxService','GetSandbox',{'sandboxId':sandbox})['sandbox']['status']=='SANDBOX_STATUS_RUNNING',label='actual Sandbox resume')
        if sql("SELECT sandbox_id FROM runtime_environments WHERE id='"+task['vm']+"';")!=sandbox or sql("SELECT session_id FROM runtime_task_sessions WHERE task_id='"+task['task']+"';")!=session:
            raise RuntimeError('Sandbox or native session identity changed')
        with TaskSocket(task['task'],jar,control=True):
            for name,expected in [('中文上传验收.txt','中文文件编辑验收\nWEB_EDIT_OK\n'.encode()),('二进制验收.bin',bytes(range(256))*16),('空文件.txt',b'')]:
                with owner.open(origin+'/api/v1/users/files/download?'+urllib.parse.urlencode({'id':task['vm'],'path':'/workspace/'+name}),timeout=60) as response:
                    if response.read()!=expected:raise RuntimeError('Sandbox restart changed a persistent file')
        record['stages']['sandbox_restart']={'passed':True,'same_sandbox':True,'same_session':True,'three_file_types_unchanged':True}
        save('rebrand-live.json',record)
    prompt='只回复此标记，不要使用工具：PERSISTENCE_'+record['marker']
    entry=followup(task,jar,prompt,'persistence')
    if 'PERSISTENCE_'+record['marker'] not in task_output(task['task']):raise RuntimeError('Resumed native Agent output missing')
    entry['passed']=True
elif args.stage=='isolation':
    task=tasks['owner-opencode'];sandbox=sql("SELECT sandbox_id FROM runtime_environments WHERE id='"+task['vm']+"';")
    containers=json.loads(__import__('subprocess').check_output(['docker','inspect','jingjiaagent-postgres-1','jingjiaagent-redis-1','jingjiaagent-storage-1']))
    targets=[]
    for container,ports in zip(containers,[(5432,),(6379,),(9000,9001)]):
        address=container['NetworkSettings']['Networks']['jingjiaagent_business']['IPAddress']
        targets.extend((address,port) for port in ports)
    code='import socket,json\nresults=[]\nfor host,port in '+repr(targets)+':\n try:\n  s=socket.create_connection((host,port),timeout=1);s.close();results.append(True)\n except OSError:results.append(False)\nprint(json.dumps(results))'
    with TaskSocket(task['task'],jar,control=True):
        result=runtime_rpc('ExecService','Exec',{'sandboxId':sandbox,'command':{'command':'python3','args':['-c',code]},'timeoutMs':10000,'maxOutputBytes':1024})['result']
        if result.get('exitCode',0)!=0 or any(json.loads(result['stdout'])):raise RuntimeError('Guest reached business storage directly')
    record['stages']['isolation']={'passed':True,'direct_business_ports_denied':4}
elif args.stage=='terminal':
    task=tasks['owner-opencode'];vm=task['vm'];marker='PTY_'+record['marker']
    def connect(terminal=''):
        terminal=terminal or str(__import__('uuid').uuid4())
        conn=socket.create_connection(('127.0.0.1',47424),timeout=20);reader=conn.makefile('rb');key=base64.b64encode(secrets.token_bytes(16)).decode();cookie='; '.join(c.name+'='+c.value for c in jar)
        route='/api/v1/users/hosts/vms/'+vm+'/terminals/connect?'+urllib.parse.urlencode({'terminal_id':terminal,'col':80,'row':24})
        conn.sendall(('GET '+route+' HTTP/1.1\r\nHost: 127.0.0.1:47424\r\nOrigin: '+origin+'\r\nCookie: '+cookie+'\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: '+key+'\r\nSec-WebSocket-Version: 13\r\n\r\n').encode())
        header=b''
        while not header.endswith(b'\r\n\r\n'):header+=reader.read(1)
        if not header.startswith(b'HTTP/1.1 101'):raise RuntimeError('Terminal handshake denied')
        return conn,reader
    def exchange(conn,reader,command):
        conn.sendall(frame(1,json.dumps({'type':'resize','data':json.dumps({'col':100,'row':30})}).encode(),secrets.token_bytes(4)))
        conn.sendall(frame(1,json.dumps({'type':'data','data':base64.b64encode(command.encode()).decode()}).encode(),secrets.token_bytes(4)))
        output=''
        while marker not in output:
            opcode,raw=read_frame(reader)
            if opcode==9:conn.sendall(frame(10,raw,secrets.token_bytes(4)));continue
            if opcode!=1:raise RuntimeError('Terminal unexpectedly disconnected')
            item=json.loads(raw)
            if item.get('type')=='error':
                message=item.get('data','')
                for secret in (load('model.json')['api_key'], (state/'daemon.token').read_text().strip()):
                    message=message.replace(secret,'[redacted]')
                raise RuntimeError('Terminal returned an error: '+message)
            if item.get('type')=='data':output+=base64.b64decode(item.get('data','')).decode(errors='replace')
        return output
    with TaskSocket(task['task'],jar,control=True):
        conn,reader=connect();exchange(conn,reader,"export JINGJIAAGENT_PTY_TEST='"+marker+"'; printf '%s\\n' \"$JINGJIAAGENT_PTY_TEST\"\n");conn.close();reader.close()
        terminals=api(owner,'/api/v1/users/hosts/vms/'+vm+'/terminals')
        if len(terminals)!=1:raise RuntimeError('Unexpected terminal fixture count')
        terminal=terminals[0]['id'];conn,reader=connect(terminal);exchange(conn,reader,'printf "%s\\n" "$JINGJIAAGENT_PTY_TEST"\n');conn.close();reader.close()
        status,result=request(member,'/api/v1/users/hosts/vms/'+vm+'/terminals/'+terminal,{},'DELETE')
        if status==200 and result.get('code')==0:raise RuntimeError('Foreign user closed terminal')
        api(owner,'/api/v1/users/hosts/vms/'+vm+'/terminals/'+terminal,{},'DELETE')
        if api(owner,'/api/v1/users/hosts/vms/'+vm+'/terminals'):raise RuntimeError('Terminal not closed')
    record['stages']['terminal']={'passed':True,'resize':True,'reconnect_same_shell':True,'foreign_close_denied':True}
save('rebrand-live.json',record)
print('JingjiaAgent live '+args.stage+' acceptance passed.',flush=True)
