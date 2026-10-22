package runtimeadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func nextModelFixture(t *testing.T) (*Client, taskflow.CreateTaskReq, uuid.UUID) {
	t.Helper()
	c, task, owner, _ := restartBusinessFixture(t)
	if _, err := c.ledger.db.Exec(`ALTER TABLE task_model_switches ADD COLUMN from_model_id uuid`); err != nil {
		t.Fatal(err)
	}
	// Selection must work without a configured/reachable runtime engine.
	c.engines = map[string]*Engine{"node": {}}
	return c, task, owner
}

func selectModelRequest(task taskflow.CreateTaskReq, owner uuid.UUID, requestID, model string) taskflow.NextTurnModelReq {
	return taskflow.NextTurnModelReq{TaskID: task.ID, OwnerID: owner, ModelID: uuid.New(), RequestID: requestID,
		Config: &taskflow.TaskExecutionConfig{LLM: &taskflow.LLM{Model: model, ApiKey: "key-" + model}}}
}

func readSelectedIntent(t *testing.T, c *Client, taskID uuid.UUID) taskflow.CreateTaskReq {
	t.Helper()
	var sealed []byte
	if err := c.ledger.db.QueryRow(`SELECT payload FROM runtime_task_intents WHERE task_id=$1`, taskID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	data, err := c.ledger.open(taskID.String(), sealed)
	if err != nil {
		t.Fatal(err)
	}
	var intent taskflow.CreateTaskReq
	if err := json.Unmarshal(data, &intent); err != nil {
		t.Fatal(err)
	}
	return intent
}

func TestNextTurnModelDoesNotChangeAcceptedRoundOrWakeEnvironment(t *testing.T) {
	for _, state := range []string{"online", "hibernated"} {
		t.Run(state, func(t *testing.T) {
			c, task, owner := nextModelFixture(t)
			if _, err := c.ledger.db.Exec(`UPDATE runtime_environments SET state=$2 WHERE id=$1`, task.VMID, state); err != nil {
				t.Fatal(err)
			}
			if state == "online" {
				if _, err := c.ledger.db.Exec(`UPDATE runtime_commands SET state='running',submission_started=true,run_id='original-run' WHERE task_id=$1 AND operation='task'`, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			var before []byte
			if err := c.ledger.db.QueryRow(`SELECT payload FROM runtime_commands WHERE task_id=$1 AND operation='task'`, task.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			req := selectModelRequest(task, owner, "select-a", "next-model")
			if err := c.TaskManager().(taskflow.NextTurnModelSelector).SelectNextTurnModel(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			var after []byte
			var canceled bool
			if err := c.ledger.db.QueryRow(`SELECT payload,cancel_requested FROM runtime_commands WHERE task_id=$1 AND operation='task'`, task.ID).Scan(&after, &canceled); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || canceled {
				t.Fatal("selection changed or canceled an accepted round")
			}
			var actualState string
			if err := c.ledger.db.QueryRow(`SELECT state FROM runtime_environments WHERE id=$1`, task.VMID).Scan(&actualState); err != nil || actualState != state {
				t.Fatalf("selection changed environment state: %s %v", actualState, err)
			}
			var restarts int
			if err := c.ledger.db.QueryRow(`SELECT count(*) FROM runtime_commands WHERE task_id=$1 AND operation='restart'`, task.ID).Scan(&restarts); err != nil || restarts != 0 {
				t.Fatal("selection enqueued a restart")
			}
			if _, err := c.ledger.db.Exec(`UPDATE runtime_commands SET state='complete' WHERE task_id=$1`, task.ID); err != nil {
				t.Fatal(err)
			}
			// Continue only reads the saved intent; no engine is needed for admission.
			if err := c.TaskManager().Continue(context.Background(), taskflow.TaskReq{Task: &taskflow.Task{ID: task.ID, Text: "next turn", ClientMessageID: "second"}}); err != nil {
				t.Fatal(err)
			}
			var id string
			if err := c.ledger.db.QueryRow(`SELECT id,payload FROM runtime_commands WHERE task_id=$1 AND turn=2`, task.ID).Scan(&id, &after); err != nil {
				t.Fatal(err)
			}
			data, err := c.ledger.open(id, after)
			if err != nil {
				t.Fatal(err)
			}
			var next taskflow.CreateTaskReq
			if err := json.Unmarshal(data, &next); err != nil || next.LLM.Model != "next-model" || next.ModelSelectionID == "" {
				t.Fatalf("next round did not snapshot selection: %v", err)
			}
		})
	}
}

func TestNextTurnModelReplayCannotOverwriteLaterSelection(t *testing.T) {
	c, task, owner := nextModelFixture(t)
	selector := c.TaskManager().(taskflow.NextTurnModelSelector)
	first := selectModelRequest(task, owner, "first", "first")
	second := selectModelRequest(task, owner, "second", "second")
	for _, req := range []taskflow.NextTurnModelReq{first, second, first} {
		if err := selector.SelectNextTurnModel(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if readSelectedIntent(t, c, task.ID).LLM.Model != "second" {
		t.Fatal("old request replay replaced latest selection")
	}
	first.ModelID = uuid.New()
	if err := selector.SelectNextTurnModel(context.Background(), first); err == nil {
		t.Fatal("conflicting request ID accepted")
	}
	second.OwnerID = uuid.New()
	if err := selector.SelectNextTurnModel(context.Background(), second); err == nil {
		t.Fatal("another owner's selection accepted")
	}
}

func TestNextTurnModelAuditFailureRollsBackSelection(t *testing.T) {
	c, task, owner := nextModelFixture(t)
	before := readSelectedIntent(t, c, task.ID)
	if _, err := c.ledger.db.Exec(`ALTER TABLE task_model_switches ADD CONSTRAINT reject_saved_selection CHECK(success IS NULL)`); err != nil {
		t.Fatal(err)
	}
	req := selectModelRequest(task, owner, "rejected", "replacement")
	if err := c.TaskManager().(taskflow.NextTurnModelSelector).SelectNextTurnModel(context.Background(), req); err == nil {
		t.Fatal("audit failure reported success")
	}
	if readSelectedIntent(t, c, task.ID).LLM.Model != before.LLM.Model {
		t.Fatal("failed selection partially updated intent")
	}
}
