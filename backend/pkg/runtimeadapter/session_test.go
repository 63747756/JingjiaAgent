package runtimeadapter

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSessionMappingFencesStaleWorkersAndRetainsRunHistory(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	task := stageFixture(t, l)
	prepare, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.UpdateCommand(ctx, prepare, "complete", "prepare-run", 0); err != nil {
		t.Fatal(err)
	}
	claim := func(turn int) Command {
		t.Helper()
		if err := l.Enqueue(ctx, task.VMID, task.ID.String(), "task", turn, task); err != nil {
			t.Fatal(err)
		}
		command, err := l.Claim(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return command
	}
	first := claim(1)
	if err = l.SaveSession(ctx, first, "opencode", "ses_first"); err != nil {
		t.Fatal(err)
	}
	if err = l.UpdateCommand(ctx, first, "complete", "run-first", 0); err != nil {
		t.Fatal(err)
	}
	stale := claim(2)
	if _, err = l.db.ExecContext(ctx, `UPDATE runtime_commands SET lease_until=now()-interval '1 second' WHERE id=$1`, stale.ID); err != nil {
		t.Fatal(err)
	}
	current, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.SaveSession(ctx, stale, "opencode", "ses_stale"); err == nil {
		t.Fatal("stale worker changed the provider session mapping")
	}
	var id string
	if err = l.db.QueryRowContext(ctx, `SELECT session_id FROM runtime_task_sessions WHERE task_id=$1`, task.ID).Scan(&id); err != nil || id != "ses_first" {
		t.Fatalf("mapping changed before an authorized commit: %s, %v", id, err)
	}
	if err = l.SaveSession(ctx, current, "opencode", "ses_second"); err != nil {
		t.Fatal(err)
	}
	if err = l.db.QueryRowContext(ctx, `SELECT session_id FROM runtime_task_sessions WHERE task_id=$1`, task.ID).Scan(&id); err != nil || id != "ses_second" {
		t.Fatalf("current session mapping missing: %s, %v", id, err)
	}
	for command, expected := range map[string]string{first.ID: "ses_first", current.ID: "ses_second"} {
		var data []byte
		if err = l.db.QueryRowContext(ctx, `SELECT result FROM runtime_commands WHERE id=$1`, command).Scan(&data); err != nil {
			t.Fatal(err)
		}
		var result map[string]string
		if err = json.Unmarshal(data, &result); err != nil || result["provider"] != "opencode" || result["session_id"] != expected {
			t.Fatalf("per-Run session mapping missing: %v", err)
		}
	}
}
