package runtimeadapter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

func TestPreparationConditionsUseDurableFactsWithoutRuntime(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	migration, err := os.ReadFile(filepath.Join("..", "..", "migration", "000032_runtime_preparation.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	req := stageFixture(t, l)
	claimed, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v := &vmClient{c: &Client{ledger: l}}
	conditions, err := v.PreparationConditions(ctx, req.VMID)
	if err != nil || len(conditions) != 1 || conditions[0].Reason != "RuntimePreparing" {
		t.Fatal("claimed project initialization still shown as queued", err)
	}
	if err = l.UpdateCommand(ctx, claimed, "pending", "", 0); err != nil {
		t.Fatal(err)
	}
	changed := time.Unix(1730000000, 0).UTC()
	for _, tc := range []struct {
		name, command, environment, sandbox, reason, kind string
		canceled, submitted                               bool
		status                                            taskflow.ConditionStatus
	}{
		{"queued", "pending", "pending", "", "RuntimePreparationWaiting", "Scheduled", false, false, taskflow.ConditionStatusInProgress},
		{"admitting", "submitting", "pending", "", "RuntimePreparing", "Scheduled", false, true, taskflow.ConditionStatusInProgress},
		{"sandbox-running-before-ready-callback", "running", "online", "sandbox", "RuntimePreparing", "Scheduled", false, true, taskflow.ConditionStatusInProgress},
		{"transport-outage", "unknown", "pending", "sandbox", "RuntimePreparationReconciling", "Scheduled", false, true, taskflow.ConditionStatusInProgress},
		{"outage-before-admission", "unknown", "pending", "", "RuntimePreparationReconciling", "Scheduled", false, false, taskflow.ConditionStatusInProgress},
		{"ready", "complete", "online", "sandbox", "RuntimePrepared", "Ready", false, true, taskflow.ConditionStatusTrue},
		{"sleeping-after-preparation", "complete", "hibernated", "sandbox", "RuntimePrepared", "Ready", false, true, taskflow.ConditionStatusTrue},
		{"recycled-after-preparation", "complete", "deleted", "sandbox", "RuntimePrepared", "Ready", true, true, taskflow.ConditionStatusTrue},
		{"incomplete-mapping", "complete", "online", "", "RuntimePreparationReconciling", "Scheduled", false, true, taskflow.ConditionStatusInProgress},
		{"failed", "failed", "offline", "", "RuntimePreparationFailed", "Failed", false, false, taskflow.ConditionStatusFalse},
		{"cancel-request-is-not-confirmation", "running", "stopping", "sandbox", "RuntimePreparationCanceling", "Scheduled", true, true, taskflow.ConditionStatusInProgress},
		{"cancel-confirmed", "canceled", "offline", "sandbox", "RuntimePreparationCanceled", "Failed", true, true, taskflow.ConditionStatusFalse},
		{"recycled-before-preparation", "pending", "deleted", "", "RuntimePreparationCanceled", "Failed", true, false, taskflow.ConditionStatusFalse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := l.db.ExecContext(ctx, `UPDATE runtime_environments SET state=$2,sandbox_id=$3 WHERE id=$1`, req.VMID, tc.environment, tc.sandbox); err != nil {
				t.Fatal(err)
			}
			if _, err := l.db.ExecContext(ctx, `UPDATE runtime_commands SET state=$2,cancel_requested=$3,submission_started=$4,updated_at=$5 WHERE environment_id=$1 AND operation='prepare'`, req.VMID, tc.command, tc.canceled, tc.submitted, changed); err != nil {
				t.Fatal(err)
			}
			// A fresh adapter has no node or RPC connection; the committed state
			// remains readable after a Worker/API process restart.
			v := &vmClient{c: &Client{ledger: l}}
			conditions, err := v.PreparationConditions(ctx, req.VMID)
			if err != nil || len(conditions) != 1 {
				t.Fatalf("conditions=%v err=%v", conditions, err)
			}
			c := conditions[0]
			if c.Reason != tc.reason || c.Type != tc.kind || c.Status != tc.status || c.LastTransitionTime != changed.Unix() || c.Progress != nil || c.Message != "" {
				t.Fatalf("unexpected public preparation condition: %+v", c)
			}
		})
	}
	v = &vmClient{c: &Client{ledger: l}}
	if conditions, err := v.PreparationConditions(ctx, "legacy-environment"); err != nil || conditions != nil {
		t.Fatal("legacy conditions overridden", err)
	}
	if err := l.Enqueue(ctx, req.VMID, req.ID.String(), "task", 7, req); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.ExecContext(ctx, `UPDATE runtime_commands SET state='failed' WHERE operation='task'`); err != nil {
		t.Fatal(err)
	}
	conditions, err = v.PreparationConditions(ctx, req.VMID)
	if err != nil || conditions[0].Reason != "RuntimePreparationCanceled" {
		t.Fatal("user turn changed preparation", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := v.PreparationConditions(canceled, req.VMID); err == nil {
		t.Fatal("database error silently became a lifecycle state")
	}
}
