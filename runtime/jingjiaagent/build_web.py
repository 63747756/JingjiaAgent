"""Build owned Web images from the current source and the shared component lock."""
import argparse
import os
import pathlib
import shutil
import subprocess
from build_metadata import ROOT, image_tag, labels, load_lock, revision, source_metadata


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--go-image', default='golang:1.26.2-bookworm')
    args = parser.parse_args()
    repo = ROOT.parent.parent
    build = ROOT / '.build'
    build.mkdir(exist_ok=True)
    lock = load_lock()
    metadata = source_metadata()
    subprocess.run(['docker', 'run', '--rm', '--env', 'CGO_ENABLED=0',
        '--mount', f'type=bind,source={repo / "backend"},target=/src',
        '--mount', f'type=bind,source={build},target=/out',
        '--mount', 'type=volume,source=jingjiaagent-go-modules,target=/go/pkg/mod',
        '--mount', 'type=volume,source=jingjiaagent-go-build-cache,target=/root/.cache/go-build',
        '--workdir', '/src', args.go_image, 'go', 'build', '-trimpath',
        '-ldflags=-X github.com/63747756/jingjiaagent/backend/pkg/brand.Revision=' + str(revision('backend', lock)),
        '-o', '/out/jingjiaagent-server-linux', './cmd/server'], check=True)
    shutil.copytree(repo / 'backend/migration', build / 'web-migration', dirs_exist_ok=True)
    pnpm = shutil.which('pnpm.cmd' if os.name == 'nt' else 'pnpm')
    if not pnpm:
        raise SystemExit('pnpm is required for the source Web build')
    install = [pnpm, 'install', '--frozen-lockfile']
    if os.environ.get('JINGJIAAGENT_PNPM_STORE'):
        install.extend(['--store-dir', os.environ['JINGJIAAGENT_PNPM_STORE']])
    subprocess.run(install, cwd=repo / 'frontend', check=True)
    subprocess.run([pnpm, 'run', 'build:offline'], cwd=repo / 'frontend', check=True)
    destination = build / 'web-static'
    # This private build output is replaced; application data is never touched.
    if destination.exists():
        if not destination.resolve().is_relative_to(build.resolve()):
            raise SystemExit('Build output escapes private build directory')
        shutil.rmtree(destination)
    shutil.copytree(repo / 'frontend/dist', destination)
    for component, dockerfile in [('backend', 'Dockerfile.web-backend'), ('frontend', 'Dockerfile.frontend')]:
        options = [arg for key, value in labels(component, lock, metadata).items() for arg in ('--label', key + '=' + value)]
        subprocess.run(['docker', 'build', *options, '-f', dockerfile, '-t', image_tag(component, lock), '.'], cwd=ROOT, check=True)


if __name__ == '__main__':
    main()
