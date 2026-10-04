"""Prepare a private, local Git HTTP fixture and scoped credential records.

Only the named isolated PoC database is used. Git identities are SQL fixtures;
this does not claim validation of an external Git platform's PAT/OAuth flow.
"""
import json
import os
import pathlib
import secrets
import subprocess
import uuid

state = pathlib.Path(__file__).resolve().parent / '.state'
path = state / 'web-git.json'
if not path.exists():
    path.write_text(json.dumps({
        'owner_identity': str(uuid.uuid4()), 'member_identity': str(uuid.uuid4()),
        'owner_token': secrets.token_urlsafe(32), 'member_token': secrets.token_urlsafe(32),
        'username': 'runtime-git-owner', 'receipt': 'WEB_GIT_' + uuid.uuid4().hex,
        'url': 'http://host.docker.internal:47593/fixture.git',
    }, indent=2), encoding='utf-8')
fixture = json.loads(path.read_text(encoding='utf-8'))
root = state / 'git-http'
working = root / 'working'
repos = root / 'repos'
working.mkdir(parents=True, exist_ok=True)
repos.mkdir(parents=True, exist_ok=True)


def git(*args):
    subprocess.run(['git', '-c', 'core.autocrlf=false', *args], check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30,
                   creationflags=subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0)


if not (working / '.git').exists():
    git('init', '--initial-branch=main', str(working))
    (working / '中文验收.txt').write_text(fixture['receipt']+'\n', encoding='utf-8', newline='\n')
    (working / 'README.md').write_text('# Local remote Git acceptance fixture\n', encoding='utf-8', newline='\n')
    git('-C', str(working), 'add', '--', '中文验收.txt', 'README.md')
    git('-C', str(working), '-c', 'user.name=Local acceptance', '-c', 'user.email=runtime-git@example.invalid',
        'commit', '-m', 'Initialize isolated Git fixture')
if not (repos / 'fixture.git').exists():
    git('clone', '--bare', '--', str(working), str(repos / 'fixture.git'))
fixture['commit'] = subprocess.check_output(['git', '-C', str(working), 'rev-parse', 'HEAD'], text=True).strip()
if (working / '中文验收.txt').read_text(encoding='utf-8') != fixture['receipt']+'\n':
    raise SystemExit('Existing Git fixture content changed; no overwrite is performed.')


def quote(value):
    return "'" + str(value).replace("'", "''") + "'"


for label, email in [('owner','runtime-admin@example.invalid'), ('member','runtime-member@example.invalid')]:
    identity_id = str(uuid.UUID(fixture[label+'_identity']))
    # Identity and token are fixtures, not secrets supplied by a third party.
    sql = """
    BEGIN;
    INSERT INTO git_identities(id,user_id,platform,base_url,access_token,username,email,remark)
    SELECT {identity},id,'gitlab','http://127.0.0.1:47593',{token},{username},'runtime-git@example.invalid',
      'Isolated local Git credential fixture' FROM users WHERE email={email} AND role='subaccount'
    ON CONFLICT(id) DO NOTHING;
    SELECT count(*) FROM git_identities g JOIN users u ON u.id=g.user_id
      WHERE g.id={identity} AND u.email={email} AND g.access_token={token} AND g.username={username};
    COMMIT;
    """.format(identity=quote(identity_id), token=quote(fixture[label+'_token']),
               username=quote(fixture['username']), email=quote(email))
    result = subprocess.run(['docker','exec','-i','jingjia-runtime-tests-20261002','psql','-U','postgres',
                             '-d','monkeycode_web_poc','-tA','-v','ON_ERROR_STOP=1'], input=sql,
                            text=True, capture_output=True, timeout=30)
    if result.returncode or '1' not in result.stdout.splitlines():
        raise SystemExit('Scoped Git fixture admission failed; details remain private.')
path.write_text(json.dumps(fixture,indent=2),encoding='utf-8')
print('Prepared the isolated smart HTTP Git fixture and two owned credential records.')
