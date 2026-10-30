package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func (c *Client) TaskContextUsage(ctx context.Context, id uuid.UUID) (*taskflow.ContextUsage, error) {
	var raw []byte
	err := c.ledger.db.QueryRowContext(ctx, `SELECT context_usage FROM runtime_task_intents WHERE task_id=$1`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	usage := &taskflow.ContextUsage{}
	if len(raw) > 0 {
		err = json.Unmarshal(raw, usage)
	}
	return usage, err
}

// Serialize with resets/continuations. A late response from before a reset (or
// from an older parallel request) must not resurrect the previous usage value.
func (c *Client) RecordContextUsage(ctx context.Context, vm string, size int, used uint64, started time.Time) error {
	if vm == "" || size <= 0 {
		return nil
	}
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var task string
	var reset sql.NullTime
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT task_id,context_reset_at,context_usage FROM runtime_task_intents WHERE environment_id=$1 AND NOT deletion_requested FOR UPDATE`, vm).Scan(&task, &reset, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var previous taskflow.ContextUsage
	if len(raw) > 0 && json.Unmarshal(raw, &previous) != nil {
		return errors.New("invalid stored context usage")
	}
	if (reset.Valid && !started.After(reset.Time)) || previous.RequestStartedAt > started.UnixMilli() {
		return nil
	}
	usage := taskflow.ContextUsage{Used: &used, Size: size, Estimated: true, UpdatedAt: time.Now().UnixMilli(), RequestStartedAt: started.UnixMilli()}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_task_intents SET context_usage=$2 WHERE task_id=$1`, task, string(mustJSON(usage))); err != nil {
		return err
	}
	if err = appendContextUsage(ctx, tx, task, "context-usage/"+uuid.NewString(), usage); err != nil {
		return err
	}
	return tx.Commit()
}

func appendContextUsage(ctx context.Context, tx *sql.Tx, task, key string, usage taskflow.ContextUsage) error {
	update := map[string]any{"sessionUpdate": "usage_update", "used": usage.Used, "size": usage.Size, "estimated": usage.Estimated, "updated_at": usage.UpdatedAt}
	chunk := taskflow.TaskChunk{Event: "task-running", Kind: "acp_event", Timestamp: time.Now().UnixNano(), Data: mustJSON(map[string]any{"update": update})}
	_, err := tx.ExecContext(ctx, `INSERT INTO runtime_events(task_id,command_id,source_key,turn,chunk) SELECT task_id,id,$2,turn,$3 FROM runtime_commands WHERE task_id=$1 AND operation='task' ORDER BY turn DESC LIMIT 1 ON CONFLICT(command_id,source_key) DO NOTHING`, task, key, string(mustJSON(chunk)))
	return err
}

// Public context resets are logical operations. They do not require a live
// sandbox. The next round clears the native session before submitting a Run.
func (t *taskClient) resetWorkContext(ctx context.Context, request taskflow.RestartTaskReq) (*taskflow.RestartTaskResp, bool, error) {
	if !t.c.ledger.workEnabled || request.LoadSession || request.ExecutionConfig != nil || request.BusinessMutation != nil {
		return nil, false, nil
	}
	var work bool
	if err := t.c.ledger.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_tasks WHERE task_id=$1)`, request.ID).Scan(&work); err != nil {
		return nil, true, err
	}
	if !work {
		return nil, false, nil
	}
	if request.RequestId == "" {
		request.RequestId = uuid.NewString()
	}
	if len(request.RequestId) > 256 {
		return nil, true, errors.New("restart request ID exceeds limit")
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(request.ID.String()+":restart:"+request.RequestId)).String()
	tx, err := t.c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, true, err
	}
	defer tx.Rollback()
	var raw, usageRaw []byte
	var env string
	var deleting bool
	if err = tx.QueryRowContext(ctx, `SELECT payload,environment_id,deletion_requested,context_usage FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, request.ID).Scan(&raw, &env, &deleting, &usageRaw); err != nil {
		return nil, true, err
	}
	if deleting {
		return nil, true, errors.New("任务正在删除，无法重置上下文")
	}
	var result []byte
	err = tx.QueryRowContext(ctx, `SELECT result FROM runtime_commands WHERE id=$1 AND operation='restart'`, id).Scan(&result)
	if err == nil {
		var resp taskflow.RestartTaskResp
		err = json.Unmarshal(result, &resp)
		return &resp, true, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, true, err
	}
	var busy bool
	var turn, restartTurn int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(turn) FILTER(WHERE operation='task'),0),COALESCE(max(turn) FILTER(WHERE operation='restart'),0),COALESCE(bool_or(state NOT IN ('complete','failed','canceled')),false) FROM runtime_commands WHERE task_id=$1`, request.ID).Scan(&turn, &restartTurn, &busy); err != nil {
		return nil, true, err
	}
	if busy {
		return nil, true, errors.New("请等待当前执行或排队消息结束，再重置上下文")
	}
	data, err := t.c.ledger.open(request.ID.String(), raw)
	if err != nil {
		return nil, true, err
	}
	var intent taskflow.CreateTaskReq
	if err = json.Unmarshal(data, &intent); err != nil {
		return nil, true, err
	}
	intent.ContextStartTurn = turn + 1
	intent.Recovered, intent.RecoveryContext, intent.RecoveryInputs = false, "", nil
	intent.RecoveryHistory = nil
	intentData := mustJSON(intent)
	intentSealed, err := t.c.ledger.seal(request.ID.String(), intentData)
	if err != nil {
		return nil, true, err
	}
	payload := mustJSON(restartCommand{Request: request, Intent: intent})
	sealed, err := t.c.ledger.seal(id, payload)
	if err != nil {
		return nil, true, err
	}
	resp := &taskflow.RestartTaskResp{ID: request.ID, RequestId: request.RequestId, Success: true, Message: "上下文已重置，下一轮将使用新上下文"}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_commands(id,environment_id,task_id,operation,turn,payload,payload_hash,state,result) VALUES($1,$2,$3,'restart',$4,$5,$6,'complete',$7)`, id, env, request.ID, restartTurn+1, sealed, hash(payload), string(mustJSON(resp))); err != nil {
		return nil, true, err
	}
	usage := taskflow.ContextUsage{}
	if len(usageRaw) > 0 {
		if err = json.Unmarshal(usageRaw, &usage); err != nil {
			return nil, true, err
		}
	}
	zero := uint64(0)
	usage.Used = &zero
	usage.Estimated = false
	usage.UpdatedAt = time.Now().UnixMilli()
	usage.RequestStartedAt = 0
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_task_intents SET context_reset_id=$2,context_reset_turn=$3,context_reset_at=now(),context_usage=$4,payload=$5,payload_hash=$6 WHERE task_id=$1`, request.ID, id, turn, string(mustJSON(usage)), intentSealed, hash(intentData)); err != nil {
		return nil, true, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM runtime_task_sessions WHERE task_id=$1`, request.ID); err != nil {
		return nil, true, err
	}
	if err = appendContextUsage(ctx, tx, request.ID.String(), "context-reset/"+id, usage); err != nil {
		return nil, true, err
	}
	return resp, true, tx.Commit()
}

func (c *Client) contextBoundary(ctx context.Context, task string) (string, int, error) {
	var id sql.NullString
	var turn int
	err := c.ledger.db.QueryRowContext(ctx, `SELECT context_reset_id,context_reset_turn FROM runtime_task_intents WHERE task_id=$1`, task).Scan(&id, &turn)
	return id.String, turn, err
}

func (c *Client) applyContextReset(ctx context.Context, e Environment, cmd Command, task taskflow.CreateTaskReq, engine *Engine) error {
	id, turn, err := c.contextBoundary(ctx, cmd.TaskID)
	if err != nil {
		return err
	}
	if id == "" || cmd.Turn <= turn {
		return nil
	}
	provider, err := runtimeProvider(task.CodingAgent)
	if err != nil {
		return err
	}
	_, err = engine.session(ctx, e.SandboxID, map[string]any{"op": "restart", "provider": provider, "command_id": id, "load_session": false})
	return err
}
