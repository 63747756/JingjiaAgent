package runtimeadapter

import (
	"context"
	"database/sql"
	"errors"
	"github.com/google/uuid"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This endpoint checks the stopped workspace and removes it under the SAME
// daemon lifecycle lock. Older nodes cannot silently ignore the protection.
func (c *Client) recyclePreservingWorkspace(ctx context.Context, e Environment, n *Engine, sandbox string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var checkpoint, digest string
	if err := c.ledger.db.QueryRowContext(ctx, `SELECT cp.id::text,cp.payload_sha256 FROM workspace_checkpoints cp JOIN runtime_environments e ON cp.id=e.checkpoint_id WHERE e.id=$1 AND cp.environment_id=e.id AND cp.state='committed' AND cp.manifest->>'schema'='jingjiaagent.workspace.checkpoint.v3'`, e.ID).Scan(&checkpoint, &digest); err != nil {
		return errors.New("完整工作空间尚未保存，不能回收")
	}
	query := url.Values{"sandbox_id": {sandbox}, "checkpoint_id": {checkpoint}, "expected_sha256": {digest}}
	address := strings.TrimRight(n.nodeURL, "/") + "/jingjiaagent/workspace/recycle?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, nil)
	if err != nil {
		return err
	}
	transport := &http.Transport{TLSClientConfig: n.tlsConfig}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: authTransport{base: transport, token: n.token}, Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return err
	} // Uncertain outcome: retain the fence and retry.
	defer res.Body.Close()
	if res.StatusCode == http.StatusNoContent {
		return c.ledger.completeRecycle(ctx, e.ID)
	}
	if res.StatusCode == http.StatusConflict || res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusBadRequest {
		// The node declined before deletion. Keep the saved environment usable.
		state := e.State
		if state == "stopping" {
			state = "hibernated"
		}
		if _, err = c.ledger.db.ExecContext(ctx, `UPDATE runtime_environments SET state=$3,checkpoint_id=CASE WHEN $4 THEN NULL ELSE checkpoint_id END WHERE id=$1 AND state='stopping' AND sandbox_id=$2`, e.ID, sandbox, state, res.StatusCode == http.StatusConflict); err != nil {
			return err
		}
		if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusBadRequest {
			return errors.New("运行节点尚不支持工作区保护回收，请升级执行器；工作空间已保留")
		}
		return errors.New("工作空间与完整备份不一致或尚未停止，原环境已保留，需要重新保存")
	}
	return errors.New("运行节点未确认安全回收，保留工作空间并等待重试")
}

func (c *Client) blockedRecycle(ctx context.Context, env string) error {
	if !c.ledger.workEnabled {
		return nil
	}
	var blocked bool
	err := c.ledger.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_storage_jobs WHERE environment_id=$1 AND kind='recycle' AND state='failed' AND attempts>=3)`, env).Scan(&blocked)
	if err != nil {
		return err
	}
	if blocked {
		return errors.New("自动回收失败，已暂停重试；请在任务页面重试保存与回收")
	}
	return nil
}

func (c *Client) recordRecycleFailure(ctx context.Context, env string, cause error) {
	if !c.ledger.workEnabled {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("recycle:"+env))
	message := "回收未完成，原工作空间已保留。请检查运行节点及存储连接后重试。"
	for _, known := range []string{"工作空间与完整备份不一致", "运行节点尚不支持", "完整工作空间尚未保存", "工作空间保存失败", "完整工作空间保存失败"} {
		if strings.Contains(cause.Error(), known) {
			message = known + "；原工作空间已保留，请处理后重试。"
			break
		}
	}
	_, _ = c.ledger.db.ExecContext(ctx, `INSERT INTO work_storage_jobs(id,task_id,environment_id,kind,state,attempts,error) SELECT $1,i.task_id,$2,'recycle','failed',1,$3 FROM runtime_task_intents i JOIN work_tasks w ON w.task_id=i.task_id WHERE i.environment_id=$2 ON CONFLICT(id) DO UPDATE SET state='failed',attempts=work_storage_jobs.attempts+1,error=$3,updated_at=now()`, id, env, message)
}

// A failed save must not leave an otherwise usable workspace fenced forever.
// Unknown remote state keeps the fence; the visible failure allows explicit retry.
func (c *Client) AbortCheckpoint(ctx context.Context, tid, cid uuid.UUID, env string) error {
	e, err := c.ledger.Environment(ctx, env)
	if err != nil {
		return err
	}
	n := c.engine(e.NodeID)
	if n == nil {
		return errors.New("运行节点不可用")
	}
	state, err := c.checkpointRemoteState(ctx, n, e.SandboxID)
	if err != nil {
		return err
	}
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	if err = tx.QueryRowContext(ctx, `SELECT environment_id FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, tid).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if current != env {
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE runtime_environments SET state=$3,checkpoint_id=NULL,archiving_checkpoint_id=NULL WHERE id=$1 AND state='checkpointing' AND archiving_checkpoint_id=$2 AND EXISTS(SELECT 1 FROM work_storage_jobs WHERE id=$2 AND state='failed' AND attempts>=3 AND lease_token IS NULL)`, env, cid, state)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count > 0 && state == "hibernated" {
		if err = ReleaseCheckpointReservation(ctx, tx, env); err != nil {
			return err
		}
	}
	return tx.Commit()
}
