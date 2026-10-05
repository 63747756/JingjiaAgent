"""Start only the isolated Linux acceptance compose project."""
import pathlib
import os
import re
import subprocess
import urllib.request
from linux_web_security import check_existing_networks, require_security_config

root=pathlib.Path(__file__).resolve().parent
state=pathlib.Path(os.environ.get('JINGJIAAGENT_RUNTIME_WEB_STATE_DIRECTORY',str(root/'.state/linux-web'))).resolve()
project=os.environ.get('JINGJIAAGENT_RUNTIME_WEB_PROJECT','jingjiaagent')
if not state.is_relative_to((root/'.state').resolve()) or not re.fullmatch(r'[a-z0-9][a-z0-9_-]{0,62}',project):
    raise SystemExit('Invalid private state directory or Compose project name.')
require_security_config(state)
check_existing_networks(project)
command=['docker','compose','-p',project,'--env-file',str(state/'compose.env'),'-f',str(root/'compose.web.yaml')]
subprocess.run(command+['up','-d','--wait','--wait-timeout','120','postgres','redis','storage','runtime','clickhouse'],check=True)
client=urllib.request.build_opener(urllib.request.ProxyHandler({}))
# Compose checks MinIO readiness inside its isolated network. Raw storage and
# its console must not be published to the host just to perform this check.
subprocess.run(command+['up','-d','--wait','--wait-timeout','120','backend'],check=True)
# A stopped backend receives a new network namespace even when its container
# ID is unchanged. The Web proxy shares that namespace and must be recreated.
subprocess.run(command+['up','-d','--force-recreate','--wait','--wait-timeout','120','web'],check=True)
# Bind-mounted config changes do not trigger Compose recreation. Reload both
# proxies so updated WebSocket forwarding is used by the running processes.
for service in ('runtime-proxy','web'):
    subprocess.run(command+['exec','-T',service,'nginx','-t'],check=True)
    subprocess.run(command+['exec','-T',service,'nginx','-s','reload'],check=True)
with client.open('http://127.0.0.1:47424/api/v1/users/oidc/default-team',timeout=5) as response:
    if response.status!=200:raise SystemExit('Independent Web API did not become ready.')
print('Independent Linux Web stack is ready at http://127.0.0.1:47424.')
