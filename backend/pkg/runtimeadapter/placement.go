package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

type schedulingError struct{ message string }

func (e *schedulingError) Error() string { return e.message }

// This probe is private, read-only and never pulls an image. Automatic placement
// only considers hosts where the configured task image can actually run.
func (n *Engine) imageCompatible(ctx context.Context, image string) (bool, error) {
	ctx, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(n.nodeURL, "/")+"/internal/jingjiaagent/image?name="+url.QueryEscape(image), nil)
	if err != nil {
		return false, err
	}
	res, err := n.nodeHTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, errors.New("image readiness probe unavailable")
	}
	var out struct {
		Compatible bool `json:"compatible"`
	}
	err = json.NewDecoder(http.MaxBytesReader(nil, res.Body, 4096)).Decode(&out)
	return out.Compatible, err
}

// A successful reservation pins the host before the first remote mutation.
// All later rounds/restores use that host, including after uncertain RPC replies.
func (c *Client) scheduleEnvironment(ctx context.Context, cmd Command, e Environment) error {
	automatic := len(e.Request.AutoHostIDs) > 0 && e.ProjectID == "" && e.SandboxID == "" && !cmd.Submitted
	restoring := false
	if cmd.Operation == "restore_prepare" {
		var err error
		restoring, err = restoringExistingTask(ctx, c.ledger.db, cmd, e)
		if err != nil {
			return err
		}
	}
	ids := []string{e.NodeID}
	if automatic {
		ids = e.Request.AutoHostIDs
		if c.nodeStore != nil {
			var err error
			ids, err = c.RuntimeNodeIDs(ctx)
			if err != nil {
				return err
			}
		}
	}
	allowed := map[string]bool{}
	if c.registry != nil {
		hosts, err := c.registry.ListRuntimeHosts(ctx, e.OwnerID, ids)
		if err != nil {
			return err
		}
		for id := range hosts {
			allowed[id] = true
		}
	} else {
		for _, id := range ids {
			allowed[id] = true
		}
	}
	type candidate struct {
		id    string
		score float64
	}
	candidates := []candidate{}
	cpu, mem, err := requestedResources(e.Request)
	if err != nil {
		return &schedulingError{"任务 CPU 或内存配置无效，请联系管理员"}
	}
	authorized, ready, compatible, possible, probeFailed := 0, 0, 0, 0, false
	for _, id := range ids {
		if !allowed[id] || c.engine(id) == nil {
			continue
		}
		authorized++
		if automatic || (e.ProjectID == "" && e.SandboxID == "" && !cmd.Submitted && !restoring) {
			accepting, err := c.acceptingNode(ctx, id)
			if err != nil {
				return err
			}
			if !accepting {
				continue
			}
		}
		if c.registry != nil {
			online, err := c.nodeReady(ctx, id)
			if err != nil {
				return err
			}
			if !online {
				continue
			}
		}
		ready++
		if automatic {
			image := runtimeImage(c.node(id), e.Request.ImageURL)
			ok, err := c.engine(id).imageCompatible(ctx, image)
			if err != nil {
				probeFailed = true
				continue
			}
			if !ok {
				continue
			}
		}
		compatible++
		var totalCPU, totalMem, usedCPU, usedMem int64
		err = c.ledger.db.QueryRowContext(ctx, `SELECT p.cpu_limit_millis,p.memory_limit_bytes,
   COALESCE((SELECT sum(cpu_millis) FROM runtime_reservations WHERE capacity_id=p.id AND active AND environment_id<>$2),0),
   COALESCE((SELECT sum(memory_bytes) FROM runtime_reservations WHERE capacity_id=p.id AND active AND environment_id<>$2),0)
   FROM runtime_nodes n JOIN runtime_capacity_pools p ON p.id=n.capacity_id WHERE n.node_id=$1 AND p.enforced`, id, e.ID).Scan(&totalCPU, &totalMem, &usedCPU, &usedMem)
		if errors.Is(err, sql.ErrNoRows) && !c.ledger.capacity.Enabled {
			possible++
			candidates = append(candidates, candidate{id, 0})
			continue
		}
		if errors.Is(err, sql.ErrNoRows) {
			probeFailed = true
			continue
		}
		if err != nil {
			return err
		}
		if cpu > totalCPU || mem > totalMem {
			continue
		}
		possible++
		if usedCPU > totalCPU-cpu || usedMem > totalMem-mem {
			continue
		}
		score := math.Max(float64(usedCPU+cpu)/float64(totalCPU), float64(usedMem+mem)/float64(totalMem))
		candidates = append(candidates, candidate{id, score})
	}
	if authorized == 0 {
		return &schedulingError{"没有可使用的执行节点，请联系管理员检查节点授权"}
	}
	if ready == 0 {
		return c.queueWork(ctx, cmd, "执行节点暂不可用，等待节点恢复")
	}
	if compatible == 0 && ready == authorized && !probeFailed {
		return &schedulingError{"可用执行节点缺少兼容的任务镜像，请联系管理员安装镜像"}
	}
	if possible == 0 && compatible == authorized && !probeFailed {
		return &schedulingError{"任务 CPU 或内存超过执行节点可分配上限，请联系管理员调整资源"}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].score < candidates[j].score
	})
	for _, candidate := range candidates {
		err = c.reservePlacement(ctx, cmd, e, candidate.id, automatic)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errcode.ErrRuntimeCapacityExhausted) && !errors.Is(err, errcode.ErrRuntimeCapacityUnavailable) {
			return err
		}
	}
	reason := "执行节点 CPU 或内存名额不足，正在排队"
	if compatible == 0 || (possible == 0 && probeFailed) {
		reason = "执行节点镜像或容量检查暂不可用，等待节点恢复"
	}
	return c.queueWork(ctx, cmd, reason)
}

func (c *Client) reservePlacement(ctx context.Context, cmd Command, e Environment, node string, automatic bool) error {
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Same order as continuation promotion: environment, then command.
	var current, project, sandbox, state string
	if err = tx.QueryRowContext(ctx, `SELECT node_id,project_id,sandbox_id,state FROM runtime_environments WHERE id=$1 FOR UPDATE`, e.ID).Scan(&current, &project, &sandbox, &state); err != nil {
		return err
	}
	if err = lockCommand(ctx, tx, cmd); err != nil {
		return err
	}
	if state == "deleted" || state == "stopping" {
		return errors.New("runtime environment is unavailable")
	}
	if c.nodeStore != nil {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared(707326030)`); err != nil {
			return err
		}
		var enabled bool
		restoring, err := restoringExistingTask(ctx, tx, cmd, e)
		if err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, `SELECT revoked_at IS NULL AND phase='ready' AND (accepting OR $2) FROM runtime_node_configs WHERE node_id=$1 FOR SHARE`, node, project != "" || sandbox != "" || cmd.Submitted || (restoring && node == current)).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return errcode.ErrRuntimeCapacityUnavailable
		}
	}
	if automatic {
		var admitted bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_commands WHERE environment_id=$1 AND submission_started)`, e.ID).Scan(&admitted); err != nil {
			return err
		}
		if admitted || project != "" || sandbox != "" || current != e.NodeID {
			return errors.New("environment changed during placement")
		}
		e.NodeID = node
		e.Request.HostID = node
		e.Request.AutoHostIDs = nil
	} else if current != node {
		return errors.New("recorded runtime node changed")
	}
	if err = c.ledger.reserveTx(ctx, tx, e); err != nil {
		return err
	}
	if automatic {
		sealed, err := c.ledger.seal(e.ID, mustJSON(e.Request))
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE runtime_environments SET node_id=$2,payload=$3 WHERE id=$1`, e.ID, node, sealed); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE virtualmachines SET host_id=$2 WHERE id=$1`, e.ID, node); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET queue_reason='',preparation_started_at=COALESCE(preparation_started_at,now()) WHERE id=$1`, cmd.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func restoringExistingTask(ctx context.Context, tx taskflow.SQLExecutor, cmd Command, e Environment) (bool, error) {
	if cmd.Operation != "restore_prepare" {
		return false, nil
	}
	var restoring bool
	err := scanSQLRow(ctx, tx, `SELECT EXISTS(
 SELECT 1 FROM runtime_restores r
 JOIN runtime_environments old ON old.id=r.old_environment_id
 JOIN runtime_task_intents i ON i.task_id=r.task_id AND i.environment_id=old.id
 WHERE r.task_id=$1 AND r.new_environment_id=$2 AND r.state='preparing'
 AND old.node_id=$3 AND old.owner_id=$4 AND NOT i.deletion_requested)`,
		[]any{cmd.TaskID, e.ID, e.NodeID, e.OwnerID}, &restoring)
	return restoring, err
}
