"""Prepare an isolated local JingjiaAgent database, Redis and original login."""
import json
import pathlib
import secrets
import shutil
import subprocess
from linux_web_security import ensure_ad_secret_key

root = pathlib.Path(__file__).resolve().parent
state = root / '.state'
web = state / 'web'
web.mkdir(parents=True, exist_ok=True)
ad_key = ensure_ad_secret_key(state)
database_container = 'jingjiaagent-postgres-1'
exists = subprocess.check_output(['docker','exec',database_container,'psql','-U','postgres','-tAc',
    "SELECT 1 FROM pg_database WHERE datname='jingjiaagent'"]).strip()
if not exists:
    subprocess.run(['docker','exec',database_container,'psql','-U','postgres','-c','CREATE DATABASE jingjiaagent'],check=True)
redis_container = 'jingjiaagent-runtime-web-redis'
present = subprocess.run(['docker','container','inspect',redis_container],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
if present.returncode:
    subprocess.run(['docker','run','-d','--name',redis_container,'--label','jingjiaagent.runtime.poc=1',
        '-p','127.0.0.1:47579:6379','redis:7-alpine','redis-server','--appendonly','yes'],check=True)
else:
    subprocess.run(['docker','start',redis_container],check=True,stdout=subprocess.DEVNULL)
account_file = state / 'web-account.json'
if not account_file.exists() or len(json.loads(account_file.read_text())['password']) >= 32:
    account_file.write_text(json.dumps({'email':'runtime-admin@example.invalid','password':secrets.token_urlsafe(18)+'_P1!'},indent=2),encoding='utf-8')
    account_file.chmod(0o600)
account = json.loads(account_file.read_text())
images = json.loads((state / 'images.json').read_text())
node_file = state / 'web-node.json'
node_registration = json.loads(node_file.read_text()) if node_file.exists() else {'id':'poc-node'}
node_id = node_registration['id']
cfg = {
    'debug':False,
    'server':{'addr':'127.0.0.1:47424','base_url':'http://127.0.0.1:47424'},
    'database':{'master':'postgres://postgres:runtime-test-only@127.0.0.1:44458/jingjiaagent?sslmode=disable'},
    'redis':{'host':'127.0.0.1','port':47579},
    'ad':{'secret_key_file':str(ad_key)},
    'root_path':str(web / 'data'),
    'security':{'captcha_enabled':False},
    'logger':{'level':'info'},
    'static_files':{'enabled':False},
    'init_team':{'email':account['email'],'password':account['password'],'name':'Remote Runtime PoC','image':images['guest'],'extension_package_dir':str(web / 'extensions')},
    'taskflow':{'callback_token':(state / 'daemon.token').read_text().strip()},
    'runtime':{'backend':'agent_compose','experimental':True,'payload_key_file':str(state / 'payload.key'),
        'nodes':[{'id':node_id,'url':'http://127.0.0.1:47410','token_file':str(state / 'daemon.token'),'guest_image':images['guest']}],
        'poll_interval':'250ms','preview':{'base_url':'http://localhost:47421','listen':'127.0.0.1:47421','trusted_proxies':['127.0.0.1/32']}},
    'llm_proxy':{'base_url':'http://host.docker.internal:47420'},
    'loki':{'addr':'http://127.0.0.1:47999'},
    'vm_idle':{'sleep_seconds':900,'recycle_seconds':259200},
}
for key in ('owner_id','team_id'):
    if node_registration.get(key):
        cfg['runtime']['nodes'][0][key] = node_registration[key]
storage_config = state / 'web-storage-config.json'
if storage_config.exists():
    cfg['object_storage'] = json.loads(storage_config.read_text())
mcp_config = state / 'web-mcp-config.json'
if mcp_config.exists():
    cfg['mcp_hub'] = json.loads(mcp_config.read_text())
    cfg['runtime']['mcp_url'] = 'http://host.docker.internal:47420/mcp'
cfg_dir = web / 'config' / 'server'
installer_config = state / 'web-installer-config.json'
if installer_config.exists():
    installer = json.loads(installer_config.read_text())
    cfg['runtime']['nodes'].extend(installer['nodes'])
    for key in ('installer_manifest_file','installer_base_url'):
        cfg['runtime'][key] = installer[key]
cfg_dir.mkdir(parents=True,exist_ok=True)
cfg_path = cfg_dir / 'config.yaml'
cfg_path.write_text(json.dumps(cfg,indent=2),encoding='utf-8')
cfg_path.chmod(0o600)
shutil.copytree(root.parent.parent / 'backend' / 'migration',web / 'migration',dirs_exist_ok=True)
print('Prepared isolated Web PoC. Test credentials are in ignored .state/web-account.json.')
