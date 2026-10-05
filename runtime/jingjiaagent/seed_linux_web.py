"""Configure private provider models and local members through existing APIs."""
import os
from linux_web_common import api, load, login, save, state

admin, user, _ = login(team=True)
model = load('model.json')
fixture = load('web-fixture.json')
_, console_user, _ = login()
console_identity = {'id': console_user['id']}
if (state/'web-console-user.json').exists() and load('web-console-user.json') != console_identity:
    raise RuntimeError('Existing console installation node ownership cannot change through seeding.')
save('web-console-user.json', console_identity)
base = model['base_url'].rstrip('/')
if base.endswith('/v1'):
    base = base[:-3]
models = api(admin, '/api/v1/teams/models')['models'] or []
for cli, protocol, address in [('codex','openai_responses',base),('claude','anthropic',base+'/anthropic')]:
    prefix = os.environ.get('JINGJIAAGENT_RUNTIME_WEB_FIXTURE_LABEL')
    remark = prefix + ' ' + cli if prefix else 'Phase 4 native ' + cli + ' acceptance'
    matches = [item for item in models if item.get('remark') == remark]
    if len(matches) > 1:
        raise RuntimeError('Conflicting isolated provider model')
    if matches:
        saved = matches[0]
        if saved['base_url'] != address or saved['interface_type'] != protocol or saved['model'] != model['model']:
            raise RuntimeError('Existing provider fixture configuration changed')
    else:
        saved = api(admin, '/api/v1/teams/models', {'provider':'DeepSeek', 'api_key':model['api_key'],
            'base_url':address,'model':model['model'],'interface_type':protocol,'temperature':.2,
            'remark':remark,'support_image':False})
    fixture[cli+'_model_id'] = saved['id']
save('web-fixture.json', fixture)
for label in ('member','outsider'):
    name = 'web-'+label+'-account.json'
    if (state/name).exists():
        login(name)
        continue
    email = os.environ.get('JINGJIAAGENT_RUNTIME_WEB_MEMBER_PREFIX','phase4')+'-'+label+'@example.invalid'
    existing = api(admin,'/api/v1/teams/users?role=user')['members'] or []
    if any(item['user']['email'] == email for item in existing):
        raise RuntimeError('Member already exists without its private credential; do not reset it')
    saved = api(admin,'/api/v1/teams/users/with-password',{'emails':[email]})
    credentials = [item for item in saved['passwords'] if item['email'] == email]
    if len(credentials) != 1:
        raise RuntimeError('Original member creation did not return one fixture credential')
    save(name, credentials[0])
    login(name)
print('Native Responses/Anthropic models and two isolated test members configured through existing Web APIs.')
