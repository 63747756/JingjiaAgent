package runtimeadapter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/DATA-DOG/go-sqlmock"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestFinalTextReconciliation(t *testing.T) {
	for _, r := range []struct{ stream, final, want string }{
		{"", "hello", "hello"}, {"hello", "hello", ""}, {"hello\n", "hello", ""},
		{"previous answer\nnext", "next answer", " answer"}, {"中文", "中文回答", "回答"},
		{"unrelated", "hello", "hello"}, {"", "", ""},
	} {
		if got := finalTextTail(r.stream, r.final); got != r.want {
			t.Fatalf("tail=%q want=%q", got, r.want)
		}
	}
}
func TestActivityContract(t *testing.T) {
	updates, err := activityUpdates(`{"kind":"tool_call","id":"tool-1","name":"read","toolKind":"read","status":"completed","input":{"path":"中文.txt"}}`)
	if err != nil || len(updates) != 2 || updates[0]["sessionUpdate"] != "tool_call" || updates[1]["sessionUpdate"] != "tool_call_update" {
		t.Fatalf("tool jingjiaagent:lifecycle: %v", err)
	}
	if _, err = activityUpdates(`{"kind":"tool_result","output":"lost ID"}`); err == nil {
		t.Fatal("accepted result without correlation ID")
	}
	if _, err = activityUpdates(`not json`); err == nil {
		t.Fatal("accepted malformed activity")
	}
	updates, err = activityUpdates(`{"kind":"tool_result","id":"tool-1","ok":false,"error":"denied"}`)
	if err != nil || updates[0]["status"] != "failed" {
		t.Fatal("lost tool failure")
	}
	updates, err = activityUpdates(`{"kind":"usage","scope":"step","inputTokens":12}`)
	if err != nil || len(updates) != 0 {
		t.Fatal("step tokens misreported as current context occupancy")
	}
}

func TestNativeFinalAlwaysPersistsCompleteAnswer(t *testing.T) {
	for _, test := range []struct {
		name, streamed, final, tail string
	}{
		{"already streamed", "中文回答", "中文回答\n", ""},
		{"partial stream", "中文", "中文回答", "回答"},
		{"no deltas", "", "中文回答", "中文回答"},
		{"empty completion", "", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger, mock := recoveryLedger(t)
			command := Command{ID: "command", TaskID: "task", Lease: "lease", Turn: 3}
			event := &v2.RunEvent{Seq: 5, Kind: v2.RunEventKind_RUN_EVENT_KIND_AGENT_MESSAGE, Text: test.final, CreatedAt: timestamppb.New(time.Unix(100, 0))}
			rows := sqlmock.NewRows([]string{"chunk"})
			if test.streamed != "" {
				rows.AddRow(mustJSON(acpChunk(event, textUpdate("agent_message_chunk", test.streamed))))
			}
			mock.ExpectQuery("SELECT chunk FROM runtime_events").WithArgs(command.ID).WillReturnRows(rows)
			expectAppend := func(key string, chunk taskflow.TaskChunk) {
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT COALESCE\\(lease_token").WithArgs(command.ID, command.Lease).WillReturnRows(sqlmock.NewRows([]string{"valid"}).AddRow(true))
				mock.ExpectExec("INSERT INTO runtime_events.*ON CONFLICT\\(command_id,source_key\\) DO NOTHING").WithArgs(command.TaskID, command.ID, key, command.Turn, string(mustJSON(chunk))).WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit()
			}
			if test.tail != "" {
				expectAppend("5", acpChunk(event, textUpdate("agent_message_chunk", test.tail)))
			}
			expectAppend("5/final", taskflow.TaskChunk{Event: "agent-final", Timestamp: event.CreatedAt.AsTime().UnixNano(), Data: mustJSON(map[string]string{"content": test.final})})
			if err := (&Client{ledger: ledger}).ingestEvent(context.Background(), command, event); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDurableStreamReplayDoesNotDuplicateFinal(t *testing.T) {
	l := testLedger(t)
	req := stageFixture(t, l)
	ctx := context.Background()
	cmd, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{ledger: l}
	events := []*v2.RunEvent{
		{Seq: 1, Kind: v2.RunEventKind_RUN_EVENT_KIND_AGENT_ACTIVITY, PayloadJson: `{"kind":"text_delta","text":"中文"}`},
		{Seq: 2, Kind: v2.RunEventKind_RUN_EVENT_KIND_AGENT_ACTIVITY, PayloadJson: `{"kind":"tool_call","id":"r1","name":"read","toolKind":"read","status":"in_progress"}`},
		{Seq: 3, Kind: v2.RunEventKind_RUN_EVENT_KIND_AGENT_ACTIVITY, PayloadJson: `{"kind":"text_delta","text":"回答"}`},
		{Seq: 4, Kind: v2.RunEventKind_RUN_EVENT_KIND_AGENT_ACTIVITY, PayloadJson: `{"kind":"tool_result","id":"r1","ok":true,"output":"file content"}`},
		{Seq: 5, Kind: v2.RunEventKind_RUN_EVENT_KIND_AGENT_MESSAGE, Text: "中文回答\n"},
	}
	for replay := 0; replay < 2; replay++ {
		for _, event := range events {
			if err = c.ingestEvent(ctx, cmd, event); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := l.Events(ctx, req.ID.String(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("unexpected durable event count %d", len(got))
	}
	var text string
	invalidations := 0
	finals := 0
	for _, chunk := range got {
		if chunk.Event == "agent-final" {
			finals++
			var final struct {
				Content string `json:"content"`
			}
			if err = json.Unmarshal(chunk.Data, &final); err != nil || final.Content != "中文回答\n" {
				t.Fatalf("lost complete final answer: %q %v", final.Content, err)
			}
			continue
		}
		if chunk.Event == "task-event" && chunk.Kind == "repo_file_change" {
			invalidations++
			continue
		}
		if chunk.Event != "task-running" || chunk.Kind != "acp_event" {
			t.Fatal("chunk is incompatible with the Web message consumer")
		}
		var e struct {
			Update struct {
				Kind    string          `json:"sessionUpdate"`
				Content json.RawMessage `json:"content"`
			} `json:"update"`
		}
		if err = json.Unmarshal(chunk.Data, &e); err != nil {
			t.Fatal(err)
		}
		if e.Update.Kind == "agent_message_chunk" {
			var content struct {
				Text string `json:"text"`
			}
			if err = json.Unmarshal(e.Update.Content, &content); err != nil {
				t.Fatal(err)
			}
			text += content.Text
		}
	}
	if invalidations != 1 {
		t.Fatal("repository invalidation was duplicated on replay")
	}
	if finals != 1 {
		t.Fatalf("final marker duplicated or lost on replay: %d", finals)
	}
	if text != "中文回答" {
		t.Fatalf("duplicated/missing stream: %q", text)
	}
}
