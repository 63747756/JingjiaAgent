package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

type taskClient struct{ c *Client }

func (t *taskClient) route(ctx context.Context, id string) (Environment, *Engine, taskflow.TaskManager, error) {
	e, err := t.c.ledger.EnvironmentForTask(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		old, err := t.c.legacyTask()
		return e, nil, old, err
	}
	if err != nil {
		return e, nil, nil, err
	}
	e, n, err := t.c.environment(ctx, e.ID)
	return e, n, nil, err
}
func (t *taskClient) Create(ctx context.Context, r taskflow.CreateTaskReq) error {
	e, _, old, err := t.route(ctx, r.ID.String())
	if err != nil {
		return err
	}
	if old != nil {
		return old.Create(ctx, r)
	}
	if e.ID != r.VMID {
		return errors.New("task environment mismatch")
	}
	r.McpConfigs = t.c.runtimeMCPConfigs(r.McpConfigs)
	tx, err := t.c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var canceled bool
	if err = tx.QueryRowContext(ctx, `SELECT cancel_requested FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, r.ID).Scan(&canceled); err != nil {
		return err
	}
	if canceled {
		return errors.New("task canceled before admission")
	}
	b, err := marshal(r)
	if err != nil {
		return err
	}
	if err = t.c.ledger.enqueueTx(ctx, tx, e.ID, r.ID.String(), "task", 1, b); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='processing',completed_at=NULL WHERE id=$1 AND EXISTS(SELECT 1 FROM runtime_commands WHERE task_id=$1 AND operation='task' AND turn=1 AND state IN ('pending','submitting','unknown','running'))`, r.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (t *taskClient) Stop(ctx context.Context, r taskflow.TaskReq) error {
	return t.stop(ctx, r, false)
}
func (t *taskClient) Cancel(ctx context.Context, r taskflow.TaskReq) error {
	return t.stop(ctx, r, true)
}
func (t *taskClient) stop(ctx context.Context, r taskflow.TaskReq, cancel bool) error {
	if r.Task == nil {
		return errors.New("missing task")
	}
	e, n, old, err := t.route(ctx, r.Task.ID.String())
	if err != nil {
		return err
	}
	if old != nil {
		if cancel {
			return old.Cancel(ctx, r)
		}
		return old.Stop(ctx, r)
	}
	tx, err := t.c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_task_intents SET cancel_requested=true WHERE task_id=$1`, r.Task.ID); err != nil {
		return err
	}
	if !cancel {
		if _, err = tx.ExecContext(ctx, `UPDATE runtime_environments SET state='stopping' WHERE id=$1 AND state<>'deleted'`, e.ID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET cancel_requested=true WHERE task_id=$1 AND state NOT IN ('complete','failed','canceled') AND (NOT $2 OR operation<>'restart')`, r.Task.ID, cancel); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	commands, err := t.c.ledger.ActiveCommands(ctx, r.Task.ID.String())
	if err != nil {
		return err
	}
	for _, c := range commands {
		if c.RunID != "" && c.State != "complete" && c.State != "failed" && c.State != "canceled" {
			if _, err = n.runs.StopRun(ctx, connect.NewRequest(&v2.StopRunRequest{RunId: c.RunID})); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) StopTaskAndWait(ctx context.Context, id string) (bool, error) {
	e, err := c.ledger.EnvironmentForTask(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if e.State == "deleted" {
		return true, nil
	}
	taskID, err := uuid.Parse(id)
	if err != nil {
		return true, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = c.TaskManager().Stop(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID}}); err != nil {
		return true, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		commands, err := c.ledger.ActiveCommands(ctx, id)
		if err != nil {
			return true, err
		}
		if len(commands) == 0 {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-ticker.C:
		}
	}
}
func (t *taskClient) Continue(ctx context.Context, r taskflow.TaskReq) error {
	if r.Task == nil {
		return errors.New("missing task")
	}
	e, _, old, err := t.route(ctx, r.Task.ID.String())
	if err != nil {
		return err
	}
	if old != nil {
		return old.Continue(ctx, r)
	}
	if e.State == "stopping" {
		return errors.New("task environment is stopping")
	}
	if strings.TrimSpace(r.Task.Text) == "" {
		return errors.New("empty task continuation")
	}
	tx, err := t.c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var turn int
	var active int
	var payload []byte
	if err = tx.QueryRowContext(ctx, `SELECT payload FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, r.Task.ID).Scan(&payload); err != nil {
		return err
	}
	data, err := t.c.ledger.open(r.Task.ID.String(), payload)
	if err != nil {
		return err
	}
	var intent taskflow.CreateTaskReq
	if err = json.Unmarshal(data, &intent); err != nil {
		return err
	}
	intent.Text, intent.Attachments = r.Task.Text, r.Task.Attachments
	intent.ClientMessageID = r.Task.ClientMessageID
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM runtime_environments WHERE id=$1 FOR UPDATE`, e.ID).Scan(&state); err != nil {
		return err
	}
	if state == "stopping" || state == "deleted" {
		return errors.New("task environment is stopping or unavailable")
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(turn) FILTER(WHERE operation='task'),0),count(*) FILTER(WHERE state IN ('pending','submitting','unknown','running')) FROM runtime_commands WHERE task_id=$1`, r.Task.ID).Scan(&turn, &active); err != nil {
		return err
	}
	if active != 0 {
		return errors.New("task still has an active run")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_task_intents SET cancel_requested=false WHERE task_id=$1`, r.Task.ID); err != nil {
		return err
	}
	b, err := marshal(intent)
	if err != nil {
		return err
	}
	if err = t.c.ledger.enqueueTx(ctx, tx, e.ID, r.Task.ID.String(), "task", turn+1, b); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='processing',completed_at=NULL WHERE id=$1`, r.Task.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (t *taskClient) Restart(ctx context.Context, r taskflow.RestartTaskReq) (*taskflow.RestartTaskResp, error) {
	env, _, old, err := t.route(ctx, r.ID.String())
	if err != nil {
		return nil, err
	}
	if old != nil {
		return old.Restart(ctx, r)
	}
	return t.restart(ctx, env, r)
}
func (t *taskClient) AutoApprove(ctx context.Context, r taskflow.TaskApproveReq) error {
	e, _, old, err := t.route(ctx, r.ID.String())
	if err != nil {
		return err
	}
	if old != nil {
		return old.AutoApprove(ctx, r)
	}
	return t.approve(ctx, e, r)
}
func (t *taskClient) AskUserQuestion(ctx context.Context, r taskflow.AskUserQuestionResponse) error {
	e, _, old, err := t.route(ctx, r.TaskId)
	if err != nil {
		return err
	}
	if old != nil {
		return old.AskUserQuestion(ctx, r)
	}
	return t.answer(ctx, e, r)
}
func (t *taskClient) ListFiles(ctx context.Context, r taskflow.RepoListFilesReq) (*taskflow.RepoListFiles, error) {
	e, n, old, err := t.route(ctx, r.TaskId)
	if err != nil {
		return nil, err
	}
	if old != nil {
		return old.ListFiles(ctx, r)
	}
	return t.listFiles(ctx, r, e, n)
}
func (t *taskClient) ReadFile(ctx context.Context, r taskflow.RepoReadFileReq) (*taskflow.RepoReadFile, error) {
	e, n, old, err := t.route(ctx, r.TaskId)
	if err != nil {
		return nil, err
	}
	if old != nil {
		return old.ReadFile(ctx, r)
	}
	return t.readFile(ctx, r, e, n)
}
func (t *taskClient) FileDiff(ctx context.Context, r taskflow.RepoFileDiffReq) (*taskflow.RepoFileDiff, error) {
	e, n, old, err := t.route(ctx, r.TaskId)
	if err != nil {
		return nil, err
	}
	if old != nil {
		return old.FileDiff(ctx, r)
	}
	result := &taskflow.RepoFileDiff{TaskId: r.TaskId, RequestId: r.RequestId, Path: r.Path}
	err = n.files(ctx, e.SandboxID, map[string]any{"op": "repo_diff", "path": r.Path, "context_lines": r.ContextLines, "unified": r.Unified}, &result.Diff)
	result.Success = err == nil
	return result, err
}
func (t *taskClient) FileChanges(ctx context.Context, r taskflow.RepoFileChangesReq) (*taskflow.RepoFileChanges, error) {
	e, n, old, err := t.route(ctx, r.TaskId)
	if err != nil {
		return nil, err
	}
	if old != nil {
		return old.FileChanges(ctx, r)
	}
	result := &taskflow.RepoFileChanges{TaskId: r.TaskId, RequestId: r.RequestId, Changes: []*taskflow.RepoFileChangeInfo{}}
	err = n.files(ctx, e.SandboxID, map[string]any{"op": "repo_changes"}, result)
	result.Success = err == nil
	return result, err
}
