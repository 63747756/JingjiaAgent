package runtimeadapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func TestWorkContextResetWithoutSandboxAndRetry(t *testing.T) {
	l, c, profile := workRuntimeLedger(t)
	task := workRuntimeTask(t, l, profile)
	ctx := context.Background()
	if _, err := l.db.Exec(`UPDATE runtime_commands SET state='complete' WHERE task_id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`UPDATE runtime_environments SET state='deleted' WHERE id=$1`, task.VMID); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Minute)
	if err := c.RecordContextUsage(ctx, task.VMID, 128000, 12000, before); err != nil {
		t.Fatal(err)
	}
	first := taskflow.RestartTaskReq{ID: task.ID, RequestId: "first-reset"}
	for _, r := range []taskflow.RestartTaskReq{first, first, {ID: task.ID, RequestId: "second-reset"}} {
		resp, err := c.TaskManager().Restart(ctx, r)
		if err != nil || !resp.Success {
			t.Fatalf("reset %+v: %v", resp, err)
		}
	}
	var count int
	if err := l.db.QueryRow(`SELECT count(*) FROM runtime_commands WHERE task_id=$1 AND operation='restart'`, task.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("duplicate reset %d %v", count, err)
	}
	id, _, err := c.contextBoundary(ctx, task.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.TaskManager().Restart(ctx, first); err != nil {
		t.Fatal(err)
	}
	after, _, _ := c.contextBoundary(ctx, task.ID.String())
	if after != id {
		t.Fatal("retry moved reset backwards")
	}
	if err := c.RecordContextUsage(ctx, task.VMID, 128000, 20000, before); err != nil {
		t.Fatal(err)
	}
	usage, err := c.TaskContextUsage(ctx, task.ID)
	if err != nil || usage.Used == nil || *usage.Used != 0 || usage.Estimated {
		t.Fatalf("stale response replaced reset: %+v %v", usage, err)
	}
	if err := c.RecordContextUsage(ctx, task.VMID, 128000, 3500, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	usage, err = c.TaskContextUsage(ctx, task.ID)
	if err != nil || *usage.Used != 3500 || !usage.Estimated {
		t.Fatalf("new usage lost %+v %v", usage, err)
	}
}

func TestContextResetRejectsExecutingAndQueuedRounds(t *testing.T) {
	l, c, profile := workRuntimeLedger(t)
	task := workRuntimeTask(t, l, profile)
	for _, state := range []string{"pending", "waiting", "running", "blocked"} {
		if _, err := l.db.Exec(`UPDATE runtime_commands SET state=$2 WHERE task_id=$1`, task.ID, state); err != nil {
			t.Fatal(err)
		}
		if _, err := c.TaskManager().Restart(context.Background(), taskflow.RestartTaskReq{ID: task.ID, RequestId: uuid.NewString()}); err == nil {
			t.Fatalf("accepted reset while %s", state)
		}
	}
}

func TestContextRecoveryRespectsResetBoundary(t *testing.T) {
	l, c, profile := workRuntimeLedger(t)
	task := workRuntimeTask(t, l, profile)
	ctx := context.Background()
	task.PromptContext = taskflow.PromptContext{Version: taskflow.PromptContextVersion, OutputRequirements: "默认输出中文文档"}
	data := mustJSON(task)
	sealed, err := l.seal(task.ID.String(), data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.db.Exec(`UPDATE runtime_task_intents SET payload=$2,payload_hash=$3 WHERE task_id=$1`, task.ID, sealed, hash(data)); err != nil {
		t.Fatal(err)
	}
	// Include a normal completed turn and its durable public input.
	var id string
	if err := l.db.QueryRow(`UPDATE runtime_commands SET operation='task',state='complete',turn=1 WHERE task_id=$1 RETURNING id`, task.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	chunk := taskflow.TaskChunk{Event: "user-input", Data: mustJSON(map[string]any{"content": []byte("secret-before-reset")})}
	if _, err := l.db.Exec(`INSERT INTO runtime_events(task_id,command_id,source_key,turn,chunk) VALUES($1,$2,'user-input',1,$3)`, task.ID, id, string(mustJSON(chunk))); err != nil {
		t.Fatal(err)
	}
	if _, err := c.TaskManager().Restart(ctx, taskflow.RestartTaskReq{ID: task.ID, RequestId: "reset"}); err != nil {
		t.Fatal(err)
	}
	next, err := l.Intent(ctx, task.ID.String())
	if err != nil || next.ContextStartTurn != 2 || next.Recovered {
		t.Fatalf("reset did not persist the next native session boundary: %+v %v", next, err)
	}
	next.Text = "本轮改成表格"
	if prompt := executionPrompt(next, 2); !strings.Contains(prompt, task.PromptContext.OutputRequirements) || !strings.HasSuffix(prompt, next.Text) || strings.Contains(prompt, "secret-before-reset") {
		t.Fatal("new session lost defaults or restored cleared dialogue")
	}
	if prompt := executionPrompt(next, 3); prompt != next.Text {
		t.Fatal("task defaults were replayed beyond the first new-session turn")
	}
	next.Recovered = true
	cmd := Command{TaskID: task.ID.String(), Turn: 2}
	if err := c.freezeRecoveryContext(ctx, &cmd, &next); err != nil {
		t.Fatal(err)
	}
	if next.RecoveryContext != "" {
		t.Fatal("reset rehydrated old dialogue")
	}
	// Replaying the receipt is safe even when the original environment is gone.
	resp, found, err := c.TaskManager().(taskflow.RestartResumer).ResumeRestart(ctx, taskflow.RestartTaskReq{ID: task.ID, RequestId: "reset"})
	if err != nil || !found || !resp.Success {
		t.Fatalf("resume reset: %+v %v", resp, err)
	}
	var stored []byte
	if err = l.db.QueryRow(`SELECT context_usage FROM runtime_task_intents WHERE task_id=$1`, task.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var usage taskflow.ContextUsage
	if json.Unmarshal(stored, &usage) != nil || usage.Used == nil || *usage.Used != 0 {
		t.Fatal("reset not persisted")
	}
}

func TestContextUsageKeepsLatestRequestRatherThanTotal(t *testing.T) {
	l := testLedger(t)
	task := stageFixture(t, l)
	c := &Client{ledger: l}
	ctx := context.Background()
	start := time.Now().Add(-time.Minute)
	for _, r := range []struct {
		offset time.Duration
		used   uint64
	}{{0, 12000}, {time.Second, 5000}, {0, 12000}} {
		if err := c.RecordContextUsage(ctx, task.VMID, 128000, r.used, start.Add(r.offset)); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := c.TaskContextUsage(ctx, task.ID)
	if err != nil || *usage.Used != 5000 {
		t.Fatalf("wrong request scope %+v %v", usage, err)
	}
}
