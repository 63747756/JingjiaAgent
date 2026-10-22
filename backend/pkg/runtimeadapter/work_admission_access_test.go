package runtimeadapter

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
)

type stagingRevocationGuard struct{ revoked atomic.Bool }

func (g *stagingRevocationGuard) CheckWorkTaskAccess(context.Context, uuid.UUID, uuid.UUID, bool) error {
	if g.revoked.Load() {
		return errcode.ErrForbidden
	}
	return nil
}

func TestGrantRevokedDuringStagingDoesNotAdmitModelRun(t *testing.T) {
	guard := &stagingRevocationGuard{}
	server := &runTestServer{afterAttachments: func() { guard.revoked.Store(true) }}
	c, task := workerFixture(t, server)
	ctx := context.Background()
	env, err := c.ledger.Environment(ctx, task.VMID)
	if err != nil {
		t.Fatal(err)
	}
	env.Request.TaskID = task.ID
	sealed, err := c.ledger.seal(env.ID, mustJSON(env.Request))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ledger.db.ExecContext(ctx, `UPDATE runtime_environments SET payload=$2 WHERE id=$1`, env.ID, sealed); err != nil {
		t.Fatal(err)
	}
	c.workGuard = guard
	if err = c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if !guard.revoked.Load() || server.starts != 0 {
		t.Fatal("model run admitted after permissions changed during input staging")
	}
	var state string
	var submitted bool
	if err = c.ledger.db.QueryRowContext(ctx, `SELECT state,submission_started FROM runtime_commands WHERE task_id=$1 AND operation='task'`, task.ID).Scan(&state, &submitted); err != nil {
		t.Fatal(err)
	}
	if state != "canceled" || submitted {
		t.Fatalf("revoked command crossed admission boundary: %s submitted=%v", state, submitted)
	}
}
