"""Regenerate handler Swagger plus existing unannotated Web contracts, then the TS client."""
import json
import os
import pathlib
import shutil
import subprocess
import tempfile

backend = pathlib.Path(__file__).resolve().parents[1]
with tempfile.TemporaryDirectory(prefix='jingjiaagent-openapi-') as tmp:
    subprocess.run(['go', 'run', 'github.com/swaggo/swag/cmd/swag@v1.16.6', 'init',
                    '-ot', 'json', '--pd', '-g', 'cmd/server/main.go', '--output', tmp], cwd=backend, check=True)
    spec = json.loads((pathlib.Path(tmp) / 'swagger.json').read_text())
extra = json.loads((backend / 'openapi/web-extensions.json').read_text())
for name, definition in extra['definitions'].items():
    if name not in spec['definitions']:
        spec['definitions'][name] = definition
    else:
        target = spec['definitions'][name]
        for key, value in definition.get('properties', {}).items():
            if key in target.get('properties', {}):
                raise SystemExit('Extension duplicates handler field: ' + name + '.' + key)
            target.setdefault('properties', {})[key] = value
        if 'enum' in definition:
            target['enum'] = definition['enum']
            target['x-enum-varnames'] = definition.get('x-enum-varnames', [])
for path, methods in extra['paths'].items():
    for verb, operation in methods.items():
        target = spec['paths'].setdefault(path, {})
        if verb in target:
            raise SystemExit('Move this now-annotated Web extension to the handler contract: ' + path)
        target[verb] = operation
spec['info']['title'] = 'JingjiaAgent API'
spec['info']['description'] = '景嘉微AI助手 Web 接口；保留既有业务路径与消息结构。'
output = backend / 'docs/swagger.json'
temporary = output.with_suffix('.json.tmp')
temporary.write_text(json.dumps(spec, ensure_ascii=False, indent=4) + '\n', encoding='utf-8')
temporary.replace(output)
pnpm = shutil.which('pnpm.cmd' if os.name == 'nt' else 'pnpm')
if not pnpm:
    raise SystemExit('pnpm is required to regenerate the Web API client')
subprocess.run([pnpm, 'exec', 'swagger-typescript-api', '--path', str(output), '--output', 'src/api'], cwd=backend.parent / 'frontend', check=True)
