package runtimeadapter

import (
	"context"
	"database/sql"
	"errors"
)

// ReconcileCreations closes only journaled, expired, never-admitted requests.
// VM-first locks serialize with product admission. Runtime identity/commands,
// owner mismatch or changed product state cause quarantine, never execution.
func (c *Client) ReconcileCreations(ctx context.Context) error {
	rows, err := c.ledger.db.QueryContext(ctx, `SELECT vm_id FROM runtime_creation_attempts WHERE state='pending' AND expires_at<=now() ORDER BY expires_at LIMIT 32`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = c.ledger.expireCreation(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
func (l *Ledger) expireCreation(ctx context.Context, id string) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner, environment string
	var deleted bool
	err = tx.QueryRowContext(ctx, `SELECT user_id,COALESCE(environment_id,''),deleted_at IS NOT NULL FROM virtualmachines WHERE id=$1 FOR UPDATE SKIP LOCKED`, id).Scan(&owner, &environment, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		// SKIP LOCKED may denote a live admission transaction. A genuinely
		// absent product VM is quarantined so it cannot starve later attempts.
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM virtualmachines WHERE id=$1)`, id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		if _, err = tx.ExecContext(ctx, `UPDATE runtime_creation_attempts SET state='uncertain',updated_at=now() WHERE vm_id=$1 AND state='pending' AND expires_at<=now()`, id); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	var attemptOwner, task, state string
	var expired bool
	err = tx.QueryRowContext(ctx, `SELECT owner_id,task_id,state,expires_at<=now() FROM runtime_creation_attempts WHERE vm_id=$1 FOR UPDATE`, id).Scan(&attemptOwner, &task, &state, &expired)
	if err != nil {
		return err
	}
	if state != "pending" || !expired {
		return nil
	}
	var runtime, associated, pending bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_environments WHERE id=$1) OR EXISTS(SELECT 1 FROM runtime_task_intents WHERE task_id=$2),
 EXISTS(SELECT 1 FROM task_virtualmachines WHERE virtualmachine_id=$1 AND task_id=$2),
 EXISTS(SELECT 1 FROM tasks WHERE id=$2 AND user_id=$3 AND status='pending')`, id, task, attemptOwner).Scan(&runtime, &associated, &pending)
	if err != nil {
		return err
	}
	if deleted && !runtime && environment == "" && owner == attemptOwner {
		_, err = tx.ExecContext(ctx, `UPDATE runtime_creation_attempts SET state='failed',updated_at=now() WHERE vm_id=$1`, id)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if runtime || environment != "" || owner != attemptOwner || !associated || !pending {
		_, err = tx.ExecContext(ctx, `UPDATE runtime_creation_attempts SET state='uncertain',updated_at=now() WHERE vm_id=$1`, id)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='error',completed_at=now() WHERE id=$1 AND user_id=$2 AND status='pending'`, task, owner); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM model_api_keys WHERE virtualmachine_id=$1 AND user_id=$2`, id, owner); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE virtualmachines SET is_recycled=true,deleted_at=now() WHERE id=$1 AND user_id=$2 AND COALESCE(environment_id,'')='' AND deleted_at IS NULL`, id, owner); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_creation_attempts SET state='failed',updated_at=now() WHERE vm_id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}
