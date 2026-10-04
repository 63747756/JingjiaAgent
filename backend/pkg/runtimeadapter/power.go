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
	if state == "stopping" || state == "deleted" {
		return errors.New("runtime environment is stopping or unavailable")
	}
	if sandbox == "" {
		return errors.New("runtime environment has no sandbox")
	}
	if target == "hibernated" {
		_, err = n.sandboxes.StopSandbox(ctx, connect.NewRequest(&v2.StopSandboxRequest{SandboxId: sandbox}))
	} else {
		_, err = n.sandboxes.ResumeSandbox(ctx, connect.NewRequest(&v2.ResumeSandboxRequest{SandboxId: sandbox}))
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_environments SET state=$2 WHERE id=$1`, e.ID, target); err != nil {
		return err
	}
	return tx.Commit()
}
