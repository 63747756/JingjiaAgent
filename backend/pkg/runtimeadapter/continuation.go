package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

// RecoveryChoiceError requires an explicit choice before losing newer progress.
type RecoveryChoiceError struct {
	CheckpointID uuid.UUID
	Message      string
}

func (e *RecoveryChoiceError) Error() string { return e.Message }

func (c *Client) SetContinuationRestore(fn func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (string, error)) {
	c.continuationRestore = fn
}

func (c *Client) setContinuationPhase(ctx context.Context, cmd Command, phase string) error {
	if !c.ledger.workEnabled || cmd.Turn <= 1 {
		return nil
	}
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockCommand(ctx, tx, cmd); err != nil {
		return err
	}
	var canceled bool
	if err = tx.QueryRowContext(ctx, `SELECT cancel_requested FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&canceled); err != nil {
		return err
	}
	if canceled {
		return errCanceledBeforeAdmission
	}
	// Keep commands without continuation preparation outside this UI lifecycle.
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET preparation_phase=$2 WHERE id=$1 AND preparation_phase<>'' AND NOT submission_started`, cmd.ID, phase); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Client) acceptContinuation(ctx context.Context, input *taskflow.Task) error {
	if strings.TrimSpace(input.Text) == "" && input.MentionedSkillID == nil {
		return errors.New("消息不能为空")
	}
	if input.ClientMessageID == "" {
		return errors.New("消息缺少唯一标识，请刷新页面重试")
	}
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var env string
	var sealed []byte
	var deleting bool
	if err = tx.QueryRowContext(ctx, `SELECT environment_id,payload,deletion_requested FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, input.ID).Scan(&env, &sealed, &deleting); err != nil {
		return err
	}
	if deleting {
		return errors.New("任务正在删除，暂不能发送新消息")
	}
	// Match the immutable receipt before checking current skills or environment.
	var receipt []byte
	err = tx.QueryRowContext(ctx, `SELECT decode(chunk->>'data','base64') FROM runtime_events WHERE task_id=$1 AND chunk->>'event'='user-input' AND convert_from(decode(chunk->>'data','base64'),'UTF8')::jsonb->>'client_message_id'=$2 LIMIT 1`, input.ID, input.ClientMessageID).Scan(&receipt)
	if err == nil {
		var old struct {
			Content     []byte                 `json:"content"`
			Attachments []taskflow.Attachment  `json:"attachments"`
			Mention     *taskflow.SkillMention `json:"skill_mention"`
		}
		if err = json.Unmarshal(receipt, &old); err != nil {
			return err
		}
		var id *uuid.UUID
		if old.Mention != nil {
			id = &old.Mention.ID
		}
		if string(old.Content) != input.Text || !sameMentionID(id, input.MentionedSkillID) || !sameInputAttachments(old.Attachments, input.Attachments) {
			return errors.New("同一消息标识不能用于不同内容")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	data, err := c.ledger.open(input.ID.String(), sealed)
	if err != nil {
		return err
	}
	var task taskflow.CreateTaskReq
	if err = json.Unmarshal(data, &task); err != nil {
		return err
	}
	var owner uuid.UUID
	var turn, queued int
	if err = tx.QueryRowContext(ctx, `SELECT owner_id FROM runtime_environments WHERE id=$1`, env).Scan(&owner); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(turn) FILTER(WHERE operation='task'),0),count(*) FILTER(WHERE state IN ('waiting','blocked')) FROM runtime_commands WHERE task_id=$1`, input.ID).Scan(&turn, &queued); err != nil {
		return err
	}
	if queued >= 10 {
		return errors.New("等待中的消息过多，请等待处理或取消后再发送")
	}
	task.Text, task.UserText, task.Attachments, task.ClientMessageID = input.Text, &input.Text, input.Attachments, input.ClientMessageID
	task.Recovered, task.RecoveryContext, task.RecoveryInputs = false, "", nil
	task.RecoveryHistory = nil
	task.SkillMention = nil
	if c.skillResolver != nil {
		task.RoundSkills, task.SkillMention, err = c.skillResolver.ResolveRoundSkills(ctx, owner, input.ID, input.MentionedSkillID)
		if err != nil {
			return err
		}
		task.Text = taskflow.SkillMentionPrompt(input.Text, task.SkillMention)
	} else if input.MentionedSkillID != nil {
		return errors.New("当前运行环境不支持技能引用")
	}
	if err = c.validateRoundResources(ctx, &task); err != nil {
		return err
	}
	if err = c.ledger.enqueueTx(ctx, tx, env, input.ID.String(), "task", turn+1, mustJSON(task)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET state='waiting',preparation_phase='queued' WHERE task_id=$1 AND operation='task' AND turn=$2`, input.ID, turn+1); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_task_intents SET cancel_requested=false WHERE task_id=$1`, input.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='processing',completed_at=NULL,last_active_at=now() WHERE id=$1`, input.ID); err != nil {
		return err
	}
	return c.commitAndNotifyWorker(tx)
}

type ContinuationStatus struct {
	ID           string     `json:"id"`
	MessageID    string     `json:"message_id"`
	State        string     `json:"state"`
	Phase        string     `json:"phase"`
	Error        string     `json:"error,omitempty"`
	CheckpointID *uuid.UUID `json:"checkpoint_id,omitempty"`
}

func (c *Client) Continuations(ctx context.Context, owner, task uuid.UUID) ([]ContinuationStatus, error) {
	e, err := c.ledger.EnvironmentForTask(ctx, task.String())
	if err != nil {
		return nil, err
	}
	if e.OwnerID != owner.String() {
		return nil, errors.New("permission denied")
	}
	rows, err := c.ledger.db.QueryContext(ctx, `SELECT id,state,preparation_phase,COALESCE(NULLIF(queue_reason,''),preparation_error),preparation_checkpoint_id,payload FROM runtime_commands WHERE task_id=$1 AND operation='task' AND state NOT IN ('complete','failed','canceled') ORDER BY turn LIMIT 10`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContinuationStatus{}
	for rows.Next() {
		var s ContinuationStatus
		var raw []byte
		if err = rows.Scan(&s.ID, &s.State, &s.Phase, &s.Error, &s.CheckpointID, &raw); err != nil {
			return nil, err
		}
		data, err := c.ledger.open(s.ID, raw)
		if err != nil {
			return nil, err
		}
		var input taskflow.CreateTaskReq
		if err = json.Unmarshal(data, &input); err != nil {
			return nil, err
		}
		s.MessageID = input.ClientMessageID
		out = append(out, s)
	}
	return out, rows.Err()
}

func (c *Client) RetryContinuation(ctx context.Context, owner, task, command uuid.UUID, checkpoint *uuid.UUID, cancel bool) error {
	e, err := c.ledger.EnvironmentForTask(ctx, task.String())
	if err != nil {
		return err
	}
	if e.OwnerID != owner.String() {
		return errors.New("permission denied")
	}
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	var choice *uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT state,preparation_checkpoint_id FROM runtime_commands WHERE id=$1 AND task_id=$2 AND NOT submission_started FOR UPDATE`, command, task).Scan(&state, &choice); err != nil {
		return err
	}
	if state != "blocked" && !(cancel && state == "waiting") {
		return errors.New("消息已在处理，请刷新状态")
	}
	if checkpoint != nil && (choice == nil || *choice != *checkpoint) {
		return errors.New("恢复节点已变化，请刷新状态")
	}
	if cancel {
		_, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET state='canceled',cancel_requested=true,lease_token=NULL,lease_until=NULL,preparation_error='' WHERE id=$1`, command)
		if err == nil {
			err = cancelContinuationEvent(ctx, tx, command.String())
		}
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET state='waiting',preparation_phase='queued',preparation_error='',preparation_started_at=NULL,recovery_checkpoint_id=$2,lease_token=NULL,lease_until=NULL,available_at=now() WHERE id=$1`, command, checkpoint)
	}
	if err != nil {
		return err
	}
	if c.ledger.workEnabled {
		if _, err = tx.ExecContext(ctx, `DELETE FROM work_run_slots WHERE command_id=$1`, command); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (c *Client) waitContinuation(ctx context.Context, cmd Command, phase, message string, blocked bool, checkpoint *uuid.UUID) error {
	state := "waiting"
	if blocked {
		state = "blocked"
	}
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE runtime_commands SET state=$3,preparation_phase=$4,queue_reason='',preparation_error=$5,preparation_checkpoint_id=$6,lease_token=NULL,lease_until=NULL,available_at=now()+interval '2 seconds',updated_at=now() WHERE id=$1 AND lease_token=$2 AND lease_until>now() AND NOT submission_started`, cmd.ID, cmd.Lease, state, phase, message, checkpoint)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errLeaseLost
	}
	// This round has not been submitted. Publish its waiting/blocked state and
	// release admission together; a retry must compete for a fresh slot.
	if c.ledger.workEnabled {
		if _, err = tx.ExecContext(ctx, `DELETE FROM work_run_slots WHERE command_id=$1`, cmd.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (c *Client) prepareContinuation(ctx context.Context, cmd *Command) error {
	// Preparation errors never cross the unknown-admission boundary: no prompt
	// has been submitted. Surface a bounded failure instead of an endless spinner.
	err := c.prepareContinuationAttempt(ctx, cmd)
	if err == nil || errors.Is(err, errLeaseLost) || errors.Is(err, errWorkQueued) {
		return err
	}
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	var choice *RecoveryChoiceError
	if errors.As(err, &choice) {
		return c.waitContinuation(cleanup, *cmd, "failed", choice.Message, true, &choice.CheckpointID)
	}
	var expired bool
	if e := c.ledger.db.QueryRowContext(cleanup, `SELECT COALESCE(preparation_started_at<now()-interval '5 minutes',false) FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&expired); e != nil {
		return e
	}
	return c.waitContinuation(cleanup, *cmd, "preparing", "工作环境暂不可用，正在重试；请检查运行节点、容量和存储。", expired, nil)
}

func (c *Client) prepareContinuationAttempt(ctx context.Context, cmd *Command) error {
	var canceled, previous bool
	if err := c.ledger.db.QueryRowContext(ctx, `SELECT cancel_requested,EXISTS(SELECT 1 FROM runtime_commands p WHERE p.task_id=c.task_id AND p.id<>c.id AND ((p.operation='task' AND p.turn<c.turn AND p.state NOT IN ('complete','failed','canceled')) OR (p.operation<>'task' AND p.operation<>'restore_prepare' AND p.state IN ('pending','submitting','unknown','running')))) FROM runtime_commands c WHERE c.id=$1`, cmd.ID).Scan(&canceled, &previous); err != nil {
		return err
	}
	if canceled {
		tx, err := c.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = lockCommand(ctx, tx, *cmd); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET state='canceled',lease_token=NULL,lease_until=NULL WHERE id=$1`, cmd.ID); err != nil {
			return err
		}
		if err = cancelContinuationEvent(ctx, tx, cmd.ID); err != nil {
			return err
		}
		if c.ledger.workEnabled {
			if _, err = tx.ExecContext(ctx, `DELETE FROM work_run_slots WHERE command_id=$1`, cmd.ID); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	if previous {
		return c.waitContinuation(ctx, *cmd, "queued", "", false, nil)
	}
	if _, err := c.ledger.db.ExecContext(ctx, `UPDATE runtime_commands SET preparation_started_at=COALESCE(preparation_started_at,now()) WHERE id=$1 AND lease_token=$2`, cmd.ID, cmd.Lease); err != nil {
		return err
	}
	e, err := c.ledger.EnvironmentForTask(ctx, cmd.TaskID)
	if err != nil {
		return err
	}
	owner, err := uuid.Parse(e.OwnerID)
	if err != nil {
		return err
	}
	tid := uuid.MustParse(cmd.TaskID)
	if c.workGuard != nil {
		if err = c.workGuard.CheckWorkTaskAccess(ctx, owner, tid, true); err != nil {
			return c.waitContinuation(ctx, *cmd, "failed", "任务或资料的访问权限已变化，无法继续。", true, nil)
		}
	}
	if c.registry != nil {
		online, checkErr := c.nodeReady(ctx, e.NodeID)
		if checkErr != nil {
			return checkErr
		}
		if !online {
			return c.queueWork(ctx, *cmd, "原执行节点暂不可用，等待节点恢复")
		}
	}
	var restoring sql.NullString
	err = c.ledger.db.QueryRowContext(ctx, `SELECT state FROM runtime_restores WHERE task_id=$1`, cmd.TaskID).Scan(&restoring)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if restoring.String == "preparing" {
		var reason string
		_ = c.ledger.db.QueryRowContext(ctx, `SELECT queue_reason FROM runtime_commands WHERE task_id=$1 AND operation='restore_prepare' AND state IN ('pending','unknown') ORDER BY created_at DESC LIMIT 1`, cmd.TaskID).Scan(&reason)
		if reason != "" {
			return c.queueWork(ctx, *cmd, reason)
		}
		return c.waitOrExpireContinuation(ctx, *cmd, "restoring", "恢复工作环境超时，请检查运行节点后重试。")
	}
	if e.State == "checkpointing" {
		var paused bool
		if err = c.ledger.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_storage_jobs j JOIN runtime_environments e ON e.archiving_checkpoint_id=j.id WHERE e.id=$1 AND j.state='failed' AND j.attempts>=3)`, e.ID).Scan(&paused); err != nil {
			return err
		}
		if paused {
			return c.waitContinuation(ctx, *cmd, "saving", "工作空间保存失败，自动重试已暂停。请先重试保存。", true, nil)
		}
		return c.waitOrExpireContinuation(ctx, *cmd, "saving", "上一轮工作保存尚未完成，请检查存储后重试。")
	}
	if e.State == "stopping" {
		return c.waitOrExpireContinuation(ctx, *cmd, "preparing", "工作环境尚未完成停止，请稍后重试。")
	}
	lost := e.State == "deleted"
	if !lost {
		_, engine, err := c.environmentUnchecked(ctx, e.ID)
		if err != nil {
			return err
		}
		if e.SandboxID == "" {
			return c.waitContinuation(ctx, *cmd, "failed", "原工作环境未完成初始化，暂无可恢复的工作文件。", true, nil)
		}
		probe, err := engine.sandboxes.GetSandbox(ctx, connect.NewRequest(&v2.GetSandboxRequest{SandboxId: e.SandboxID}))
		if err != nil {
			if connect.CodeOf(err) != connect.CodeNotFound {
				return err
			}
			lost = true
		} else if probe.Msg.GetSandbox() == nil {
			return errors.New("empty sandbox response")
		}
	}
	if lost {
		// Select and verify the actual checkpoint before deciding whether it
		// covers a repository. Full workspace snapshots can restore Git/ZIP
		// tasks; RestoreCheckpoint still rejects their older partial snapshots.
		if c.continuationRestore == nil {
			return c.waitContinuation(ctx, *cmd, "failed", "工作恢复服务未配置，请联系管理员。", true, nil)
		}
		// A confirmed missing sandbox can be replaced. A transport timeout cannot.
		if e.State != "deleted" {
			if _, err = c.ledger.db.ExecContext(ctx, `UPDATE runtime_environments SET state='offline' WHERE id=$1 AND state NOT IN ('checkpointing','restoring','stopping','deleted')`, e.ID); err != nil {
				return err
			}
		}
		var choice *uuid.UUID
		if err = c.ledger.db.QueryRowContext(ctx, `SELECT recovery_checkpoint_id FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&choice); err != nil {
			return err
		}
		cid := uuid.Nil
		if choice != nil {
			cid = *choice
		}
		_, err = c.continuationRestore(ctx, owner, tid, cid)
		if err != nil {
			return err
		}
		return c.waitContinuation(ctx, *cmd, "restoring", "", false, nil)
	}
	var task taskflow.CreateTaskReq
	if err = json.Unmarshal(cmd.Payload, &task); err != nil {
		return err
	}
	// Preserve the accepted model/skills. Only bind this command to the verified
	// replacement environment; never copy its stale model back over the intent.
	if task.VMID != e.ID {
		task.VMID = e.ID
		task.Recovered = true
	}
	if err = c.freezeRecoveryContext(ctx, cmd, &task); err != nil {
		return err
	}
	b := mustJSON(task)
	sealed, err := c.ledger.seal(cmd.ID, b)
	if err != nil {
		return err
	}
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	if err = tx.QueryRowContext(ctx, `SELECT environment_id FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, cmd.TaskID).Scan(&current); err != nil {
		return err
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM runtime_environments WHERE id=$1 FOR UPDATE`, e.ID).Scan(&state); err != nil {
		return err
	}
	if err = lockCommand(ctx, tx, *cmd); err != nil {
		return err
	}
	var wasCanceled bool
	if err = tx.QueryRowContext(ctx, `SELECT cancel_requested FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&wasCanceled); err != nil {
		return err
	}
	if wasCanceled {
		return errors.New("continuation canceled")
	}
	if current != e.ID || state == "checkpointing" || state == "deleted" || state == "stopping" {
		return errors.New("environment changed during preparation")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET state='pending',environment_id=$2,payload=$3,payload_hash=$4,preparation_phase='starting',queue_reason='',preparation_error='',lease_token=NULL,lease_until=NULL,available_at=now() WHERE id=$1 AND NOT cancel_requested`, cmd.ID, e.ID, sealed, hash(b)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_environments SET checkpoint_id=NULL WHERE id=$1`, e.ID); err != nil {
		return err
	}
	return c.commitAndNotifyWorker(tx)
}

func cancelContinuationEvent(ctx context.Context, tx *sql.Tx, id string) error {
	chunk := taskflow.TaskChunk{Event: "task-ended", Timestamp: time.Now().UnixNano(), Data: mustJSON(map[string]string{"status": "canceled"})}
	_, err := tx.ExecContext(ctx, `INSERT INTO runtime_events(task_id,command_id,source_key,turn,chunk) SELECT task_id,id,'task-ended',turn,$2 FROM runtime_commands WHERE id=$1 ON CONFLICT(command_id,source_key) DO NOTHING`, id, string(mustJSON(chunk)))
	return err
}

func (c *Client) cancelWaitingContinuations(ctx context.Context, task uuid.UUID) (bool, error) {
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var current string
	if err = tx.QueryRowContext(ctx, `SELECT environment_id FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, task).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `UPDATE runtime_commands SET state='canceled',cancel_requested=true,lease_token=NULL,lease_until=NULL WHERE task_id=$1 AND state IN ('waiting','blocked') AND NOT submission_started RETURNING id`, task)
	if err != nil {
		return false, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if err = cancelContinuationEvent(ctx, tx, id); err != nil {
			return false, err
		}
	}
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_commands WHERE task_id=$1 AND state IN ('pending','submitting','unknown','running') AND operation<>'restore_prepare')`, task).Scan(&active); err != nil {
		return false, err
	}
	return len(ids) > 0 && !active, tx.Commit()
}

func (c *Client) waitOrExpireContinuation(ctx context.Context, cmd Command, phase, message string) error {
	var expired bool
	if err := c.ledger.db.QueryRowContext(ctx, `SELECT preparation_started_at<now()-interval '5 minutes' FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&expired); err != nil {
		return err
	}
	if !expired {
		message = ""
	}
	return c.waitContinuation(ctx, cmd, phase, message, expired, nil)
}
