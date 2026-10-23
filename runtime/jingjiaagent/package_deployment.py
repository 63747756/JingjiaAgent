"""Build signed online/offline deployment materials. No push or secret export.

Build machine requires Python 3.11+, Go, Docker; target host only runs the binary.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import time
from build_metadata import ROOT, image_tag, load_lock, validate_image, source_metadata
from pinned_proxy import prepare_proxy


def run(*args, **kwargs):
    return subprocess.check_output(list(map(str, args)), **kwargs)


def checksum(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def archive_release(source, target, version):
    # Windows does not preserve POSIX executable bits through Path.chmod.
    # Record target permissions explicitly so an extracted package runs as-is.
    def permissions(member):
        member.uid = member.gid = 0
        member.uname = member.gname = ''
        member.mode = 0o755 if member.isdir() or Path(member.name).name in {
            'jingjiaagent', 'install.sh', 'install-online.sh', 'docker-compose'
        } else 0o644
        return member
    with tarfile.open(target, 'w:gz') as package:
        package.add(source, arcname='jingjiaagent-' + version, filter=permissions)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--version', required=True)
    parser.add_argument('--key', type=Path, required=True, help='Ed25519 private key outside output')
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--image-refs', type=Path, help='optional component -> immutable registry@sha256 references')
    parser.add_argument('--docker-archive', type=Path, help='verified official Linux amd64 static Docker tgz')
    parser.add_argument('--compose-binary', type=Path, help='verified Linux amd64 Docker Compose binary')
    parser.add_argument('--existing-engine-only',action='store_true',help='omit bundled engine, target must already have Docker')
    parser.add_argument('--archive', action='store_true', help='also create the distributable .tar.gz')
    parser.add_argument('--resume',action='store_true',help='reuse checked archives from an unfinished build')
    args = parser.parse_args()
    import re
    if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}', args.version):
        parser.error('Invalid release version')
    out = args.output.resolve(); key = args.key.resolve(); repo = ROOT.parent.parent
    if key.is_relative_to(out):
        parser.error('Signing key must remain outside the release directory')
    if out.exists() and any(out.iterdir()) and (not args.resume or (out/'manifest.json').exists()):
        parser.error('Output must be empty; releases are immutable')
    if bool(args.docker_archive) != bool(args.compose_binary):
        parser.error('Supply both Docker and Compose, or neither')
    if not args.docker_archive and not args.existing_engine_only:
        from prepare_deployment_engine import prepare
        args.docker_archive,args.compose_binary=prepare(ROOT/'.state/release-engine')
    out.mkdir(parents=True, exist_ok=True)
    lock = load_lock(); refs = json.loads(args.image_refs.read_text()) if args.image_refs else {}
    manifest = {'schema': 1, 'product': 'jingjiaagent', 'version': args.version,'sequence':int(time.time()),
                'architecture': 'amd64', 'database_generation': 1, 'source': source_metadata(),
                'images': {}, 'files': {}}
    def add(path, online=True):
        manifest['files'][path.relative_to(out).as_posix()] = {'sha256': checksum(path), 'size': path.stat().st_size, 'online': online}
    public = run('go', 'run', './cmd/sign', '--key', key, cwd=repo/'installer').decode().strip()
    env = dict(os.environ, GOOS='linux', GOARCH='amd64', CGO_ENABLED='0')
    subprocess.run(['go', 'build', '-trimpath', '-ldflags', f'-s -w -X main.releasePublicKey={public} -X main.installerVersion={args.version}', '-o', str(out/'jingjiaagent'), '.'], cwd=repo/'installer', env=env, check=True)
    (out/'jingjiaagent').chmod(0o755); add(out/'jingjiaagent')
    (out/'install-online.sh').write_text((repo/'installer/install-online.sh').read_text().replace('__BINARY_SHA256__',checksum(out/'jingjiaagent')),encoding='utf-8',newline='\n')
    (out/'install-online.sh').chmod(0o755);add(out/'install-online.sh')
    proxy_lock = json.loads((ROOT/'installer-proxy.lock.json').read_text())
    prepare_proxy(proxy_lock)
    # Export by a stable local tag because docker save/load does not retain
    # RepoDigests. The tag is assigned only from the verified pinned reference.
    proxy_tag = 'jingjiaagent-proxy:' + proxy_lock['repo_digests'][0].split('@sha256:')[1]
    subprocess.run(['docker', 'tag', proxy_lock['repo_digests'][0], proxy_tag], check=True)
    infrastructure = {'postgres': 'postgres:16-alpine', 'redis': 'redis:7-alpine', 'storage': 'minio/minio:latest',
                      'nginx': proxy_tag, 'clickhouse': 'clickhouse/clickhouse-server:25.8-alpine'}
    for name in ('backend', 'frontend', 'daemon', 'guest', 'converter', *infrastructure):
        tag = infrastructure.get(name) or image_tag(name, lock)
        image = json.loads(run('docker', 'image', 'inspect', tag))[0]
        if image['Os'] != 'linux' or image['Architecture'] != 'amd64':
            raise SystemExit('All images must be Linux amd64: '+name)
        if name not in infrastructure:
            validate_image(name, image, lock)
        ref = refs.get(name, '')
        if ref:
            if not re.fullmatch(r'[^\s]+@sha256:[a-f0-9]{64}', ref):
                raise SystemExit('Registry references must be immutable: '+name)
            if json.loads(run('docker', 'image', 'inspect', ref))[0]['Id'] != image['Id']:
                raise SystemExit('Registry digest differs from locally validated image: '+name)
        archive = out/'images'/f'{name}.tar'; archive.parent.mkdir(exist_ok=True)
        marker=archive.with_suffix('.identity.json')
        previous=json.loads(marker.read_text()) if marker.exists() else {}
        if not (args.resume and archive.exists() and previous.get('id')==image['Id'] and previous.get('sha256')==checksum(archive)):
            subprocess.run(['docker', 'image', 'save', '-o', str(archive), tag], check=True)
            marker.write_text(json.dumps({'id':image['Id'],'sha256':checksum(archive)}))
        with tarfile.open(archive) as package:
            entries = json.load(package.extractfile('manifest.json'))
            if len(entries) != 1:
                raise SystemExit('Expected one image per archive')
            config = package.extractfile(entries[0]['Config']).read()
        manifest['images'][name] = {'id': image['Id'], 'config_id': 'sha256:'+hashlib.sha256(config).hexdigest(),
            'local_tag': tag, 'ref': ref, 'archive': f'images/{name}.tar', 'labels': image['Config'].get('Labels', {})}
        add(archive, online=not bool(ref))
    # Runtime bindings download this exact checksum-checked package from the
    # platform; Internet access is not required on newly bound machines.
    subprocess.run([sys.executable, str(ROOT/'build_install_bundle.py')], check=True)
    shutil.copytree(ROOT/'.state/installation-bundle', out/'node',dirs_exist_ok=args.resume)
    for p in (out/'node').iterdir(): add(p)
    if args.docker_archive:
        (out/'engine').mkdir(exist_ok=True)
        shutil.copyfile(args.docker_archive, out/'engine/docker.tgz'); add(out/'engine/docker.tgz')
        shutil.copyfile(args.compose_binary, out/'engine/docker-compose'); add(out/'engine/docker-compose')
    shutil.copyfile(repo/'installer/install.sh', out/'install.sh'); (out/'install.sh').chmod(0o755); add(out/'install.sh')
    shutil.copyfile(repo/'installer/README.md', out/'README.md'); add(out/'README.md')
    (out/'manifest.json').write_text(json.dumps(manifest, indent=2, ensure_ascii=False)+'\n', encoding='utf-8')
    subprocess.run(['go', 'run', './cmd/sign', '--key', str(key), '--manifest', str(out/'manifest.json')], cwd=repo/'installer', check=True)
    (out/'manifest.json.sig').rename(out/'manifest.sig')
    for marker in (out/'images').glob('*.identity.json'): marker.unlink()
    (out/'SHA256SUMS').write_text(''.join(f'{checksum(p)}  {p.name}\n' for p in (out/'jingjiaagent', out/'manifest.json', out/'manifest.sig', out/'install.sh')), encoding='ascii')
    if args.archive:
        target=Path(str(out)+'.tar.gz')
        if target.exists(): raise SystemExit('Archive already exists')
        archive_release(out, target, args.version)
    print('Signed release built:', out)
    print('No images were pushed. Publish this directory at an HTTPS release URL for online installation.')


if __name__ == '__main__': main()
