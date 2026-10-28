"""Exercise preinstalled office dependencies and a real CLI without internet."""
import json
import hashlib
from importlib.metadata import version
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import tarfile
import time
import urllib.request


def check(command, *, cwd, env, expected=0):
    result = subprocess.run(command, cwd=cwd, env=env, capture_output=True, text=True, timeout=60)
    if result.returncode != expected:
        raise RuntimeError(f'{command[0]} exited {result.returncode}, expected {expected}: '
                           + result.stderr[-1500:] + result.stdout[-1500:])
    return result.stdout


def check_guest_tools(root, env):
    """Run under the native login PATH, with Docker --network none."""
    import requests
    import yaml
    from bs4 import BeautifulSoup

    python = '/opt/office/venv/bin/python'
    for name in ('requirements.lock', 'requirements.guest.lock'):
        for line in (Path('/opt/office') / name).read_text().splitlines():
            if line and not line.startswith('#'):
                package, locked = line.split('==')
                if version(package) != locked:
                    raise RuntimeError(f'{package} does not match the image dependency lock')
    check([python, '-m', 'pip', 'check'], cwd=root, env=env)
    with requests.Session() as client:
        client.trust_env = False
        response = client.get('http://127.0.0.1:8080/health', timeout=5)
        response.raise_for_status()
    assert yaml.safe_load('title: 本地资料')['title'] == '本地资料'
    assert BeautifulSoup('<h1>采购报告</h1>', 'html.parser').h1.text == '采购报告'
    for command in (['curl', '--version'], ['jq', '--version'], ['zip', '-v'],
                    ['unzip', '-v'], ['file', '--version'], ['ssh', '-V'], ['rg', '--version']):
        check(command, cwd=root, env=env)
    check(['curl', '--noproxy', '*', '--fail', '--silent', 'http://127.0.0.1:8080/health'], cwd=root, env=env)
    document = root / 'tools.json'
    document.write_text('{"amount":20}', encoding='utf-8')
    assert check(['jq', '-r', '.amount', str(document)], cwd=root, env=env).strip() == '20'
    assert 'amount' in check(['rg', 'amount', str(document)], cwd=root, env=env)
    check(['zip', '-q', 'tools.zip', 'tools.json'], cwd=root, env=env)
    check(['unzip', '-q', 'tools.zip', '-d', 'unpacked'], cwd=root, env=env)
    assert (root / 'unpacked/tools.json').read_bytes() == document.read_bytes()
    assert 'JSON' in check(['file', '-b', str(document)], cwd=root, env=env)

    # A fresh store and a local tarball prove pnpm works without Corepack fetching
    # itself or registry access. Project dependencies still need an offline store.
    assert check(['pnpm', '--version'], cwd=root, env=env).strip() == '10.34.5'
    package = root / 'local-package'
    package.mkdir()
    (package / 'package.json').write_text('{"name":"local-fixture","version":"1.0.0","main":"index.js"}')
    (package / 'index.js').write_text('module.exports = 42;')
    with tarfile.open(root / 'local-fixture.tgz', 'w:gz') as archive:
        archive.add(package, arcname='package')
    project = root / 'pnpm-project'
    project.mkdir()
    (project / 'package.json').write_text(json.dumps({'private': True, 'packageManager': 'pnpm@10.34.5',
        'dependencies': {'local-fixture': 'file:../local-fixture.tgz'}}))
    check(['pnpm', 'install', '--offline', '--ignore-scripts', '--store-dir', str(root / 'pnpm-store')], cwd=project, env=env)
    check(['node', '-e', "if(require('local-fixture')!==42)process.exit(1)"], cwd=project, env=env)
    check_pnpm_archive_boundary(root, env)
    test = root / 'test_sample.py'
    test.write_text('def test_total():\n    assert 12 + 8 == 20\n')
    check([python, '-m', 'pytest', '-q', str(test)], cwd=root, env=env)
    failing_test = root / 'test_failure.py'
    failing_test.write_text('def test_total():\n    assert 12 + 8 == 21\n')
    check([python, '-m', 'pytest', '-q', str(failing_test)], cwd=root, env=env, expected=1)
    test.write_text('import os\n')
    check(['/opt/office/venv/bin/ruff', 'check', '--no-cache', '--isolated', str(test)], cwd=root, env=env, expected=1)
    test.write_text('value = 20\n')
    check(['/opt/office/venv/bin/ruff', 'check', '--no-cache', '--isolated', str(test)], cwd=root, env=env)
    installed_env = dict(env, JINGJIAAGENT_SKILLS_ROOT='/opt/jingjiaagent/skills', PYTHONDONTWRITEBYTECODE='1')
    check([python, '-m', 'unittest', 'discover', '-s', '/src/tests', '-p', 'test_*_tools.py', '-v'], cwd=root, env=installed_env)
    print('Guest dependency locks, system tools, pnpm offline install, pytest/ruff and installed PDF/PPT regressions passed')


def check_pnpm_archive_boundary(root, env):
    """GHSA-vq4v-j7r6-jq4m: --ignore-scripts must not permit package path traversal."""
    project = root / 'pnpm-boundary-project'
    project.mkdir()
    outside = root / 'outside-project'
    outside.mkdir()
    marker = outside / 'marker.txt'
    marker.write_text('original')
    package = root / 'traversal-package'
    package.mkdir()
    (package / 'package.json').write_text(json.dumps({
        'name': '@x/' + '../' * 25 + outside.as_posix().lstrip('/'), 'version': '1.0.0'}))
    (package / 'marker.txt').write_text('overwritten')
    archive_path = root / 'traversal-fixture.tgz'
    with tarfile.open(archive_path, 'w:gz') as archive:
        archive.add(package, arcname='package')
    (project / 'package.json').write_text(json.dumps({'private': True,
        'dependencies': {'boundary-fixture': 'file:../traversal-fixture.tgz'}}))
    result = subprocess.run(['pnpm', 'install', '--offline', '--ignore-scripts',
        '--store-dir', str(root / 'boundary-store')], cwd=project, env=env,
        capture_output=True, text=True, timeout=30)
    if result.returncode == 0 or marker.read_text() != 'original':
        raise RuntimeError('pnpm accepted a package that writes outside the project')
    if 'ERR_PNPM_INVALID_DEPENDENCY_NAME' not in result.stdout + result.stderr:
        raise RuntimeError('pnpm failed for an unrelated reason: ' + result.stdout[-1000:] + result.stderr[-1000:])
    print('pnpm rejects the path-traversal tarball without changing the outside file')


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
            check_guest_tools(root, env)
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
                '--dir',str(workspace),'--format','json','--model','fixture/office-fixture','--prompt-stdin'],
                input='根据授权资料生成工作报告、Excel分析表和PPT，允许本地工具完成文件。',env=env,capture_output=True,text=True,encoding='utf-8',timeout=90)
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
