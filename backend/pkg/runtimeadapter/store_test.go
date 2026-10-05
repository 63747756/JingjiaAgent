package runtimeadapter

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/63747756/jingjiaagent/backend/pkg/tasklog"
	"github.com/google/uuid"
)

func testLedger(t *testing.T) *Ledger {
	t.Helper()
	dsn := os.Getenv("JINGJIAAGENT_RUNTIME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JINGJIAAGENT_RUNTIME_TEST_DATABASE_URL to an isolated PostgreSQL test database")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "runtime_test_" + uuid.NewString()
	schema = "\"" + schema + "\""
	if _, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); _ = admin.Close() })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migration, err := os.ReadFile(filepath.Join("..", "..", "migration", "000026_remote_runtime.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	controls, err := os.ReadFile(filepath.Join("..", "..", "migration", "000027_runtime_controls.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(controls)); err != nil {
		t.Fatal(err)
	}
	previews, err := os.ReadFile(filepath.Join("..", "..", "migration", "000028_runtime_previews.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(previews)); err != nil {
		t.Fatal(err)
	}
	nodes, err := os.ReadFile(filepath.Join("..", "..", "migration", "000029_runtime_nodes.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(nodes)); err != nil {
		t.Fatal(err)
	}
	capacity, err := os.ReadFile(filepath.Join("..", "..", "migration", "000030_runtime_capacity.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(capacity)); err != nil {
		t.Fatal(err)
	}
	creation, err := os.ReadFile(filepath.Join("..", "..", "migration", "000031_runtime_creation_attempts.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(creation)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `CREATE TABLE tasks(id uuid PRIMARY KEY,status text,completed_at timestamptz)`); err != nil {
		t.Fatal(err)
	}
	ledger, err := NewLedger(db, bytes.Repeat([]byte{17}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}
func TestPayloadAuthenticatedEncryption(t *testing.T) {
	db := new(sql.DB)
	ledger, err := NewLedger(db, bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte(`{"api_key":"unique-secret-for-test"}`)
	a, err := ledger.seal("command-1", secret)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ledger.seal("command-1", secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) || bytes.Contains(a, secret) {
		t.Fatal("payload is not randomized encrypted ciphertext")
	}
	decoded, err := ledger.open("command-1", a)
	if err != nil || !bytes.Equal(decoded, secret) {
		t.Fatal("payload round trip failed")
	}
	if _, err = ledger.open("command-2", a); err == nil {
		t.Fatal("ciphertext accepted under another command")
	}
	a[len(a)-1] ^= 1
	if _, err = ledger.open("command-1", a); err == nil {
		t.Fatal("modified ciphertext accepted")
	}
	if _, err = NewLedger(db, []byte("short")); err == nil {
		t.Fatal("short encryption key accepted")
	}
}
func stageFixture(t *testing.T, l *Ledger) taskflow.CreateTaskReq {
	t.Helper()
	ctx := context.Background()
	task := uuid.New()
	env := "agent_" + uuid.NewString()
	req := taskflow.CreateTaskReq{ID: task, VMID: env, Text: "中文任务", CodingAgent: taskflow.CodingAgentOpenCode, LLM: taskflow.LLM{ApiKey: "ledger-secret"}}
	if err := l.SaveEnvironment(ctx, Environment{ID: env, OwnerID: uuid.NewString(), NodeID: "node", Request: taskflow.CreateVirtualMachineReq{ID: env}}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.ExecContext(ctx, `INSERT INTO tasks(id,status) VALUES($1,'pending')`, task); err != nil {
		t.Fatal(err)
	}
	if err := l.Stage(ctx, req); err != nil {
		t.Fatal(err)
	}
	return req
}
func TestLedgerAdmissionLeaseEventsAndHistory(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	req := stageFixture(t, l)
	if err := l.Stage(ctx, req); err != nil {
		t.Fatal(err)
	}
	changed := req
	changed.Text = "changed"
	if l.Stage(ctx, changed) == nil {
		t.Fatal("conflicting submission accepted")
	}
	var count int
	var payload []byte
	if err := l.db.QueryRowContext(ctx, `SELECT count(*) FROM runtime_commands`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate prepare jobs: %d, %v", count, err)
	}
	if err := l.db.QueryRowContext(ctx, `SELECT payload FROM runtime_task_intents WHERE task_id=$1`, req.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("ledger-secret")) {
		t.Fatal("credentials stored in plaintext")
	}
	first, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Claim(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("same command claimed twice: %v", err)
	}
	if _, err = l.db.ExecContext(ctx, `UPDATE runtime_commands SET lease_until=now()-interval '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Lease == second.Lease {
		t.Fatal("expired command was not reclaimed with a new lease")
	}
	if l.UpdateCommand(ctx, first, "complete", "", 0) == nil {
		t.Fatal("stale worker committed command")
	}
	beforeInputs, err := (&historyProvider{ledger: l}).QueryUserInputs(ctx, req.ID, time.Time{}, "", 10)
	if err != nil || !beforeInputs.Authoritative || len(beforeInputs.Entries) != 0 {
		t.Fatalf("pre-admission history must suppress synthetic input: %v", err)
	}
	event := taskflow.TaskChunk{Event: "user-input", Timestamp: time.Now().UnixNano(), Data: mustJSON(map[string]any{"content": []byte(req.Text)})}
	if err = l.Append(ctx, second, "user-input", event); err != nil {
		t.Fatal(err)
	}
	if err = l.Append(ctx, second, "user-input", event); err != nil {
		t.Fatal(err)
	}
	events, err := l.Events(ctx, req.ID.String(), 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("event dedupe failed: %v %d", err, len(events))
	}
	history, watermark, err := snapshot(ctx, l, req.ID)
	if err != nil || len(history.Entries) != 1 || watermark != events[0].Seq {
		t.Fatalf("history watermark mismatch: %v", err)
	}
	// Late ingestion with an earlier provider timestamp must survive reconnect.
	if err = l.Append(ctx, second, "late", taskflow.TaskChunk{Event: "task-event", Timestamp: event.Timestamp - 1000, Data: []byte("late")}); err != nil {
		t.Fatal(err)
	}
	remaining, err := l.Events(ctx, req.ID.String(), int64(watermark))
	if err != nil || len(remaining) != 1 {
		t.Fatalf("late event lost: %v", err)
	}
	inputs, err := (&historyProvider{ledger: l}).QueryUserInputs(ctx, req.ID, time.Time{}, "", 10)
	if err != nil || len(inputs.Entries) != 1 || !inputs.Authoritative {
		t.Fatalf("user input history failed: %v", err)
	}
	if err = l.UpdateCommand(ctx, second, "complete", "run", 2); err != nil {
		t.Fatal(err)
	}
	if err = l.Enqueue(ctx, req.VMID, req.ID.String(), "task", 1, req); err != nil {
		t.Fatal(err)
	}
	next, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Append(ctx, next, "user-input", event); err != nil {
		t.Fatal(err)
	}
	turns, err := (&historyProvider{ledger: l}).QueryTurns(ctx, req.ID, time.Time{}, tasklog.QueryTurnsOpts{Limit: 1})
	if err != nil || !turns.HasMore || turns.NextCursor != "1" {
		t.Fatalf("turn paging failed: %+v %v", turns, err)
	}
	older, err := (&historyProvider{ledger: l}).QueryTurns(ctx, req.ID, time.Time{}, tasklog.QueryTurnsOpts{Limit: 1, Cursor: turns.NextCursor})
	if err != nil || older.HasMore || len(older.Chunks) != 2 {
		t.Fatalf("older turn paging failed: %+v %v", older, err)
	}
	var decoded taskflow.CreateTaskReq
	if err = json.Unmarshal(next.Payload, &decoded); err != nil || decoded.Text != req.Text {
		t.Fatal("durable payload lost on reclaim")
	}
}
