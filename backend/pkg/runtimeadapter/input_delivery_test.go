package runtimeadapter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

func TestInputEchoCommitsWithAcceptanceBeforeWorker(t *testing.T) {
	l := testLedger(t)
	req := stageFixture(t, l)
	req.ClientMessageID = uuid.NewString()
	ctx := context.Background()
	if err := l.Enqueue(ctx, req.VMID, req.ID.String(), "task", 1, req); err != nil {
		t.Fatal(err)
	}
	chunks, err := l.Events(ctx, req.ID.String(), 0)
	if err != nil || len(chunks) != 1 || chunks[0].Event != "user-input" {
		t.Fatalf("acceptance has no durable echo: %v", err)
	}
	var input struct {
		ClientMessageID string `json:"client_message_id"`
		Content         []byte `json:"content"`
	}
	if err = json.Unmarshal(chunks[0].Data, &input); err != nil || input.ClientMessageID != req.ClientMessageID || string(input.Content) != req.Text {
		t.Fatal("input correlation lost")
	}
	if err = l.Enqueue(ctx, req.VMID, req.ID.String(), "task", 1, req); err != nil {
		t.Fatal(err)
	}
	replayed, err := l.Events(ctx, req.ID.String(), 0)
	if err != nil || len(replayed) != 1 || replayed[0].Timestamp != chunks[0].Timestamp {
		t.Fatal("acceptance retry duplicated input")
	}
	// A rolled-back submission must not appear accepted to reconnecting clients.
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.enqueueTx(ctx, tx, req.VMID, req.ID.String(), "task", 2, mustJSON(req)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	replayed, err = l.Events(ctx, req.ID.String(), 0)
	if err != nil || len(replayed) != 1 {
		t.Fatal("rolled back input became visible")
	}
}

func TestActiveRunIsPolledPromptlyAndFallbackDoesNotDuplicateInput(t *testing.T) {
	server := &runTestServer{status: v2.RunStatus_RUN_STATUS_RUNNING}
	c, req := workerFixture(t, server)
	ctx := context.Background()
	if err := c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	chunks, err := c.ledger.Events(ctx, req.ID.String(), 0)
	if err != nil || len(chunks) != 1 {
		t.Fatal("worker duplicated accepted input")
	}
	var wait float64
	if err = c.ledger.db.QueryRowContext(ctx, `SELECT extract(epoch FROM available_at-updated_at) FROM runtime_commands WHERE task_id=$1 AND operation='task'`, req.ID).Scan(&wait); err != nil {
		t.Fatal(err)
	}
	if wait < 0.09 || wait > 0.11 {
		t.Fatalf("active run deferred %f seconds", wait)
	}
	// Existing pre-upgrade commands still retain the worker's input fallback.
	if _, err = c.ledger.db.ExecContext(ctx, `DELETE FROM runtime_events WHERE task_id=$1`, req.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ledger.db.ExecContext(ctx, `UPDATE runtime_commands SET available_at=now() WHERE task_id=$1 AND operation='task'`, req.ID); err != nil {
		t.Fatal(err)
	}
	if err = c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	chunks, err = c.ledger.Events(ctx, req.ID.String(), 0)
	if err != nil || len(chunks) != 1 || chunks[0].Timestamp < time.Now().Add(-time.Minute).UnixNano() {
		t.Fatal("old command fallback failed")
	}
}
