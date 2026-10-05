"""Create only a foreign team/group fixture in the named local test database.

No users, credentials or permissions are created. These rows supply an existing
foreign group for negative original member API tests, not team-creation proof.
"""
import json
import pathlib
import subprocess
import uuid

from linux_web_common import state
if not (state / 'web-member-account.json').exists():
    raise SystemExit('Create the dedicated member fixture in the original Web first.')
record_file = state / 'web-member-scope.json'
if record_file.exists():
    record = json.loads(record_file.read_text())
else:
    record = {'team':str(uuid.uuid4()),'group':str(uuid.uuid4())}
    record_file.write_text(json.dumps(record,indent=2),encoding='utf-8')
team_id, group_id = [str(uuid.UUID(record[field])) for field in ('team','group')]
query = """BEGIN;
INSERT INTO teams(id,name,member_limit,task_concurrency_limit,task_vm_sleep_enabled,task_vm_sleep_seconds,task_vm_recycle_enabled,task_vm_recycle_seconds,created_at,updated_at)
VALUES ('%s','Isolated foreign member-scope fixture',5,3,true,0,true,0,now(),now()) ON CONFLICT(id) DO NOTHING;
INSERT INTO team_groups(id,team_id,name,created_at,updated_at)
VALUES ('%s','%s','Isolated foreign member-scope group',now(),now()) ON CONFLICT(id) DO NOTHING;
COMMIT;""" % (team_id,group_id,team_id)
subprocess.run(['docker','exec','-i','jingjiaagent-postgres-1','psql','-v','ON_ERROR_STOP=1',
                '-U','postgres','-d','jingjiaagent'],input=query,text=True,check=True,stdout=subprocess.DEVNULL)
check = subprocess.check_output(['docker','exec','jingjiaagent-postgres-1','psql','-U','postgres','-d','jingjiaagent',
    '-tAc',"SELECT count(*) FROM team_groups g JOIN teams t ON g.team_id=t.id WHERE g.id='"+group_id+"' AND t.id='"+team_id+"' AND t.name='Isolated foreign member-scope fixture' AND g.name='Isolated foreign member-scope group'"],text=True).strip()
if check != '1':
    raise SystemExit('Saved foreign-group fixture conflicts with existing data.')
print('Prepared isolated foreign team/group rows for negative scope checks; no account was created.')
