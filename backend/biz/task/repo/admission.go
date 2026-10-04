package repo

import (
	"context"
	"errors"

	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/pkg/entx"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
)

// CompleteCreateWithAdmission binds the product VM and commits the complete
// encrypted runtime request together. The preparation Worker sees only commit.
func (t *TaskRepo) CompleteCreateWithAdmission(ctx context.Context, vmID string, admit func(context.Context, *db.Tx) (*taskflow.VirtualMachine, error)) (*taskflow.VirtualMachine, error) {
	var result *taskflow.VirtualMachine
	err := entx.WithTx2(ctx, t.db, func(tx *db.Tx) error {
		var locked string
		rows, err := tx.QueryContext(ctx, `SELECT id FROM virtualmachines WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, vmID)
		if err != nil {
			return err
		}
		if !rows.Next() {
			rows.Close()
			return errors.New("prepared VM unavailable")
		}
		err = rows.Scan(&locked)
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `SELECT state FROM runtime_creation_attempts WHERE vm_id=$1 FOR UPDATE`, vmID)
		if err != nil {
			return err
		}
		var state string
		if !rows.Next() {
			rows.Close()
			return errors.New("prepared admission journal unavailable")
		}
		err = rows.Scan(&state)
		rows.Close()
		if err != nil {
			return err
		}
		if state != "pending" && state != "admitted" {
			return errors.New("prepared admission is closed")
		}
		result, err = admit(ctx, tx)
		if err != nil {
			return err
		}
		if result == nil || result.ID != vmID || result.EnvironmentID == "" {
			return errors.New("invalid admitted VM mapping")
		}
		up := tx.VirtualMachine.UpdateOneID(vmID).SetEnvironmentID(result.EnvironmentID)
		if result.AccessToken != "" {
			up.SetAccessToken(result.AccessToken)
		}
		if err = up.Exec(ctx); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE runtime_creation_attempts SET state='admitted',updated_at=now() WHERE vm_id=$1 AND state='pending'`, vmID)
		return err
	})
	return result, err
}

// A finished HTTP request that did not commit admission need not wait the crash
// grace period. A committed attempt is never downgraded, including lost commits.
func (t *TaskRepo) ExpirePreparedAdmission(ctx context.Context, vmID string) error {
	_, err := t.db.ExecContext(ctx, `UPDATE runtime_creation_attempts SET expires_at=now(),updated_at=now() WHERE vm_id=$1 AND state='pending'`, vmID)
	return err
}
