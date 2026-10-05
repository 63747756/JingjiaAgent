"""Archive a fresh JingjiaAgent deployment, excluding secrets and data volumes."""
import hashlib
import json
import pathlib
import shutil
import subprocess
from build_metadata import ROOT, image_tag, load_lock, revision, validate_image

lock = load_lock()
directory = ROOT / '.state/release-bundle'
directory.mkdir(parents=True, exist_ok=True)
records = {}
proxy = json.loads((ROOT/'installer-proxy.lock.json').read_text())
for name, tag in [(name,image_tag(name,lock)) for name in ('daemon','guest','backend','frontend')] + [('postgres','postgres:16-alpine'),('redis','redis:7-alpine'),('storage','minio/minio:latest'),('nginx','nginx:alpine'),('clickhouse','clickhouse/clickhouse-server:25.8-alpine'),('installer_proxy',proxy['image'])]:
    item = json.loads(subprocess.check_output(['docker','image','inspect',tag]))[0]
    if item['Os'] != 'linux':raise SystemExit('Release requires Linux images')
    if name in ('daemon','guest','backend','frontend'):validate_image(name,item,lock)
    records[name] = {'tag':tag,'id':item['Id'],'digests':item.get('RepoDigests',[]),'platform':item['Os']+'/'+item['Architecture'],'labels':item['Config'].get('Labels') or {}}
if len({item['platform'] for item in records.values()}) != 1:raise SystemExit('Release architectures differ')
owned = [records[name]['labels'] for name in ('daemon','guest','backend','frontend')]
if len({item['org.opencontainers.image.revision'] for item in owned}) != 1 or len({item['jingjiaagent.source.tree.sha256'] for item in owned}) != 1:
    raise SystemExit('Owned release components were built from different source identities')
archive = directory/'images.tar'
subprocess.run(['docker','image','save','-o',str(archive),*[record['tag'] for record in records.values()]],check=True)
with archive.open('rb') as stream:checksum=hashlib.file_digest(stream,'sha256').hexdigest()
subprocess.run(['docker','image','load','-i',str(archive)],check=True,stdout=subprocess.DEVNULL)
for item in records.values():
    loaded=json.loads(subprocess.check_output(['docker','image','inspect',item['tag']]))[0]
    if loaded['Id'] != item['id']:raise SystemExit('Reloaded image identity differs')
manifest = {'schema':1,'product':'jingjiaagent','fork_commit':owned[0]['org.opencontainers.image.revision'],'source_tree_sha256':owned[0]['jingjiaagent.source.tree.sha256'],'upstream_commit':lock['commit'],'component_revisions':{name:revision(name,lock) for name in ('daemon','guest','backend','frontend')},'images':records,'archive':'images.tar','archive_sha256':checksum,'archive_bytes':archive.stat().st_size,'reload_same_image_ids':True,'data_volumes_and_credentials_excluded':True}
(directory/'manifest.json').write_text(json.dumps(manifest,indent=2),encoding='utf-8')
for name in ['source.lock.json','build_metadata.py','build_install_bundle.py','install_web.py','prepare_linux_web.py','linux_web_common.py','linux_web_security.py','start_linux_web.py','seed_linux_web.py','seed_web.py','web_log.py','compose.web.yaml','installer-proxy.lock.json','README.md']:
    shutil.copyfile(ROOT/name,directory/name)
print('Fresh JingjiaAgent images archived and reloaded; no credentials or volumes included.')
