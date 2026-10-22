"""Archive a fresh JingjiaAgent deployment, excluding secrets and data volumes."""
import argparse
import hashlib
import json
import pathlib
import shutil
import subprocess
from build_metadata import ROOT, image_tag, load_lock, revision, validate_image, validate_release_sources
from package_work_skills import package as package_skills

parser = argparse.ArgumentParser(description='Export the locked offline images and public deployment materials.')
parser.add_argument('--output', type=pathlib.Path, help='New empty output directory; defaults to .state/release-bundle')
args = parser.parse_args()
lock = load_lock()
directory = args.output if args.output is not None else ROOT / '.state/release-bundle'
if directory.is_symlink() or (args.output is not None and directory.exists() and (not directory.is_dir() or any(directory.iterdir()))):
    raise SystemExit('Explicit bundle output must be a plain empty directory; existing bundles are preserved')
directory = directory.resolve()
directory.mkdir(parents=True, exist_ok=True)
records = {}
proxy = json.loads((ROOT/'installer-proxy.lock.json').read_text())
components = ('daemon','guest','backend','frontend','converter')
for name, tag in [(name,image_tag(name,lock)) for name in components] + [('postgres','postgres:16-alpine'),('redis','redis:7-alpine'),('storage','minio/minio:latest'),('nginx','nginx:alpine'),('clickhouse','clickhouse/clickhouse-server:25.8-alpine'),('installer_proxy',proxy['image'])]:
    item = json.loads(subprocess.check_output(['docker','image','inspect',tag]))[0]
    if item['Os'] != 'linux':raise SystemExit('Release requires Linux images')
    if name in components:validate_image(name,item,lock)
    records[name] = {'tag':tag,'id':item['Id'],'digests':item.get('RepoDigests',[]),'platform':item['Os']+'/'+item['Architecture'],'labels':item['Config'].get('Labels') or {}}
if len({item['platform'] for item in records.values()}) != 1:raise SystemExit('Release architectures differ')
sources = {name: {'fork_commit': records[name]['labels']['org.opencontainers.image.revision'],
                 'source_tree_sha256': records[name]['labels']['jingjiaagent.source.tree.sha256']}
           for name in components}
identity = {'schema': 2, **sources['backend'], 'component_sources': sources, 'images': records}
validate_release_sources(identity)
archive = directory/'images.tar'
subprocess.run(['docker','image','save','-o',str(archive),*[record['tag'] for record in records.values()]],check=True)
with archive.open('rb') as stream:checksum=hashlib.file_digest(stream,'sha256').hexdigest()
subprocess.run(['docker','image','load','-i',str(archive)],check=True,stdout=subprocess.DEVNULL)
for item in records.values():
    loaded=json.loads(subprocess.check_output(['docker','image','inspect',item['tag']]))[0]
    if loaded['Id'] != item['id']:raise SystemExit('Reloaded image identity differs')
skills = package_skills(directory/'work-skills')
skill_manifest_sha256 = hashlib.sha256((directory/'work-skills/manifest.json').read_bytes()).hexdigest()
manifest = {**identity,'product':'jingjiaagent','upstream_commit':lock['commit'],'component_revisions':{name:revision(name,lock) for name in components},'archive':'images.tar','archive_sha256':checksum,'archive_bytes':archive.stat().st_size,'reload_same_image_ids':True,'data_volumes_and_credentials_excluded':True,'work_skills':skills,'work_skills_manifest_sha256':skill_manifest_sha256}
(directory/'manifest.json').write_text(json.dumps(manifest,indent=2),encoding='utf-8')
for name in ['source.lock.json','build_metadata.py','build_install_bundle.py','install_web.py','prepare_linux_web.py','linux_web_common.py','linux_web_security.py','runtime_mount_mapping.py','start_linux_web.py','seed_linux_web.py','seed_web.py','web_log.py','compose.web.yaml','compose.work-agent.yaml','installer-proxy.lock.json','minio-source.lock.json','LICENSES.md']:
    shutil.copyfile(ROOT/name,directory/name)
shutil.copytree(ROOT/'office/licenses',directory/'licenses',dirs_exist_ok=True)
shutil.copyfile(ROOT/'README.release.md',directory/'README.md')
shutil.copyfile(pathlib.Path(__file__).resolve().parents[2]/'docs/ad/README.md',directory/'AD.md')
print('Fresh JingjiaAgent images archived and reloaded; no credentials or volumes included.')
