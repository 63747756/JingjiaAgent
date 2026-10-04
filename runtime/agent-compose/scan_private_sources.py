"""Check nonignored source for known local fixture secrets; never print values."""
import json
import pathlib
import subprocess

root=pathlib.Path(__file__).resolve().parent
workspace=root.parent.parent
state=root/'.state'
keys={'password','api_key','token','callback_token','sync_token','access_key','secret_key',
      'owner_token','member_token','access_token','client_secret'}
secrets=set()
def collect(obj):
    if isinstance(obj,dict):
        for key,value in obj.items():
            if key.lower() in keys and isinstance(value,str) and len(value)>=8:
                secrets.add(value.encode())
            collect(value)
    elif isinstance(obj,list):
        for value in obj: collect(value)
for file in state.rglob('*.json'):
    if file.stat().st_size>1024*1024: continue
    try: collect(json.loads(file.read_text()))
    except (ValueError,UnicodeError): pass
for pattern in ('*.token','*.key'):
    for file in state.rglob(pattern):
        value=file.read_bytes().strip()
        if 8<=len(value)<=65536: secrets.add(value)
paths=subprocess.check_output(['git','ls-files','-m','-o','--exclude-standard','-z'],cwd=workspace).decode().split('\0')
hits=[]; whitespace=[]; preexisting_whitespace=[]; scanned=0
for name in sorted(set(paths)):
    file=workspace/name
    if not name or not file.is_file() or name.startswith('.idea/'): continue
    content=file.read_bytes(); scanned+=1
    if any(secret in content for secret in secrets): hits.append(name)
    if b'\0' not in content:
        bad={line for line in content.splitlines() if line.endswith((b' ',b'\t'))}
        if bad:
            baseline=subprocess.run(['git','show','HEAD:'+name],cwd=workspace,capture_output=True)
            original=set(baseline.stdout.splitlines()) if baseline.returncode==0 else set()
            if bad-original: whitespace.append(name)
            else: preexisting_whitespace.append(name)
report={'scanned_nonignored_files':scanned,'known_private_values':len(secrets),'secret_hit_paths':hits,
        'new_trailing_whitespace_paths':whitespace,'preexisting_whitespace_paths':preexisting_whitespace}
(state/'installer-private-scan-report.json').write_text(json.dumps(report,indent=2))
print(json.dumps(report,indent=2))
if hits or whitespace: raise SystemExit(1)
