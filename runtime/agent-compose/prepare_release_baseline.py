"""Reconstruct the last accepted backend with only the route fix reverted.

Writes an isolated build tree; never edits the current checkout. Retains the
Git helper idempotency repair in both candidates. The original unretained image
manifest is unavailable, so record the compatible source reconstruction
separately and tag both images before removing a running container.
"""
import json
import pathlib
import shutil
import subprocess
from linux_web_common import load, save, state

root = pathlib.Path(__file__).resolve().parent
repo = root.parent.parent
target = state / 'release-baseline-source'
target.mkdir(exist_ok=True)
files = subprocess.check_output(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard', 'backend'], cwd=repo).decode().split('\0')
for name in filter(None, files):
    source = repo / name
    if not source.is_file():
        continue
    destination = target / name
    if not destination.resolve().is_relative_to(target.resolve()):
        raise RuntimeError('Baseline build source escaped its dedicated directory')
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, destination)
shutil.copyfile(repo / '.gitignore', target / '.gitignore')
handler = target / 'backend/biz/host/handler/v1/internal.go'
text = handler.read_text(encoding='utf-8')
current = '\t// Changing the default routes only new environments. Mapped compose\n\t// environments still need their scoped credential bridge after rollback.\n\tif cfg.Runtime.Backend == "agent_compose" || len(cfg.Runtime.Nodes) != 0 || cfg.Runtime.NodesJSON != "" {'
previous = '\tif cfg.Runtime.Backend == "agent_compose" {'
if text.count(current) != 1:
    raise RuntimeError('Cannot identify the single accepted baseline difference')
handler.write_text(text.replace(current, previous), encoding='utf-8', newline='\n')
git_dir = pathlib.Path(subprocess.check_output(['git', 'rev-parse', '--absolute-git-dir'], cwd=repo).decode().strip())
if not git_dir.is_dir():
    raise RuntimeError('Baseline VCS metadata must be a verified Git directory')
context = state / 'release-baseline-image'
(context / '.build').mkdir(parents=True, exist_ok=True)
shutil.copyfile(root / 'Dockerfile.web-backend', context / 'Dockerfile')
shutil.copytree(root / '.build/web-migration', context / '.build/web-migration', dirs_exist_ok=True)
old = load('release-baseline.json')
if not (state / 'release-unavailable-original-image.json').exists():
    save('release-unavailable-original-image.json', old)
candidate = json.loads(subprocess.check_output(['docker', 'image', 'inspect', 'jingjia-monkeycode-web:phase4']))[0]['Id']
subprocess.run(['docker', 'tag', candidate, 'jingjia-monkeycode-web:phase4-candidate'], check=True)
subprocess.run(['docker', 'run', '--rm', '--name', 'jingjia-phase4-baseline-build', '--cpus', '3', '--memory', '5g',
    '-v', str(target) + ':/repo', '-v', str(git_dir) + ':/repo/.git:ro',
    '-v', str(context / '.build') + ':/release',
    '-v', 'jingjia-runtime-go-modules:/go/pkg/mod', '-v', 'jingjia-runtime-go-build-cache:/root/.cache/go-build',
    '-w', '/repo/backend', '-e', 'CGO_ENABLED=0', 'golang:1.26.2-bookworm',
    'go', 'build', '-p', '2', '-trimpath', '-o', '/release/monkeycode-server-linux', './cmd/server'], check=True)
subprocess.run(['docker', 'build', '-t', 'jingjia-monkeycode-web:phase4-baseline', str(context)], check=True)
old['backend'] = json.loads(subprocess.check_output(['docker', 'image', 'inspect', 'jingjia-monkeycode-web:phase4-baseline']))[0]['Id']
save('release-baseline.json', old)
save('release-baseline-reconstruction.json', {'source_difference': 'Only remove the Git route registration fix',
    'current_checkout_unchanged': True, 'baseline_image': old['backend'], 'candidate_image': candidate})
print('Previous accepted source reconstructed and both release images retained by explicit tags.', flush=True)
