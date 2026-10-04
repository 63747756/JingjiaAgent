"""Fetch verified upstream source, apply explicit patches and build both images."""
import argparse
import hashlib
import json
import pathlib
import subprocess
import shutil
import sys
import tarfile
import urllib.request

root = pathlib.Path(__file__).resolve().parent
lock = json.loads((root / 'source.lock.json').read_text())
parser = argparse.ArgumentParser()
parser.add_argument('--go-image', default='golang:1.26.2-bookworm')
parser.add_argument('--no-prepare', action='store_true', help='Build versioned images without replacing deployed local fixture configuration')
args = parser.parse_args()
build = root / '.build'
build.mkdir(exist_ok=True)
archive = build / 'source.tar.gz'
if not archive.exists():
    with urllib.request.urlopen(lock['archive_url'],timeout=120) as source, archive.open('wb') as target:
        while chunk := source.read(1024*1024): target.write(chunk)
if hashlib.sha256(archive.read_bytes()).hexdigest() != lock['archive_sha256']:
    raise SystemExit('Upstream archive SHA256 mismatch; remove the invalid .build/source.tar.gz before retrying')
source = build / ('agent-compose-' + lock['commit'])
if not source.exists():
    with tarfile.open(archive) as tar:
        tar.extractall(build,filter='data')
subprocess.run([sys.executable,str(root / lock['patch_script']),str(source)],check=True)
shutil.copyfile(root.parent.parent / 'backend/pkg/runtimeadapter/guest/interaction.py', build / 'monkeycode-interaction.py')
subprocess.run(['docker','run','--rm','--env','CGO_ENABLED=0',
    '--mount','type=bind,source='+str(source)+',target=/src',
    '--mount','type=volume,source=jingjia-runtime-go-modules,target=/go/pkg/mod',
    '--mount','type=volume,source=jingjia-runtime-go-build-cache,target=/root/.cache/go-build',
    '--workdir','/src',args.go_image,'go','build','-trimpath','-o','/src/agent-compose-linux','./cmd/agent-compose'],check=True)
for kind in ['daemon','guest']:
    revision = lock.get('guest_patch_revision', lock['patch_revision']) if kind == 'guest' else lock['patch_revision']
    tag = 'jingjia-agent-' + ('runtime' if kind=='daemon' else 'guest') + ':c03302d-p' + str(revision)
    subprocess.run(['docker','build','--build-arg','MONKEYCODE_RUNTIME_PATCH='+str(revision),'-f','Dockerfile.'+kind,'-t',tag,'.'],cwd=root,check=True)
if not args.no_prepare:
    subprocess.run([sys.executable,str(root / 'prepare_local.py')],check=True)
