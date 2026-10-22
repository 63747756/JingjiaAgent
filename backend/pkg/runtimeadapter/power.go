package runtimeadapter

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"time"
)

// Serialize power changes with admission/recycle on the environment row.
// No pool lock is held while calling the node; the reservation remains intact.
func (v *vmClient) power(ctx context.Context, e Environment, n *Engine, target string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	tx, err := v.c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, sandbox string
	if err = tx.QueryRowContext(ctx, `SELECT state,sandbox_id FROM runtime_environments WHERE id=$1 FOR UPDATE`, e.ID).Scan(&state, &sandbox); err != nil {
		return err
	}
	if state == "stopping" || state == "deleted" || state == "checkpointing" || state == "restoring" {
		return errors.New("runtime environment is stopping or unavailable")
	}
	if sandbox == "" {
		return errors.New("runtime environment has no sandbox")
	}
	if target == "hibernated" {
		var active bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_commands WHERE environment_id=$1 AND state IN ('pending','submitting','unknown','running','waiting'))`, e.ID).Scan(&active); err != nil {
			return err
		}
		if active {
			return errors.New("任务仍在执行或排队，暂不能休眠")
		}

		_, err = n.sandboxes.StopSandbox(ctx, connect.NewRequest(&v2.StopSandboxRequest{SandboxId: sandbox}))
	} else {
		if err = v.c.ledger.reserveEnvironmentTx(ctx, tx, e.ID, false); err != nil {
			return err
		}
		_, err = n.sandboxes.ResumeSandbox(ctx, connect.NewRequest(&v2.ResumeSandboxRequest{SandboxId: sandbox}))
	}
	if err != nil {
		return err
	}
	query := `UPDATE runtime_environments SET state=$2 WHERE id=$1`
	if v.c.ledger.workEnabled && target == "online" {
		query = `UPDATE runtime_environments SET state=$2,checkpoint_id=NULL WHERE id=$1`
	}
	if _, err = tx.ExecContext(ctx, query, e.ID, target); err != nil {
		return err
	}
	return tx.Commit()
}
