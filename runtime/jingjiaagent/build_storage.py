"""Build withdrawn MinIO community image from pinned official source."""
import json
from pathlib import Path
import subprocess

ROOT=Path(__file__).resolve().parent
lock=json.loads((ROOT/'minio-source.lock.json').read_text())
if lock['repository']!='https://github.com/minio/minio' or len(lock['commit'])!=40:
    raise SystemExit('Invalid storage source lock')
subprocess.run(['docker','build','--build-arg','MINIO_COMMIT='+lock['commit'],'-f','Dockerfile.storage','-t',lock['image'],'.'],cwd=ROOT,check=True)
record=json.loads(subprocess.check_output(['docker','image','inspect',lock['image']]))[0]
labels=record['Config'].get('Labels') or {}
if labels.get('org.opencontainers.image.revision')!=lock['commit'] or labels.get('org.opencontainers.image.licenses')!=lock['license']:
    raise SystemExit('Storage source identity differs from the lock')
with (ROOT/'.build/storage-image.json').open('w',encoding='utf-8') as stream:
    json.dump({'source':lock,'image_id':record['Id'],'platform':record['Os']+'/'+record['Architecture']},stream,indent=2)
print('Built official-source community storage image; ID and source recorded in ignored build output.')
