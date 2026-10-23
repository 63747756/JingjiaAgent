"""Obtain checksum-pinned engine materials on the connected release builder."""
import hashlib
import json
from pathlib import Path
import subprocess
import urllib.request

ROOT=Path(__file__).resolve().parent


def prepare(destination):
    lock=json.loads((ROOT/'engine.lock.json').read_text())
    destination.mkdir(parents=True,exist_ok=True)
    def valid(path,expected):
        if not path.is_file():return False
        with path.open('rb') as stream:return hashlib.file_digest(stream,'sha256').hexdigest()==expected
    archive=destination/'docker.tgz';compose=destination/'docker-compose'
    if not valid(archive,lock['docker_sha256']):
        with urllib.request.urlopen(lock['docker_url'],timeout=120) as response,archive.open('wb') as target:
            while chunk:=response.read(1<<20):target.write(chunk)
        if not valid(archive,lock['docker_sha256']):raise SystemExit('Docker archive checksum mismatch')
    if not valid(compose,lock['compose_sha256']):
        subprocess.run(['docker','pull',lock['compose_source_image']],check=True)
        container=subprocess.check_output(['docker','create','--platform','linux/amd64','--entrypoint','true',lock['compose_source_image']],text=True).strip()
        try:subprocess.run(['docker','cp',container+':'+lock['compose_source_path'],str(compose)],check=True)
        finally:subprocess.run(['docker','rm',container],check=True,stdout=subprocess.DEVNULL)
        if not valid(compose,lock['compose_sha256']):raise SystemExit('Compose binary checksum mismatch')
    return archive,compose


if __name__=='__main__':
    import argparse
    parser=argparse.ArgumentParser();parser.add_argument('--output',type=Path,required=True)
    prepare(parser.parse_args().output)
