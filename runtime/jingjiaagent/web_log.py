"""Read isolated backend diagnostics with configured secrets redacted."""
import json
import pathlib
import re
import sys

from linux_web_common import state
text = (state / 'web' / 'backend.log').read_text(encoding='utf-8',errors='replace')
for name,field in [('model.json','api_key'),('web-account.json','password'),('web-member-account.json','password')]:
    path = state / name
    if path.exists(): text = text.replace(json.loads(path.read_text())[field],'[redacted]')
for name, fields in [('web-mcp.json', ('token', 'sync_token')), ('web-storage.json', ('access_key', 'secret_key')),
                     ('web-git.json', ('owner_token','member_token'))]:
    path = state / name
    if path.exists():
        saved = json.loads(path.read_text())
        for field in fields:
            value = saved.get(field)
            if value:
                text = text.replace(value, '[redacted]')
text = text.replace((state / 'daemon.token').read_text().strip(),'[redacted]')
text = re.sub(r'invalid header:[A-Za-z0-9+/=_-]+','invalid header:[redacted upstream headers]',text)
print('\n'.join(text.splitlines()[-int(sys.argv[1] if len(sys.argv)>1 else 40):]))
