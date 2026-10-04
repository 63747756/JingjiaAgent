package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func restartBusinessFixture(t *testing.T) (*Client, taskflow.CreateTaskReq, uuid.UUID, uuid.UUID) {
	t.Helper()
	c, task, _, _ := controlFixture(t)
	var owner uuid.UUID
	if err := c.ledger.db.QueryRow(`SELECT owner_id FROM runtime_environments WHERE id=$1`, task.VMID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ledger.db.Exec(`ALTER TABLE tasks ADD COLUMN user_id uuid, ADD COLUMN skill_ids jsonb, ADD COLUMN plugin_ids jsonb;
CREATE TABLE project_tasks(task_id uuid PRIMARY KEY, model_id uuid NOT NULL);
CREATE TABLE task_model_switches(id uuid PRIMARY KEY, task_id uuid NOT NULL, user_id uuid NOT NULL, to_model_id uuid NOT NULL, request_id text NOT NULL, load_session boolean NOT NULL, success boolean, message text NOT NULL DEFAULT '', session_id text NOT NULL DEFAULT '', updated_at timestamptz NOT NULL DEFAULT now());`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ledger.db.Exec(`UPDATE tasks SET user_id=$2,skill_ids='["original-skill"]',plugin_ids='["original-plugin"]' WHERE id=$1`, task.ID, owner); err != nil {
		t.Fatal(err)
	}
	oldModel := uuid.New()
	if _, err := c.ledger.db.Exec(`INSERT INTO project_tasks(task_id,model_id) VALUES($1,$2)`, task.ID, oldModel); err != nil {
		t.Fatal(err)
	}
	return c, task, owner, oldModel
}

func modelRestartRequest(t *testing.T, c *Client, task taskflow.CreateTaskReq, owner uuid.UUID) taskflow.RestartTaskReq {
	t.Helper()
	change := &taskflow.RestartModelSwitch{ID: uuid.New(), ModelID: uuid.New()}
	req := taskflow.RestartTaskReq{ID: task.ID, RequestId: "model-switch", LoadSession: true,
		ExecutionConfig:  &taskflow.TaskExecutionConfig{LLM: &taskflow.LLM{Model: "replacement-model", ApiKey: "fixture-key"}},
		BusinessMutation: &taskflow.RestartBusinessMutation{OwnerID: owner, ModelSwitch: change}}
	if _, err := c.ledger.db.Exec(`INSERT INTO task_model_switches(id,task_id,user_id,to_model_id,request_id,load_session) VALUES($1,$2,$3,$4,$5,$6)`, change.ID, task.ID, owner, change.ModelID, req.RequestId, req.LoadSession); err != nil {
		t.Fatal(err)
	}
	return req
}

func admitRestartWithoutWorker(t *testing.T, c *Client, req taskflow.RestartTaskReq) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.TaskManager().Restart(ctx, req); !taskflow.IsRestartPending(err) {
		t.Fatalf("caller timeout was not pending: %v", err)
	}
}

func finishBusinessRestart(t *testing.T, c *Client) {
	t.Helper()
	// The original unsubmitted user turn is canceled before the restart runs.
	for i := 0; i < 2; i++ {
		ready(t, c)
		if err := c.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRestartWorkerReconcilesModelAfterHTTPTimeout(t *testing.T) {
	c, task, owner, _ := restartBusinessFixture(t)
	req := modelRestartRequest(t, c, task, owner)
	admitRestartWithoutWorker(t, c, req)
	var success sql.NullBool
	if err := c.ledger.db.QueryRow(`SELECT success FROM task_model_switches WHERE id=$1`, req.BusinessMutation.ModelSwitch.ID).Scan(&success); err != nil || success.Valid {
		t.Fatalf("HTTP timeout prematurely finished audit: %v, %v", success, err)
	}
	finishBusinessRestart(t, c)
	resumer := c.TaskManager().(taskflow.RestartResumer)
	resume := req
	resume.ExecutionConfig = nil
	resp, found, err := resumer.ResumeRestart(context.Background(), resume)
	if err != nil || !found || resp == nil || !resp.Success || !resp.BusinessStateCommitted {
		t.Fatalf("durable result missing: %+v, %v", resp, err)
	}
	var actual uuid.UUID
	var session string
	if err := c.ledger.db.QueryRow(`SELECT model_id FROM project_tasks WHERE task_id=$1`, task.ID).Scan(&actual); err != nil || actual != req.BusinessMutation.ModelSwitch.ModelID {
		t.Fatalf("worker did not persist target model: %s, %v", actual, err)
	}
	if err := c.ledger.db.QueryRow(`SELECT success,session_id FROM task_model_switches WHERE id=$1`, req.BusinessMutation.ModelSwitch.ID).Scan(&success, &session); err != nil || !success.Valid || !success.Bool || session != "ses_original" {
		t.Fatalf("worker did not complete audit: %v, %s, %v", success, session, err)
	}
	// A successful old request must not roll back a newer business selection.
	newer := uuid.New()
	if _, err := c.ledger.db.Exec(`UPDATE project_tasks SET model_id=$2 WHERE task_id=$1`, task.ID, newer); err != nil {
		t.Fatal(err)
	}
	if _, found, err := resumer.ResumeRestart(context.Background(), resume); err != nil || !found {
		t.Fatal(err)
	}
	if err := c.ledger.db.QueryRow(`SELECT model_id FROM project_tasks WHERE task_id=$1`, task.ID).Scan(&actual); err != nil || actual != newer {
		t.Fatalf("old retry restored stale model: %v", err)
	}
	if err := c.TaskManager().Continue(context.Background(), taskflow.TaskReq{Task: &taskflow.Task{ID: task.ID, Text: "next turn"}}); err != nil {
		t.Fatalf("successful restart retained fence: %v", err)
	}
}

func TestRestartWorkerReconcilesResourcesAfterHTTPTimeout(t *testing.T) {
	c, task, owner, _ := restartBusinessFixture(t)
	req := taskflow.RestartTaskReq{ID: task.ID, RequestId: "resources", LoadSession: true,
		BusinessMutation: &taskflow.RestartBusinessMutation{OwnerID: owner, ResourceSelection: &taskflow.RestartResourceSelection{SkillIDs: []string{"replacement-skill"}, PluginIDs: []string{}}}}
	admitRestartWithoutWorker(t, c, req)
	finishBusinessRestart(t, c)
	var skillJSON, pluginJSON []byte
	if err := c.ledger.db.QueryRow(`SELECT skill_ids,plugin_ids FROM tasks WHERE id=$1`, task.ID).Scan(&skillJSON, &pluginJSON); err != nil {
		t.Fatal(err)
	}
	var skills, plugins []string
	if err := json.Unmarshal(skillJSON, &skills); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pluginJSON, &plugins); err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0] != "replacement-skill" || plugins == nil || len(plugins) != 0 {
		t.Fatalf("resource baseline/clear lost after timeout: %s, %s", skillJSON, pluginJSON)
	}
	if _, err := c.ledger.db.Exec(`UPDATE tasks SET skill_ids='["newer-skill"]' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	response, found, err := c.TaskManager().(taskflow.RestartResumer).ResumeRestart(context.Background(), req)
	if err != nil || !found || !response.BusinessStateCommitted {
		t.Fatalf("resource replay failed: %v", err)
	}
	if err := c.ledger.db.QueryRow(`SELECT skill_ids FROM tasks WHERE id=$1`, task.ID).Scan(&skillJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(skillJSON, &skills); err != nil || len(skills) != 1 || skills[0] != "newer-skill" {
		t.Fatalf("resource replay overwrote newer selection: %s, %v", skillJSON, err)
	}
}

func TestRestartCanceledAuditAndFenceSettleTogether(t *testing.T) {
	c, task, owner, oldModel := restartBusinessFixture(t)
	req := modelRestartRequest(t, c, task, owner)
	admitRestartWithoutWorker(t, c, req)
	if _, err := c.ledger.db.Exec(`UPDATE runtime_commands SET cancel_requested=true WHERE task_id=$1 AND operation='restart'`, task.ID); err != nil {
		t.Fatal(err)
	}
	finishBusinessRestart(t, c)
	var success sql.NullBool
	if err := c.ledger.db.QueryRow(`SELECT success FROM task_model_switches WHERE id=$1`, req.BusinessMutation.ModelSwitch.ID).Scan(&success); err != nil || !success.Valid || success.Bool {
		t.Fatalf("canceled restart left audit pending: %v, %v", success, err)
	}
	var actual uuid.UUID
	if err := c.ledger.db.QueryRow(`SELECT model_id FROM project_tasks WHERE task_id=$1`, task.ID).Scan(&actual); err != nil || actual != oldModel {
		t.Fatalf("canceled restart changed model: %v", err)
	}
	if err := c.TaskManager().Continue(context.Background(), taskflow.TaskReq{Task: &taskflow.Task{ID: task.ID, Text: "after cancellation"}}); err != nil {
		t.Fatalf("canceled restart retained fence: %v", err)
	}
}

func TestRestartBusinessWriteFailureRetainsFenceAndRetries(t *testing.T) {
	c, task, owner, oldModel := restartBusinessFixture(t)
	req := modelRestartRequest(t, c, task, owner)
	// Force a business write error after the remote session operation succeeds.
	if _, err := c.ledger.db.Exec(`ALTER TABLE project_tasks ADD CONSTRAINT fixture_model_write CHECK (model_id='` + oldModel.String() + `')`); err != nil {
		t.Fatal(err)
	}
	admitRestartWithoutWorker(t, c, req)
	ready(t, c)
	if err := c.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready(t, c)
	if err := c.Step(context.Background()); err == nil {
		t.Fatal("business write failure was acknowledged")
	}
	var success sql.NullBool
	if err := c.ledger.db.QueryRow(`SELECT success FROM task_model_switches WHERE id=$1`, req.BusinessMutation.ModelSwitch.ID).Scan(&success); err != nil || success.Valid {
		t.Fatalf("failed transaction finalized audit: %v", err)
	}
	var sessions int
	if err := c.ledger.db.QueryRow(`SELECT count(*) FROM runtime_task_sessions WHERE task_id=$1`, task.ID).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("session mapping committed without business result: %d, %v", sessions, err)
	}
	if err := c.TaskManager().Continue(context.Background(), taskflow.TaskReq{Task: &taskflow.Task{ID: task.ID, Text: "must remain fenced"}}); err == nil {
		t.Fatal("business write failure released restart fence")
	}
	if _, err := c.ledger.db.Exec(`ALTER TABLE project_tasks DROP CONSTRAINT fixture_model_write`); err != nil {
		t.Fatal(err)
	}
	ready(t, c)
	if err := c.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, found, err := c.TaskManager().(taskflow.RestartResumer).ResumeRestart(context.Background(), req)
	if err != nil || !found || response == nil || !response.Success || !response.BusinessStateCommitted {
		t.Fatalf("business mutation did not recover: %+v, %v", response, err)
	}
}
