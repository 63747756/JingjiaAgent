package runtimeadapter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestQuestionSelectionsRespectOriginalQuestionOrder(t *testing.T) {
	var native nativeInteraction
	if err := json.Unmarshal([]byte(`{"questions":[{"question":"第二项","multiple":true,"custom":false,"options":[{"label":"A"},{"label":"B"}]},{"question":"第一项","custom":true}]}`), &native); err != nil {
		t.Fatal(err)
	}
	answers, err := orderedAnswers(taskflow.AskUserQuestionResponse{AnswersJson: `{"第一项":"自定义文本","第二项":["B","A"]}`}, native)
	if err != nil || string(mustJSON(answers)) != `[["B","A"],["自定义文本"]]` {
		t.Fatalf("answers/order: %v %v", answers, err)
	}
	for _, bad := range []string{`{"第一项":"","第二项":["A"]}`, `{"第一项":"自定义","第二项":["非法"]}`, `{"第一项":"自定义","第二项":["A","A"]}`, `{"第一项":["A","B"],"第二项":["A"]}`, `{"第一项":"自定义"}`, `null`} {
		if _, err := orderedAnswers(taskflow.AskUserQuestionResponse{AnswersJson: bad}, native); err == nil {
			t.Errorf("invalid selection accepted: %s", bad)
		}
	}
}

func TestNativeInteractionHasDurableTaskRunCorrelation(t *testing.T) {
	c, task := workerFixture(t, &runTestServer{status: v2.RunStatus_RUN_STATUS_RUNNING})
	ctx := context.Background()
	if err := c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	var command Command
	command.TaskID = task.ID.String()
	if err := c.ledger.db.QueryRow(`SELECT id,environment_id,run_id,turn FROM runtime_commands WHERE task_id=$1 AND operation='task'`, task.ID).Scan(&command.ID, &command.EnvironmentID, &command.RunID, &command.Turn); err != nil {
		t.Fatal(err)
	}
	ready(t, c)
	command, err := c.ledger.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"kind": "tool_call", "id": "que_native_proof", "name": "question", "input": map[string]any{"request_id": "que_native_proof", "run_id": command.RunID, "session_id": "ses_proof", "request_kind": "question", "questions": []any{map[string]any{"question": "选择", "custom": false, "options": []any{map[string]string{"label": "A"}}}}}}
	if err = c.ingestEvent(ctx, command, &v2.RunEvent{Seq: 1, Kind: v2.RunEventKind_RUN_EVENT_KIND_AGENT_ACTIVITY, PayloadJson: string(mustJSON(payload))}); err != nil {
		t.Fatal(err)
	}
	if err = c.ledger.UpdateCommand(ctx, command, "running", command.RunID, 1); err != nil {
		t.Fatal(err)
	}
	request := taskflow.AskUserQuestionResponse{TaskId: task.ID.String(), RequestId: "que_native_proof", AnswersJson: `{"选择":"A"}`}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err = c.TaskManager().AskUserQuestion(short, request); err == nil {
		t.Fatal("reply acknowledged before Worker processed it")
	}
	var count int
	if err = c.ledger.db.QueryRow(`SELECT count(*) FROM runtime_commands WHERE task_id=$1 AND operation='interaction'`, task.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("durable reply admission: %d %v", count, err)
	}
	if err = c.TaskManager().AskUserQuestion(short, taskflow.AskUserQuestionResponse{TaskId: task.ID.String(), RequestId: "que_unknown", AnswersJson: `{"选择":"A"}`}); err == nil {
		t.Fatal("unrelated question accepted")
	}
	rows, err := c.ledger.db.Query(`SELECT payload,id FROM runtime_commands WHERE task_id=$1 AND operation='interaction'`, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var answer interactionCommand
	if rows.Next() {
		var sealed []byte
		var id string
		if err = rows.Scan(&sealed, &id); err != nil {
			t.Fatal(err)
		}
		plain, err := c.ledger.open(id, sealed)
		if err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(plain, &answer) != nil || answer.Target != command.ID || answer.Turn != command.Turn || answer.Native.RunID != command.RunID {
			t.Fatal("reply lost original task/Run/turn mapping")
		}
	}
	rows.Close()
	if _, err = c.ledger.db.Exec(`UPDATE runtime_commands SET available_at=now()+interval '1 day' WHERE task_id=$1 AND operation='task'`, task.ID); err != nil {
		t.Fatal(err)
	}
	reply, err := c.ledger.Claim(ctx)
	if err != nil || reply.Operation != "interaction" {
		t.Fatalf("reply claim: %v %v", reply, err)
	}
	stale := reply
	stale.Lease = task.ID.String()
	if err = c.finishInteraction(ctx, stale, answer); err == nil {
		t.Fatal("stale Worker acknowledged question response")
	}
	if err = c.finishInteraction(ctx, reply, answer); err != nil {
		t.Fatal(err)
	}
	if err = c.TaskManager().AskUserQuestion(ctx, request); err != nil {
		t.Fatalf("completed replay required native server: %v", err)
	}
	var persistedTurn int
	if err = c.ledger.db.QueryRow(`SELECT turn FROM runtime_events WHERE command_id=$1 AND source_key='reply-question'`, reply.ID).Scan(&persistedTurn); err != nil || persistedTurn != command.Turn {
		t.Fatalf("question reply landed in wrong history turn: %d %v", persistedTurn, err)
	}
}
