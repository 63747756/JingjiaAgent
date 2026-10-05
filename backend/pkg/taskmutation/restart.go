// Package taskmutation reconciles authorized business mutations in the same
// database transaction as durable runtime control commands. It performs no RPCs.
package taskmutation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func validateShape(m *taskflow.RestartBusinessMutation) error {
	if m == nil {
		return nil // Plain restarts and legacy commands carry no business change.
	}
	if m.OwnerID == uuid.Nil || (m.ModelSwitch == nil) == (m.ResourceSelection == nil) {
		return errors.New("invalid restart business mutation")
	}
	if m.ModelSwitch != nil && (m.ModelSwitch.ID == uuid.Nil || m.ModelSwitch.ModelID == uuid.Nil) {
		return errors.New("invalid restart model switch")
	}
	return nil
}

func lockTask(ctx context.Context, tx *sql.Tx, taskID string, m *taskflow.RestartBusinessMutation) error {
	var owner uuid.UUID
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM tasks WHERE id=$1 FOR UPDATE`, taskID).Scan(&owner); err != nil {
		return fmt.Errorf("lock restart business task: %w", err)
	}
	if owner != m.OwnerID {
		return errors.New("restart business mutation owner mismatch")
	}
	return nil
}

// ValidateRestart checks that metadata belongs to the routed environment and
// existing authorized audit record. Call before admitting a new runtime command;
// it does not authorize a model/resource that the usecase has not validated.
func ValidateRestart(ctx context.Context, tx *sql.Tx, taskID, ownerID string, request taskflow.RestartTaskReq) error {
	m := request.BusinessMutation
	if err := validateShape(m); err != nil || m == nil {
		return err
	}
	if m.OwnerID.String() != ownerID || request.ID.String() != taskID {
		return errors.New("restart business mutation routing mismatch")
	}
	if err := lockTask(ctx, tx, taskID, m); err != nil {
		return err
	}
	if m.ModelSwitch == nil {
		return nil
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_model_switches WHERE id=$1 AND task_id=$2 AND user_id=$3 AND to_model_id=$4 AND request_id=$5 AND load_session=$6 AND success IS NULL)`,
		m.ModelSwitch.ID, taskID, m.OwnerID, m.ModelSwitch.ModelID, request.RequestId, request.LoadSession).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("restart model switch audit record mismatch or already finished")
	}
	return nil
}

// ApplyRestart atomically applies a terminal restart result. The caller owns the
// transaction and must commit the command's terminal state in that transaction.
// Any error must roll back both writes and retain the command for reconciliation.
// A repeated model-switch completion never overwrites a later task model.
func ApplyRestart(ctx context.Context, tx *sql.Tx, taskID string, m *taskflow.RestartBusinessMutation, response taskflow.RestartTaskResp) error {
	if err := validateShape(m); err != nil || m == nil {
		return err
	}
	if err := lockTask(ctx, tx, taskID, m); err != nil {
		return err
	}
	if change := m.ModelSwitch; change != nil {
		var success sql.NullBool
		if err := tx.QueryRowContext(ctx, `SELECT success FROM task_model_switches WHERE id=$1 AND task_id=$2 AND user_id=$3 AND to_model_id=$4 FOR UPDATE`,
			change.ID, taskID, m.OwnerID, change.ModelID).Scan(&success); err != nil {
			return fmt.Errorf("lock restart model switch: %w", err)
		}
		if success.Valid {
			if success.Bool != response.Success {
				return errors.New("restart model switch terminal result conflict")
			}
			return nil
		}
		if response.Success {
			result, err := tx.ExecContext(ctx, `UPDATE project_tasks SET model_id=$2 WHERE task_id=$1`, taskID, change.ModelID)
			if err = exactlyOne(result, err); err != nil {
				return fmt.Errorf("update restart task model: %w", err)
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE task_model_switches SET success=$2,message=$3,session_id=$4,updated_at=now() WHERE id=$1 AND success IS NULL`,
			change.ID, response.Success, response.Message, response.SessionID)
		return exactlyOne(result, err)
	}
	if !response.Success {
		return nil
	}
	selection := m.ResourceSelection
	// Empty selections are explicit clears. Store JSON arrays, not JSON null.
	skills, plugins := selection.SkillIDs, selection.PluginIDs
	if skills == nil {
		skills = []string{}
	}
	if plugins == nil {
		plugins = []string{}
	}
	skillJSON, err := json.Marshal(skills)
	if err != nil {
		return err
	}
	pluginJSON, err := json.Marshal(plugins)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET skill_ids=$2::jsonb,plugin_ids=$3::jsonb WHERE id=$1 AND user_id=$4`,
		taskID, string(skillJSON), string(pluginJSON), m.OwnerID)
	return exactlyOne(result, err)
}

func exactlyOne(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("updated business row count = %d, want 1", count)
	}
	return nil
}
