package runtimeadapter

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

// PreparationConditions projects only committed lifecycle facts. A running
// Sandbox does not prove that preparation and product callbacks have completed.
// No RPC, decrypted configuration or upstream diagnostic is exposed here.
func (v *vmClient) PreparationConditions(ctx context.Context, id string) ([]*taskflow.Condition, error) {
	var environment, command, sandbox string
	var canceled, submitted, claimed bool
	var changed time.Time
	err := v.c.ledger.db.QueryRowContext(ctx, `
		SELECT e.state,e.sandbox_id,COALESCE(c.state,''),
		       COALESCE(c.cancel_requested,false),COALESCE(c.submission_started,false),
		       COALESCE(c.lease_until>now(),false),
		       COALESCE(c.updated_at,e.created_at)
		FROM runtime_environments e
		LEFT JOIN runtime_commands c ON c.environment_id=e.id AND c.operation='prepare'
		WHERE e.id=$1 ORDER BY c.created_at DESC,c.id DESC LIMIT 1`, id).
		Scan(&environment, &sandbox, &command, &canceled, &submitted, &claimed, &changed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // Existing Taskflow environments retain their own conditions.
	}
	if err != nil {
		return nil, err
	}
	condition := &taskflow.Condition{Type: "Scheduled", Status: taskflow.ConditionStatusInProgress,
		Reason: "RuntimePreparing", LastTransitionTime: changed.Unix()}
	switch {
	case command == "failed":
		condition.Type, condition.Status, condition.Reason = "Failed", taskflow.ConditionStatusFalse, "RuntimePreparationFailed"
	case command == "canceled":
		condition.Type, condition.Status, condition.Reason = "Failed", taskflow.ConditionStatusFalse, "RuntimePreparationCanceled"
	case command == "complete" && sandbox != "":
		// This describes initial preparation, not current availability. Sleeping
		// and recycled environments continue to use the existing VM status UI.
		condition.Type, condition.Status, condition.Reason = "Ready", taskflow.ConditionStatusTrue, "RuntimePrepared"
	case environment == "deleted":
		condition.Type, condition.Status, condition.Reason = "Failed", taskflow.ConditionStatusFalse, "RuntimePreparationCanceled"
	case canceled || environment == "stopping":
		condition.Reason = "RuntimePreparationCanceling"
	case command == "unknown" || command == "complete":
		condition.Reason = "RuntimePreparationReconciling"
	case (command == "pending" || (command == "" && environment == "pending")) && !submitted && !claimed:
		condition.Reason = "RuntimePreparationWaiting"
	case command != "submitting" && command != "running" && command != "pending":
		condition.Reason = "RuntimePreparationReconciling"
	}
	return []*taskflow.Condition{condition}, nil
}

var _ taskflow.PreparationReader = (*vmClient)(nil)
