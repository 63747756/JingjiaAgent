package runtimeadapter

import (
	"context"
	"errors"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

// Persist the fence before removing anything remotely. A lost removal reply
// keeps the fence and reservation; the next recycle retries the same sandbox.
func (l *Ledger) beginRecycle(ctx context.Context, id string) (string, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var state, sandbox string
	if err = tx.QueryRowContext(ctx, `SELECT state,sandbox_id FROM runtime_environments WHERE id=$1 FOR UPDATE`, id).Scan(&state, &sandbox); err != nil {
		return "", err
	}
	if state == "deleted" {
		return "", errors.New("virtual_machine not found")
	}
	if state == "checkpointing" || state == "restoring" {
		var failed bool
		if l.workEnabled {
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_storage_jobs j JOIN runtime_environments e ON e.archiving_checkpoint_id=j.id WHERE e.id=$1 AND j.kind IN ('checkpoint','artifacts') AND j.state='failed' AND j.attempts>=3)`, id).Scan(&failed); err != nil {
				return "", err
			}
		}
		if failed {
			return "", errors.New("工作空间保存失败，自动重试已暂停；请在任务页面重试保存")
		}
		return "", taskflow.ErrRecyclePending
	}
	var active, uncertain bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_commands WHERE environment_id=$1 AND (state IN ('pending','submitting','unknown','running') OR (state='waiting' AND $2<>'stopping'))),
 EXISTS(SELECT 1 FROM runtime_commands WHERE environment_id=$1 AND submission_started)`, id, state).Scan(&active, &uncertain); err != nil {
		return "", err
	}
	if active {
		return "", taskflow.ErrRecyclePending
	}
	if l.workEnabled && sandbox != "" {
		var safe bool
		if err = tx.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM runtime_task_intents i JOIN work_tasks w ON w.task_id=i.task_id WHERE i.environment_id=$1) OR EXISTS(SELECT 1 FROM runtime_environments e JOIN workspace_checkpoints cp ON cp.id=e.checkpoint_id WHERE e.id=$1 AND cp.state='committed' AND cp.environment_id=e.id AND cp.manifest->>'schema'='jingjiaagent.workspace.checkpoint.v3')`, id).Scan(&safe); err != nil {
			return "", err
		}
		if !safe {
			var blocked bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_storage_jobs WHERE environment_id=$1 AND kind='checkpoint' AND state='failed' AND attempts>=3 AND request->>'scope'='workspace')`, id).Scan(&blocked); err != nil {
				return "", err
			}
			if blocked {
				return "", errors.New("完整工作空间保存失败，自动重试已暂停；请在任务页面重试保存")
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO work_storage_jobs(id,task_id,environment_id,kind,request) SELECT gen_random_uuid(),i.task_id,i.environment_id,'checkpoint','{"resume_state":"hibernated","scope":"workspace"}'::jsonb FROM runtime_task_intents i JOIN work_tasks w ON w.task_id=i.task_id WHERE i.environment_id=$1 AND NOT EXISTS(SELECT 1 FROM work_storage_jobs j WHERE j.environment_id=$1 AND j.kind='checkpoint' AND j.state IN ('pending','running','failed') AND NOT EXISTS(SELECT 1 FROM workspace_checkpoints cp WHERE cp.id=j.id AND cp.state='committed'))`, id)
			if err != nil {
				return "", err
			}
			var checkpoint string
			if err = tx.QueryRowContext(ctx, `SELECT j.id::text FROM work_storage_jobs j WHERE j.environment_id=$1 AND j.kind='checkpoint' AND j.state IN ('pending','running','failed') AND NOT EXISTS(SELECT 1 FROM workspace_checkpoints cp WHERE cp.id=j.id AND cp.state='committed') ORDER BY j.created_at,j.id LIMIT 1 FOR UPDATE`, id).Scan(&checkpoint); err != nil {
				return "", err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE work_storage_jobs SET request=request || '{"resume_state":"hibernated","scope":"workspace"}'::jsonb WHERE id=$1`, checkpoint); err != nil {
				return "", err
			}
			// Fence final preservation in the same transaction as the idle
			// recycle decision. A newly accepted message can queue, but cannot
			// start and later be stopped by an obsolete background archive.
			if _, err = tx.ExecContext(ctx, `UPDATE runtime_environments SET state='checkpointing',checkpoint_id=NULL,archiving_checkpoint_id=$2 WHERE id=$1`, id, checkpoint); err != nil {
				return "", err
			}
			if err = tx.Commit(); err != nil {
				return "", err
			}
			return "", taskflow.ErrRecyclePending
		}
	}
	if sandbox == "" && uncertain {
		return "", errAdmissionUncertain
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_environments SET state='stopping' WHERE id=$1`, id); err != nil {
		return "", err
	}
	return sandbox, tx.Commit()
}

func (l *Ledger) completeRecycle(ctx context.Context, id string) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM runtime_environments WHERE id=$1 FOR UPDATE`, id).Scan(&state); err != nil {
		return err
	}
	if state != "stopping" && state != "deleted" {
		return errors.New("runtime recycle fence is missing")
	}
	if err = releaseReservationTx(ctx, tx, id, "recycled"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_environments SET state='deleted' WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}
