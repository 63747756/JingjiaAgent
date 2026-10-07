"""Prepare an independent Linux Web stack; preserve existing private state."""
import datetime
import ipaddress
import json
import os
import pathlib
import secrets
import shutil
import subprocess
import uuid
from linux_web_security import (AD_SECRET_CONTAINER_PATH, SECURITY_VERSION, prepare_ad_secret_key,
                                redis_password, validate_runtime_image)
from build_metadata import image_tag, revision
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.x509.oid import NameOID, ExtendedKeyUsageOID

def main(directory=None):
    root=pathlib.Path(directory) if directory is not None else pathlib.Path(__file__).resolve().parent
    state=pathlib.Path(os.environ.get('JINGJIAAGENT_RUNTIME_WEB_STATE_DIRECTORY', str(root/'.state/linux-web'))).resolve()
    if not state.is_relative_to((root/'.state').resolve()):
        raise SystemExit('Private deployment state must remain in the ignored .state directory.')
    # This must precede every private-state write, even bundle/config preparation.
    prepare_ad_secret_key(state, os.environ.get('JINGJIAAGENT_RUNTIME_WEB_PROJECT', 'jingjiaagent'))
    def private(path,text):
        path.parent.mkdir(parents=True,exist_ok=True)
        path.write_text(text,encoding='utf-8',newline='\n');path.chmod(0o600)
    credentials=state/'credentials.json'
    if not credentials.exists():
        private(credentials,json.dumps({'postgres_password':secrets.token_urlsafe(32),'storage_user':'jingjiaagent-'+secrets.token_hex(8),'storage_password':secrets.token_urlsafe(36),'redis_password':secrets.token_urlsafe(36)},indent=2))
    keys=json.loads(credentials.read_text())
    # Fail before rewriting any legacy credentials/configuration. Migration is manual.
    redis_secret=redis_password(keys)
    private(state/'redis.password',redis_secret+'\n')
    if not keys.get('mcp_token'):
        keys['mcp_token']=secrets.token_urlsafe(36)
        private(credentials,json.dumps(keys,indent=2))
    if not keys.get('clickhouse_password'):
        keys['clickhouse_password']=secrets.token_urlsafe(36)
        private(credentials,json.dumps(keys,indent=2))
    if not (state/'daemon.token').exists():
        private(state/'daemon.token',secrets.token_urlsafe(36)+'\n')
    if not (state/'payload.key').exists():
        (state/'payload.key').write_bytes(secrets.token_bytes(32))
        (state/'payload.key').chmod(0o600)
    if len((state/'payload.key').read_bytes()) != 32:
        raise SystemExit('Existing payload encryption key must contain exactly 32 raw bytes; do not replace a key after admitting data.')
    if not (state/'web-account.json').exists():private(state/'web-account.json',json.dumps({'email':os.environ.get('JINGJIAAGENT_RUNTIME_WEB_ADMIN_EMAIL','jingjiaagent-admin@example.invalid'),'password':secrets.token_urlsafe(18)+'_P1!'},indent=2))
    if not (state/'web-node.json').exists():private(state/'web-node.json',json.dumps({'id':str(uuid.uuid4())},indent=2))
    node=json.loads((state/'web-node.json').read_text())
    account=json.loads((state/'web-account.json').read_text())
    model=json.loads((root/'.state/model.json').read_text())
    private(state/'model.json',json.dumps(model,indent=2))
    if not (state/'runtime.crt').exists():
        now=datetime.datetime.now(datetime.timezone.utc)
        ca_key=rsa.generate_private_key(public_exponent=65537,key_size=2048)
        name=x509.Name([x509.NameAttribute(NameOID.COMMON_NAME,'JingjiaAgent local acceptance CA')])
        ca=x509.CertificateBuilder().subject_name(name).issuer_name(name).public_key(ca_key.public_key()).serial_number(x509.random_serial_number()).not_valid_before(now-datetime.timedelta(minutes=5)).not_valid_after(now+datetime.timedelta(days=30)).add_extension(x509.BasicConstraints(ca=True,path_length=0),critical=True).sign(ca_key,hashes.SHA256())
        key=rsa.generate_private_key(public_exponent=65537,key_size=2048)
        cert=x509.CertificateBuilder().subject_name(x509.Name([x509.NameAttribute(NameOID.COMMON_NAME,'runtime-proxy')])).issuer_name(name).public_key(key.public_key()).serial_number(x509.random_serial_number()).not_valid_before(now-datetime.timedelta(minutes=5)).not_valid_after(now+datetime.timedelta(days=30)).add_extension(x509.BasicConstraints(ca=False,path_length=None),critical=True).add_extension(x509.SubjectAlternativeName([x509.DNSName('runtime-proxy'),x509.IPAddress(ipaddress.ip_address('127.0.0.1'))]),critical=False).add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.SERVER_AUTH]),critical=False).sign(ca_key,hashes.SHA256())
        private(state/'config/server/runtime.ca.pem',ca.public_bytes(serialization.Encoding.PEM).decode())
        private(state/'runtime.crt',cert.public_bytes(serialization.Encoding.PEM).decode())
        private(state/'runtime.key',key.private_bytes(serialization.Encoding.PEM,serialization.PrivateFormat.PKCS8,serialization.NoEncryption()).decode())
    private(state/'runtime-proxy.conf','''map $http_upgrade $connection_upgrade { default upgrade; '' close; }
    server {
     listen 7411 ssl; server_name runtime-proxy;
     ssl_certificate /run/tls/server.crt; ssl_certificate_key /run/tls/server.key;
     ssl_protocols TLSv1.2 TLSv1.3;
     client_max_body_size 64m;
     location / {
      proxy_pass http://runtime:7410; proxy_http_version 1.1; proxy_buffering off; proxy_read_timeout 600s;
      proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection $connection_upgrade;
     }
    }
    ''')
    lock=json.loads((root/'source.lock.json').read_text())
    images={}
    for name,tag in [('daemon',image_tag('daemon',lock)),('guest',image_tag('guest',lock)),('backend',image_tag('backend',lock)),('frontend',image_tag('frontend',lock)),('postgres','postgres:16-alpine'),('redis','redis:7-alpine'),('storage','minio/minio:latest'),('nginx','nginx:alpine'),('clickhouse','clickhouse/clickhouse-server:25.8-alpine')]:
        item=json.loads(subprocess.check_output(['docker','image','inspect',tag]))[0]
        validate_runtime_image(name,item,lock)
        images[name]=item['Id']
    private(state/'images.json',json.dumps(images,indent=2))
    values={'JINGJIAAGENT_RUNTIME_DAEMON_IMAGE':images['daemon'],'JINGJIAAGENT_RUNTIME_GUEST_IMAGE':images['guest'],'JINGJIAAGENT_WEB_BACKEND_IMAGE':images['backend'],'JINGJIAAGENT_WEB_POSTGRES_IMAGE':images['postgres'],'JINGJIAAGENT_WEB_REDIS_IMAGE':images['redis'],'JINGJIAAGENT_WEB_STORAGE_IMAGE':images['storage'],'JINGJIAAGENT_WEB_NGINX_IMAGE':images['nginx'],'JINGJIAAGENT_WEB_POSTGRES_PASSWORD':keys['postgres_password'],'JINGJIAAGENT_WEB_STORAGE_USER':keys['storage_user'],'JINGJIAAGENT_WEB_STORAGE_PASSWORD':keys['storage_password'],'JINGJIAAGENT_WEB_CLICKHOUSE_IMAGE':images['clickhouse'],'JINGJIAAGENT_WEB_CLICKHOUSE_PASSWORD':keys['clickhouse_password']}
    values['JINGJIAAGENT_WEB_STATE_DIRECTORY']=state.relative_to(root).as_posix()
    values['JINGJIAAGENT_WEB_FRONTEND_IMAGE']=images['frontend']
    private(state/'compose.env',''.join(key+'='+value+'\n' for key,value in values.items()))
    runtime_node={'id':node['id'],'url':'https://runtime-proxy:7411','ca_file':'/app/config/server/runtime.ca.pem','token_file':'/run/secrets/runtime_token','guest_image':images['guest']}
    for key in ('owner_id','team_id'):
        if node.get(key):runtime_node[key]=node[key]
    guest_base=os.environ.get('JINGJIAAGENT_RUNTIME_WEB_GUEST_BASE_URL','http://backend:47424').rstrip('/')
    guest_storage=os.environ.get('JINGJIAAGENT_RUNTIME_WEB_GUEST_STORAGE_URL','http://backend:47596').rstrip('/')
    cfg={'debug':False,'server':{'addr':'0.0.0.0:8888','base_url':'http://127.0.0.1:47424'},
     'database':{'master':'postgres://postgres:'+keys['postgres_password']+'@postgres:5432/jingjiaagent?sslmode=disable'},
     'redis':{'host':'redis','port':6379,'pass':redis_secret},'ad':{'secret_key_file':AD_SECRET_CONTAINER_PATH},'root_path':'/app/data','security':{'captcha_enabled':False},'logger':{'level':'info'},'static_files':{'enabled':False},
     'init_team':{'email':account['email'],'password':account['password'],'name':os.environ.get('JINGJIAAGENT_RUNTIME_WEB_TEAM_NAME','景嘉微AI助手'),'image':images['guest'],'extension_package_dir':'/app/data/extensions'},
     'taskflow':{'callback_token':(state/'daemon.token').read_text().strip()},
     'runtime':{'backend':'agent_compose','experimental':True,'payload_key_file':'/run/secrets/payload_key','nodes':[runtime_node],'poll_interval':'250ms',
     'capacity':{'enabled':True,'max_cpu_millis':int(os.environ.get('JINGJIAAGENT_RUNTIME_WEB_MAX_CPU_MILLIS','4000')),'max_memory_bytes':int(os.environ.get('JINGJIAAGENT_RUNTIME_WEB_MAX_MEMORY_BYTES',str(8<<30)))},'mcp_url':guest_base+'/mcp',
     'preview':{'base_url':'http://localhost:47425','listen':'127.0.0.1:8889','trusted_proxies':['127.0.0.1/32']}},
     'llm_proxy':{'base_url':guest_base},'mcp_hub':{'enabled':True,'url':'http://127.0.0.1:47424','token':keys['mcp_token'],'upstream_timeout':'15s'},'loki':{'addr':'http://127.0.0.1:47999'},
     'clickhouse':{'addr':'clickhouse:9000','database':'jingjiaagent','username':'jingjiaagent','password':keys['clickhouse_password'],'init_enabled':True,'max_open_conns':4,'max_idle_conns':2},
     'vm_idle':{'sleep_seconds':900,'recycle_seconds':259200},
     'object_storage':{'enabled':True,'provider':'s3','force_path_style':True,'init_bucket':True,'endpoint':'http://storage:9000','access_endpoint':'http://127.0.0.1:47424/oss','agent_access_endpoint':guest_storage,'access_key':keys['storage_user'],'access_key_secret':keys['storage_password'],'bucket':'jingjiaagent','region':'us-east-1','presign_expires':'1h','temp_prefix':'temp','avatar_prefix':'avatar','spec_prefix':'spec','repo_prefix':'repo'}}
    if os.environ.get('JINGJIAAGENT_RUNTIME_WEB_ENABLE_INSTALLER') == '1' and node.get('owner_id') and node.get('team_id'):
        manifest=state/'config/server/installation-bundle/manifest.json'
        if not manifest.is_file():raise SystemExit('Build the fixed runtime installation bundle before preparing the installer.')
        bundle=json.loads(manifest.read_text())
        if (bundle.get('product'),bundle.get('daemon_image'),bundle.get('guest_image'),
            bundle.get('daemon_revision'),bundle.get('guest_revision'),bundle.get('upstream_commit')) != (
            'jingjiaagent',images['daemon'],images['guest'],revision('daemon',lock),revision('guest',lock),lock['commit']):
            raise SystemExit('Runtime installer bundle differs from the selected images. Rebuild build_install_bundle.py before preparing the deployment.')
        def pending_node(name, owner_id, team_id, port):
            pending=state/(name+'.json')
            if not pending.exists():private(pending,json.dumps({'id':str(uuid.uuid4()),'token':secrets.token_urlsafe(36)},indent=2))
            install=json.loads(pending.read_text())
            certdir=state/'config/server'/name
            if not (certdir/'server.crt').exists():
                now=datetime.datetime.now(datetime.timezone.utc)
                key=rsa.generate_private_key(public_exponent=65537,key_size=2048)
                subject=x509.Name([x509.NameAttribute(NameOID.COMMON_NAME,'local-'+name)])
                cert=x509.CertificateBuilder().subject_name(subject).issuer_name(subject).public_key(key.public_key()).serial_number(x509.random_serial_number()).not_valid_before(now-datetime.timedelta(minutes=5)).not_valid_after(now+datetime.timedelta(days=30)).add_extension(x509.BasicConstraints(ca=True,path_length=0),critical=True).add_extension(x509.SubjectAlternativeName([x509.DNSName('host.docker.internal'),x509.IPAddress(ipaddress.ip_address('127.0.0.1'))]),critical=False).add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.SERVER_AUTH]),critical=False).sign(key,hashes.SHA256())
                private(certdir/'server.crt',cert.public_bytes(serialization.Encoding.PEM).decode())
                private(certdir/'server.key',key.private_bytes(serialization.Encoding.PEM,serialization.PrivateFormat.PKCS8,serialization.NoEncryption()).decode())
            private(certdir/'daemon.token',install['token']+'\n')
            prefix='/app/config/server/'+name+'/'
            result={'id':install['id'],'url':'https://host.docker.internal:'+str(port),'token_file':prefix+'daemon.token',
                'ca_file':prefix+'server.crt','install_cert_file':prefix+'server.crt','install_key_file':prefix+'server.key',
                'install':True,'install_listen':'0.0.0.0:'+str(port),'owner_id':owner_id,'guest_image':images['guest']}
            if team_id:result['team_id']=team_id
            return result
        cfg['runtime']['installer_manifest_file']='/app/config/server/installation-bundle/manifest.json'
        cfg['runtime']['installer_base_url']='http://host.docker.internal:47424'
        cfg['runtime']['nodes'].append(pending_node('installer-node',node['owner_id'],node['team_id'],47599))
        console_identity=state/'web-console-user.json'
        if console_identity.exists():
            console_owner=str(uuid.UUID(json.loads(console_identity.read_text())['id']))
            # Ordinary console accounts bind their own private node. This node has
            # distinct identity/TLS/token and is never added to the team's grants.
            cfg['runtime']['nodes'].append(pending_node('installer-user-node',console_owner,'',47600))
    private(state/'config/server/config.yaml',json.dumps(cfg,indent=2))
    private(state/'nginx.conf','''# Only pre-signed, read-only object requests may cross the sandbox boundary.
    # MinIO still verifies the signature, object scope and expiration.
    map $args $agent_storage_signed {
     default 0;
     ~(^|&)X-Amz-Signature=[0-9a-f]+(&|$) 1;
    }
    # Match the application's existing public-read bucket policy. Temporary
    # uploads and private resources still require a scoped signature.
    map $uri $browser_storage_public {
     default 0;
     ~^/oss/jingjiaagent/(avatar|spec|repo)/ 1;
    }
    map "$request_method:$browser_storage_public:$agent_storage_signed" $browser_storage_allowed {
     default 0;
     ~^(GET|HEAD):1:[01]$ 1;
     ~^(GET|HEAD|PUT):[01]:1$ 1;
    }
    map $http_upgrade $connection_upgrade { default upgrade; '' close; }
    server {
     listen 47424; server_name _; root /usr/share/nginx/html; client_max_body_size 64m;
     # Browser URLs carry scoped S3 signatures. The /oss prefix is outside the
     # signature; remove only that prefix, keeping the signed Host and query.
     location ^~ /oss/ {
      if ($request_method !~ ^(GET|HEAD|PUT)$) { return 405; }
      if ($browser_storage_allowed = 0) { return 403; }
      access_log off;
      proxy_pass http://storage:9000/; proxy_http_version 1.1;
      proxy_set_header Host $http_host;
      proxy_set_header Authorization ""; proxy_set_header Cookie "";
      proxy_buffering off;
     }
     location ~ ^/(api/|v1/|internal/|mcp) {
      proxy_pass http://127.0.0.1:8888; proxy_http_version 1.1; proxy_buffering off; proxy_read_timeout 600s;
      proxy_set_header Host $http_host; proxy_set_header X-Forwarded-Proto $scheme;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection $connection_upgrade;
     }
     location / { try_files $uri $uri/ /index.html; }
    }
    server {
     listen 47425; server_name _; client_max_body_size 64m;
     location / {
      proxy_pass http://127.0.0.1:8889; proxy_http_version 1.1; proxy_buffering off; proxy_read_timeout 600s;
      proxy_set_header Host $http_host; proxy_set_header X-Forwarded-Proto $scheme;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection $connection_upgrade;
     }
    }
    server {
     listen 47596; server_name _;
     # No publish on the host: this listener is reachable only in the container
     # namespace. Preserve Host and URI because both are covered by the S3 signature.
     access_log off;
     if ($request_method !~ ^(GET|HEAD)$) { return 405; }
     if ($agent_storage_signed = 0) { return 403; }
     location / {
      proxy_pass http://storage:9000; proxy_http_version 1.1;
      proxy_set_header Host $http_host;
      proxy_set_header Authorization "";
      proxy_buffering off;
     }
    }
    ''')
    private(state/'network-security.json',json.dumps({'version':SECURITY_VERSION},indent=2))
    print('Prepared Linux stack at 127.0.0.1:47424; private state retained in '+state.relative_to(root).as_posix()+'.')


if __name__ == '__main__':
    main()
