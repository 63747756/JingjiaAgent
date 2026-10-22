package runtimeadapter

import (
	"context"
	"database/sql"
	"errors"
)

var errWorkQueued = errors.New("task queued")

func workEnvironment(e Environment) bool {
	for _, entry := range e.Request.Envs {
		if entry == "JINGJIAAGENT_WORK_MODE=1" {
			return true
		}
	}
	return false
}

// One durable task slot covers preparation and the accepted prompt round.
// The team policy is read for each new admission; existing runs are not killed
// when an administrator lowers a limit. Queue entries never occupy slots.
func (c *Client) acquireWorkSlot(ctx context.Context, cmd Command) error {
	if cmd.TaskID == "" {
		return nil
	}
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(745923811)`); err != nil {
		return err
	}
	if err = lockCommand(ctx, tx, cmd); err != nil {
		return err
	}
	var owner, team string
	var personal, totalLimit int
	err = tx.QueryRowContext(ctx, `SELECT t.user_id,tm.id,tm.task_concurrency_limit,tm.task_total_concurrency_limit
 FROM tasks t LEFT JOIN work_tasks w ON w.task_id=t.id
 JOIN teams tm ON tm.id=COALESCE(w.team_id,(SELECT m.team_id FROM team_members m JOIN teams x ON x.id=m.team_id WHERE m.user_id=t.user_id AND x.deleted_at IS NULL ORDER BY x.created_at,x.id LIMIT 1))
 WHERE t.id=$1 AND t.deleted_at IS NULL AND tm.deleted_at IS NULL`, cmd.TaskID).Scan(&owner, &team, &personal, &totalLimit)
	if errors.Is(err, sql.ErrNoRows) {
		return &schedulingError{"任务所属团队不存在，无法读取任务策略"}
	}
	if err != nil {
		return err
	}
	if personal <= 0 || totalLimit <= 0 {
		return &schedulingError{"任务并发策略无效，请联系管理员"}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM work_run_slots s USING runtime_commands c WHERE s.command_id=c.id AND (c.state IN ('failed','canceled') OR (c.state IN ('waiting','blocked') AND NOT c.submission_started) OR (c.state='complete' AND c.operation IN ('task','restore_prepare')))`); err != nil {
		return err
	}
	var existing bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_run_slots WHERE task_id=$1)`, cmd.TaskID).Scan(&existing); err != nil {
		return err
	}
	if existing {
		if _, err = tx.ExecContext(ctx, `UPDATE work_run_slots SET command_id=$2 WHERE task_id=$1`, cmd.TaskID, cmd.ID); err != nil {
			return err
		}
		return tx.Commit()
	}
	var total, active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE owner_id=$2) FROM work_run_slots WHERE team_id=$1`, team, owner).Scan(&total, &active); err != nil {
		return err
	}
	reason := ""
	if active >= personal {
		reason = "已达到个人并发上限，正在排队"
	} else if total >= totalLimit {
		reason = "已达到全局并发上限，正在排队"
	}
	if reason != "" {
		if err = queueCommandTx(ctx, tx, cmd, reason); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return errWorkQueued
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO work_run_slots(command_id,task_id,owner_id,team_id) VALUES($1,$2,$3,$4)`, cmd.ID, cmd.TaskID, owner, team); err != nil {
		return err
	}
	return tx.Commit()
}

func queueCommandTx(ctx context.Context, tx *sql.Tx, cmd Command, reason string) error {
	_, err := tx.ExecContext(ctx, `UPDATE runtime_commands SET state=CASE WHEN state='waiting' THEN 'waiting' ELSE 'pending' END,
 queue_reason=$2,preparation_phase=CASE WHEN operation='task' AND preparation_phase<>'' THEN 'queued' ELSE preparation_phase END,
 preparation_error='',preparation_started_at=NULL,lease_token=NULL,lease_until=NULL,available_at=now()+interval '2 seconds' WHERE id=$1`, cmd.ID, reason)
	return err
}
func (c *Client) queueWork(ctx context.Context, cmd Command, reason string) error {
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockCommand(ctx, tx, cmd); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM work_run_slots WHERE command_id=$1`, cmd.ID); err != nil {
		return err
	}
	if err = queueCommandTx(ctx, tx, cmd, reason); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return errWorkQueued
}
