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
from build_metadata import image_tag, labels, revision, source_metadata

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
metadata = source_metadata()
node_version = image_tag('daemon', lock)
shutil.copyfile(root.parent.parent / 'backend/pkg/runtimeadapter/guest/interaction.py', build / 'jingjiaagent-interaction.py')
subprocess.run(['docker','run','--rm','--env','CGO_ENABLED=0',
    '--mount','type=bind,source='+str(source)+',target=/src',
    '--mount','type=volume,source=jingjiaagent-go-modules,target=/go/pkg/mod',
    '--mount','type=volume,source=jingjiaagent-go-build-cache,target=/root/.cache/go-build',
    '--workdir','/src',args.go_image,'go','build','-trimpath',
    '-ldflags=-X github.com/chaitin/agent-compose/pkg/agentcompose/proxy.jingjiaAgentNodeVersion='+node_version,
    '-o','/src/agent-compose-linux','./cmd/agent-compose'],check=True)
for kind in ['daemon','guest']:
    tag = image_tag(kind, lock)
    options = [option for key,value in labels(kind,lock,metadata).items() for option in ('--label',key+'='+value)]
    subprocess.run(['docker','build',*options,'--build-arg','JINGJIAAGENT_RUNTIME_PATCH='+str(revision(kind, lock)),'-f','Dockerfile.'+kind,'-t',tag,'.'],cwd=root,check=True)
if not args.no_prepare:
    subprocess.run([sys.executable,str(root / 'prepare_local.py')],check=True)
