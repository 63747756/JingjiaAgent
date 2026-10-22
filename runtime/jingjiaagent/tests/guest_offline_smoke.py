"""Exercise preinstalled office dependencies and a real CLI without internet."""
import json
import hashlib
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import urllib.request


def main():
    import pandas,openpyxl,docx,matplotlib
    for binary in ('opencode','codex','claude'):
        result=subprocess.run([binary,'--version'],capture_output=True,text=True,timeout=20)
        if result.returncode:raise RuntimeError(binary+' version check failed')
        print(binary,result.stdout.strip())
    server=subprocess.Popen([sys.executable,'/src/tests/work_model_fixture.py'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    try:
        client=urllib.request.build_opener(urllib.request.ProxyHandler({}))
        for attempt in range(30):
            try:client.open('http://127.0.0.1:8080/health',timeout=1).close();break
            except OSError:time.sleep(.1)
        with tempfile.TemporaryDirectory(dir='/tmp') as directory:
            root=Path(directory);home=root/'home';home.mkdir()
            config={'provider':{'fixture':{'npm':'@ai-sdk/openai-compatible','name':'fixture','options':{'baseURL':'http://127.0.0.1:8080/v1','apiKey':'fixture-only'},'models':{'office-fixture':{'name':'office-fixture','limit':{'context':200000,'output':32000}}}}},'permission':'allow'}
            # Native bash uses a login shell, whose /etc/profile can replace
            # image PATH. Exercise the same system PATH rather than relying on
            # Docker's virtualenv-first ENV to hide missing office imports.
            env=dict(os.environ,PATH='/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin',HOME=str(home),OPENCODE_CONFIG_CONTENT=json.dumps(config),OPENCODE_DISABLE_AUTOUPDATE='true',OPENCODE_DISABLE_MODELS_FETCH='1',OPENCODE_DISABLE_DEFAULT_PLUGINS='true',DISABLE_TELEMETRY='1')
            result=subprocess.run(['opencode','run','--format','json','--model','fixture/office-fixture','生成一份工作报告、Excel分析表和PPT。资料是采购12、维护8，允许使用本地工具完成文件。'],cwd=root,env=env,capture_output=True,text=True,timeout=90)
            if result.returncode:raise RuntimeError('offline OpenCode failed: '+result.stderr[-1000:]+result.stdout[-1000:])
            expected=('report.docx','analysis.xlsx','presentation.pptx')
            if not all((root/'.jingjiaagent/output'/name).is_file() for name in expected):raise RuntimeError('offline CLI did not produce expected artifacts: '+result.stdout[-1200:])
            print('offline OpenCode model/SSE/tool/Office delivery passed')
            office_python='/opt/office/venv/bin/python'
            skills=Path('/opt/jingjiaagent/skills')
            sample=root/'sample.csv';sample.write_text('项目,金额\n采购,12\n维护,8\n')
            commands=[
                [office_python,str(skills/'data-analysis/scripts/profile_data.py'),str(sample),'--out',str(root/'.jingjiaagent/output/data-profile')],
                [office_python,str(skills/'report-writing/scripts/build_report.py'),str(skills/'report-writing/assets/report.template.json'),'--output',str(root/'.jingjiaagent/output/skill-report.docx')],
                [office_python,str(skills/'deliverable-check/scripts/check_deliverables.py'),str(root/'.jingjiaagent/output'),'--manifest',str(root/'.jingjiaagent/work/deliverables.json')],
            ]
            for command in commands:
                result=subprocess.run(command,cwd=root,env=env,capture_output=True,text=True,timeout=30)
                if result.returncode:raise RuntimeError('official Skill failed under login PATH: '+result.stderr[-1000:])
            print('official Python Skills passed under native login PATH')
            # Exercise the production HTTP native bridge with the Work input
            # manifest. Its path/hash checks differ from the direct CLI above.
            task='11111111-1111-1111-1111-111111111111'
            command='22222222-2222-2222-2222-222222222222'
            workspace=Path('/workspace');source=workspace/'.jingjiaagent/attachments'/task/command/'1-acceptance.csv'
            source.parent.mkdir(parents=True,exist_ok=True)
            content='项目,金额\n采购,12\n维护,8\n'.encode();source.write_bytes(content)
            state=Path('/data/state/jingjiaagent-attachments');state.mkdir(parents=True,exist_ok=True)
            (state/(task+'.json')).write_text(json.dumps({'task_id':task,'command_id':command,'work_mode':True,
                'files':[{'path':str(source),'filename':'acceptance.csv','mime':'text/csv','size':len(content),'sha256':hashlib.sha256(content).hexdigest()}]}))
            env.update(AGENT_COMPOSE_RUN_ID='office-work-native-smoke',JINGJIAAGENT_TASK_ID=task)
            result=subprocess.run([sys.executable,'/opt/agent-compose-runtime/jingjiaagent-opencode.py','run',
                '--dir',str(workspace),'--format','json','--model','fixture/office-fixture',
                '根据授权资料生成工作报告、Excel分析表和PPT，允许本地工具完成文件。'],env=env,capture_output=True,text=True,timeout=90)
            if result.returncode:raise RuntimeError('offline native bridge failed: '+result.stdout[-1200:])
            if not all((workspace/'.jingjiaagent/output'/name).is_file() for name in expected):raise RuntimeError('native bridge did not generate office deliverables')
            context=json.loads((workspace/'.jingjiaagent/work/context.json').read_text())
            if context['input_sha256']!=hashlib.sha256(content).hexdigest() or context['source_rows']!=2 or context['total']!='20':
                raise RuntimeError('native Office generation did not consume the actual pinned CSV')
            print('offline native HTTP bridge/Work input/hash/Office delivery passed')
            result=subprocess.run(['node','--input-type=module','-e',
                "import {nativeAttachments} from '/opt/agent-compose-runtime/dist/jingjiaagent-assets.js';"
                "const selected=nativeAttachments('验收');if(selected.files.length!==0||!selected.text.includes('/workspace/.jingjiaagent/attachments/'))throw new Error('Work input adapter failed');"],
                env=env,capture_output=True,text=True,timeout=15)
            if result.returncode:raise RuntimeError('Codex/Claude Work input adapter smoke failed')
            print('native Codex/Claude shared Work input adapter passed')
    finally:
        server.terminate();server.wait(timeout=5)


if __name__=='__main__':main()
