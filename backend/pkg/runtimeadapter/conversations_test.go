package runtimeadapter

import (
	"context"
	"testing"
	"time"

	"github.com/chaitin/MonkeyCode/backend/pkg/clickhouse"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/chaitin/MonkeyCode/backend/pkg/tasklog"
	"github.com/google/uuid"
)

func TestRejectedAdmissionHistoryDoesNotUseLegacyLogs(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := l.db.ExecContext(ctx, `INSERT INTO runtime_creation_attempts(vm_id,owner_id,task_id,state) VALUES($1,$2,$3,'failed')`, "agent_"+uuid.NewString(), uuid.New(), id); err != nil {
		t.Fatal(err)
	}
	c := &Client{ledger: l}
	p, managed, err := c.ResolveHistory(ctx, id)
	if err != nil || !managed || p == nil {
		t.Fatalf("rejected admission not resolved: %v", err)
	}
	turns, err := p.QueryTurns(ctx, id, time.Time{}, tasklog.QueryTurnsOpts{})
	if err != nil || turns.HasMore || len(turns.Chunks) != 0 {
		t.Fatalf("failed task must have empty durable history: %+v %v", turns, err)
	}
	if p, managed, err := c.ResolveHistory(ctx, uuid.New()); err != nil || managed || p != nil {
		t.Fatalf("unmanaged task incorrectly claimed: %v", err)
	}
}

func TestRuntimeTeamConversationsScopePagingAndRecycle(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	req := stageFixture(t, l)
	prepare, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.UpdateCommand(ctx, prepare, "complete", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := l.Enqueue(ctx, req.VMID, req.ID.String(), "task", 0, req); err != nil {
		t.Fatal(err)
	}
	command, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture constructs exactly two input events with tied timestamps.
	// Discard the normal transactional acceptance echo before inserting them.
	if _, err := l.db.ExecContext(ctx, `DELETE FROM runtime_events WHERE command_id=$1 AND source_key='user-input'`, command.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, key := range []string{"first", "second"} {
		input := taskflow.TaskChunk{Event: "user-input", Timestamp: now.UnixNano(), Data: mustJSON(map[string]any{"content": []byte(key)})}
		if err := l.Append(ctx, command, key, input); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.SetEnvironment(ctx, req.VMID, "", "", "deleted"); err != nil {
		t.Fatal(err)
	}
	c := &Client{ledger: l}
	q := clickhouse.TeamConversationListQuery{TaskIDs: []string{req.ID.String()}, Limit: 1}
	first, err := c.QueryTeamConversations(ctx, q)
	if err != nil || len(first.Rows) != 1 || !first.HasNextPage {
		t.Fatalf("first page: %+v %v", first, err)
	}
	q.Cursor = first.NextCursor
	second, err := c.QueryTeamConversations(ctx, q)
	if err != nil || len(second.Rows) != 1 || second.HasNextPage || first.Rows[0].MsgSeqStart == second.Rows[0].MsgSeqStart {
		t.Fatalf("timestamp tie lost/duplicated a conversation: %+v %v", second, err)
	}
	other, err := c.QueryTeamConversations(ctx, clickhouse.TeamConversationListQuery{TaskIDs: []string{uuid.NewString()}})
	if err != nil || len(other.Rows) != 0 {
		t.Fatalf("task scope leaked conversations: %+v %v", other, err)
	}
	if _, err := c.QueryTeamConversations(ctx, clickhouse.TeamConversationListQuery{TaskIDs: q.TaskIDs, Cursor: "bad cursor"}); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	stats, err := c.QueryTeamConversationStats(ctx, clickhouse.TeamConversationQuery{TaskIDs: q.TaskIDs, Start7d: now.AddDate(0, 0, -7), TodayStart: now.Add(-time.Hour), TrendStart: now.AddDate(0, 0, -7), End: time.Now().Add(time.Minute)})
	if err != nil || stats.Total != 2 || stats.Count7d != 2 || stats.CountToday != 2 || len(stats.DailyCreated) != 1 || stats.DailyCreated[0].Count != 2 {
		t.Fatalf("conversation stats: %+v %v", stats, err)
	}
}
