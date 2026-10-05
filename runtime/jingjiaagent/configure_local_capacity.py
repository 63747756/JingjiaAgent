"""Explicitly apply the local capacity policy without weakening admission checks.

Heartbeat intentionally only lowers saved limits; an operator-requested increase
uses this command, fenced against admission and bounded by a recent machine sample.
"""
import json
import pathlib
import re
from linux_web_common import sql, state

cfg=json.loads((state/'config/server/config.yaml').read_text(encoding='utf-8'))
policy=cfg['runtime']['capacity']
if cfg['runtime']['backend']!='agent_compose' or not policy.get('enabled'):
    raise SystemExit('Only an enforced agent-compose capacity policy may be applied.')
numbers={key:int(policy.get(key,0)) for key in ('max_cpu_millis','max_memory_bytes','reserve_cpu_millis','reserve_memory_bytes')}
if numbers['max_cpu_millis']<=0 or numbers['max_memory_bytes']<=0 or any(value<0 for value in numbers.values()):
    raise SystemExit('Local capacity requires positive finite limits and nonnegative reserves.')
node=cfg['runtime']['nodes'][0]['id']
if not re.fullmatch(r'[a-f0-9-]{36}',node):raise SystemExit('Invalid local node identity.')
query=f"""BEGIN;
SELECT pg_advisory_xact_lock(707326030);
DO $policy$
DECLARE pool text; sample runtime_capacity_pools%ROWTYPE;
BEGIN
 SELECT capacity_id INTO pool FROM runtime_nodes WHERE node_id='{node}' AND ready AND last_seen_at>now()-interval '60 seconds' FOR SHARE;
 IF pool IS NULL OR pool='' THEN RAISE EXCEPTION 'A recent verified local node sample is required'; END IF;
 SELECT * INTO sample FROM runtime_capacity_pools WHERE id=pool FOR UPDATE;
 IF sample.id IS NULL OR NOT sample.enforced OR sample.updated_at<now()-interval '60 seconds' THEN RAISE EXCEPTION 'A recent enforced capacity pool is required'; END IF;
 UPDATE runtime_capacity_pools SET
 cpu_limit_millis=GREATEST(0,LEAST(cpu_total_millis-{numbers['reserve_cpu_millis']},{numbers['max_cpu_millis']})),
 memory_limit_bytes=GREATEST(0,LEAST(memory_total_bytes-{numbers['reserve_memory_bytes']},{numbers['max_memory_bytes']})),
 updated_at=now() WHERE id=pool;
END $policy$;
COMMIT;
SELECT json_build_object('cpu_millis',cpu_limit_millis,'memory_bytes',memory_limit_bytes,'enforced',enforced)
FROM runtime_capacity_pools WHERE id=(SELECT capacity_id FROM runtime_nodes WHERE node_id='{node}');
"""
result=sql(query)
summary=json.loads(result.splitlines()[-1])
(state/'capacity-policy-applied.json').write_text(json.dumps(summary,indent=2),encoding='utf-8')
print('Local capacity policy applied: '+json.dumps(summary))
