package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type recoveryProgress struct {
	Engine       string
	NodeMigrated bool
}

func engineIdentity() (string, string, error) {
	b, err := output("docker", "info", "--format", "{{.ID}}")
	if err != nil {
		return "", "", err
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", "", errors.New("Docker engine identity is unavailable")
	}
	h := sha256.Sum256([]byte(id))
	return id, hex.EncodeToString(h[:]), nil
}

func initializeRecovery(s *deployment) error {
	_, engine, err := engineIdentity()
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(s.Root, "recovery-progress.json"), recoveryProgress{Engine: engine})
}

func deploymentSQL(s *deployment, sql string) ([]byte, error) {
	return output("docker", composeArgs(s, "exec", "-T", "postgres", "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "jingjiaagent", "-Atqc", sql)...)
}

// Only the explicit full-host restore path may rebind a local engine. Normal
// startup and heartbeats retain strict identity checks. This runs before the
// API/worker starts, with an idempotent transaction and a host-specific marker.
func prepareRecoveredNode(s *deployment) error {
	path := filepath.Join(s.Root, "recovery-progress.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var progress recoveryProgress
	if err = json.Unmarshal(b, &progress); err != nil {
		return err
	}
	engineID, capacity, err := engineIdentity()
	if err != nil {
		return err
	}
	if progress.Engine != capacity {
		return errors.New("恢复目标 Docker 身份已改变，拒绝自动重新绑定")
	}
	if progress.NodeMigrated {
		return nil
	}
	if info, err := os.Stat(filepath.Join(s.Data, "postgres/PG_VERSION")); err != nil || !info.Mode().IsRegular() {
		return errors.New("恢复的数据目录尚未就绪，拒绝创建空数据库；请保留并恢复解压暂存目录")
	}
	running, err := runningServices(s)
	if err != nil {
		return err
	}
	if running["backend"] || running["runtime"] || running["gateway"] {
		return errors.New("恢复身份迁移尚未完成，不能在 API 或运行时启动后修改节点")
	}
	if err = compose(s, "up", "-d", "--wait", "--wait-timeout", "120", "postgres"); err != nil {
		return err
	}
	if err = activeCommands(s); err != nil {
		return err
	}
	if err = migrateRecoveredImages(s); err != nil {
		return err
	}
	if s.Mode == "single" {
		b, err = deploymentSQL(s, `SELECT COALESCE(json_agg(json_build_object('id',c.node_id,'instance',COALESCE(n.instance_id,''),'capacity',COALESCE(n.capacity_id,''))),'[]'::json) FROM runtime_node_configs c LEFT JOIN runtime_nodes n ON n.node_id=c.node_id WHERE c.imported AND c.endpoint='https://runtime-proxy:7411'`)
		if err != nil {
			return err
		}
		var nodes []struct{ ID, Instance, Capacity string }
		if err = json.Unmarshal(b, &nodes); err != nil {
			return err
		}
		if len(nodes) > 1 {
			return errors.New("备份包含多个本机节点，无法安全自动迁移")
		}
		if len(nodes) == 1 {
			instance, err := os.ReadFile(filepath.Join(s.Data, "runtime/jingjiaagent-node-id"))
			if err != nil {
				return err
			}
			id := strings.TrimSpace(string(instance))
			uuid := regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
			n := nodes[0]
			if !uuid.MatchString(id) || !uuid.MatchString(n.ID) || (n.Instance != "" && n.Instance != id) || (n.Capacity != "" && !shaPattern.MatchString(n.Capacity)) {
				return errors.New("备份中的本机运行目录与节点身份不匹配")
			}
			fingerprint := sha256.Sum256([]byte(id + "\x00" + engineID))
			if _, err = deploymentSQL(s, recoveredNodeSQL(n.ID, id, n.Capacity, capacity, hex.EncodeToString(fingerprint[:]))); err != nil {
				return fmt.Errorf("本机节点恢复迁移失败，未启动业务服务: %w", err)
			}
		}
	}
	progress.NodeMigrated = true
	return writeJSON(path, progress)
}

// Arguments are UUIDs / SHA256 values validated above, never arbitrary input.
func recoveredNodeSQL(node, instance, oldPool, pool, fingerprint string) string {
	return fmt.Sprintf(`BEGIN;
SELECT pg_advisory_xact_lock(707326030);
DO $restore$ BEGIN
 IF EXISTS(SELECT 1 FROM runtime_nodes WHERE node_id<>'%[1]s' AND capacity_id='%[4]s') THEN
  RAISE EXCEPTION 'target engine already assigned to another node';
 END IF;
END $restore$;
INSERT INTO runtime_capacity_pools(id,cpu_total_millis,memory_total_bytes,cpu_limit_millis,memory_limit_bytes,enforced)
 SELECT '%[4]s',cpu_total_millis,memory_total_bytes,cpu_limit_millis,memory_limit_bytes,enforced
 FROM runtime_capacity_pools WHERE id='%[3]s' ON CONFLICT(id) DO NOTHING;
UPDATE runtime_reservations r SET capacity_id='%[4]s',active=false,
 release_reason=CASE WHEN r.active THEN 'verified_stopped_checkpoint' ELSE r.release_reason END,updated_at=now()
 FROM runtime_environments e WHERE r.environment_id=e.id AND e.node_id='%[1]s';
INSERT INTO runtime_nodes(node_id,instance_id,fingerprint,capacity_id,ready,last_error_code)
 VALUES('%[1]s','%[2]s','%[5]s','%[4]s',false,'restored')
 ON CONFLICT(node_id) DO UPDATE SET instance_id=EXCLUDED.instance_id,fingerprint=EXCLUDED.fingerprint,
 capacity_id=EXCLUDED.capacity_id,ready=false,observation=NULL,last_seen_at=NULL,last_error_code='restored';
COMMIT;`, node, instance, oldPool, pool, fingerprint)
}

func checkLocalNode(s *deployment) error {
	if s.Mode != "single" {
		return nil
	}
	b, err := deploymentSQL(s, `SELECT CASE
 WHEN NOT EXISTS(SELECT 1 FROM teams) THEN 'uninitialized'
 WHEN EXISTS(SELECT 1 FROM runtime_node_configs WHERE imported AND endpoint='https://runtime-proxy:7411' AND (NOT accepting OR revoked_at IS NOT NULL)) THEN 'disabled'
 WHEN EXISTS(SELECT 1 FROM runtime_node_configs c JOIN runtime_nodes n ON n.node_id=c.node_id WHERE c.imported AND c.endpoint='https://runtime-proxy:7411' AND c.phase='ready' AND n.ready AND n.last_seen_at>now()-interval '30 seconds') THEN 'ready'
 ELSE 'unavailable' END`)
	if err != nil {
		return err
	}
	switch strings.TrimSpace(string(b)) {
	case "uninitialized", "disabled", "ready":
		return nil
	default:
		return errors.New("本机工作空间节点未通过心跳检查，请检查节点身份和运行时日志")
	}
}

func waitLocalNode(s *deployment) error {
	var err error
	for i := 0; i < 15; i++ {
		if err = checkLocalNode(s); err == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return err
}
