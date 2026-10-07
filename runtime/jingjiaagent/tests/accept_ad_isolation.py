"""Check actual Docker boundaries of the fixed, isolated AD acceptance project."""
import json
import pathlib
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[1]
STATE = ROOT / '.state/ad-acceptance'
PROJECT = 'jingjiaagent-ad-acceptance'


def inspect(name):
    return json.loads(subprocess.check_output(['docker', 'inspect', name]))[0]


def main():
    task = json.loads((STATE / 'ad-real-task.json').read_text(encoding='utf-8'))
    guest_name = 'agent-compose-' + task['sandbox'][:12]
    guest = inspect(guest_name)
    backend = inspect(PROJECT + '-backend-1')
    directory = json.loads((STATE / 'fixture/directory.json').read_text(encoding='utf-8'))
    environment = '\n'.join(guest['Config']['Env'])
    checks = {
        'guest_only_sandbox_network': set(guest['NetworkSettings']['Networks']) == {PROJECT + '_sandbox'},
        'guest_has_no_ad_credentials': directory['bind_password'] not in environment
            and not any(value.split('=', 1)[0].startswith('JINGJIAAGENT_AD_') for value in guest['Config']['Env']),
        'guest_has_no_ad_key_mount': not any('ad-secret' in mount.get('Source', '')
            or 'ad-secret' in mount.get('Destination', '') for mount in guest['Mounts']),
        'guest_resource_limits': guest['HostConfig']['Memory'] == 2 << 30 and guest['HostConfig']['NanoCpus'] == 1_000_000_000,
        'backend_only_fixture_connection': PROJECT + '_ad-test' in backend['NetworkSettings']['Networks']
            and PROJECT + '_ad-test' not in inspect(PROJECT + '-runtime-1')['NetworkSettings']['Networks'],
    }
    targets = []
    for service, port, network in [('postgres', 5432, 'business'), ('redis', 6379, 'business'),
                                  ('storage', 9000, 'business'), ('clickhouse', 9000, 'business'),
                                  ('ad-fixture', 1636, 'ad-test')]:
        data = inspect(PROJECT + '-' + service + '-1')
        address = data['NetworkSettings']['Networks'][PROJECT + '_' + network]['IPAddress']
        targets.append([service, address, port])
    probe = """import json,socket,sys
results={}
for service,address,port in json.loads(sys.argv[1]):
    try:
        connection=socket.create_connection((address,port),timeout=1)
    except OSError:results[service]=True
    else:connection.close();results[service]=False
print(json.dumps(results))
"""
    refused = json.loads(subprocess.check_output(['docker', 'exec', guest_name, 'python3', '-c', probe,
                                                  json.dumps(targets)]))
    checks.update({'guest_cannot_connect_' + service: result for service, result in refused.items()})
    result = subprocess.check_output(['docker', 'exec', PROJECT + '-backend-1', 'sh', '-c',
                                     'stat -c "%a:%s" /run/secrets/ad-secret-key'], text=True).strip()
    checks['backend_private_key_600_32'] = result == '600:32'
    (STATE / 'ad-isolation-report.json').write_text(json.dumps(checks, indent=2), encoding='utf-8')
    if not all(checks.values()):
        raise SystemExit('AD isolation acceptance failed: ' + ','.join(name for name, value in checks.items() if not value))
    print(json.dumps({'passed_checks': len(checks), 'project': PROJECT, 'credential_values_printed': False}))


if __name__ == '__main__':
    main()
