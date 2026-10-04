package runtimeadapter

import (
	"context"
	"errors"
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
	var active, uncertain bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_commands WHERE environment_id=$1 AND state IN ('pending','submitting','unknown','running')),
 EXISTS(SELECT 1 FROM runtime_commands WHERE environment_id=$1 AND submission_started)`, id).Scan(&active, &uncertain); err != nil {
		return "", err
	}
	if active {
		return "", errors.New("runtime environment still has active commands; stop before recycle")
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
