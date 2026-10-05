package runtimeadapter

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

var errLeaseLost = errors.New("runtime worker lease lost")
var errCanceledBeforeAdmission = errors.New("task canceled before admission")
var errAdmissionUncertain = errors.New("canceled submission awaits runtime reconciliation")

func lockCommand(ctx context.Context, tx *sql.Tx, c Command) error {
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(lease_token=$2 AND lease_until>now(),false) FROM runtime_commands WHERE id=$1 FOR UPDATE`, c.ID, c.Lease).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return errLeaseLost
	}
	return nil
}

// BeginSubmission commits the uncertainty boundary before sending any RPC.
func (s *Ledger) BeginSubmission(ctx context.Context, c *Command) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockCommand(ctx, tx, *c); err != nil {
		return err
	}
	var canceled bool
	if err = tx.QueryRowContext(ctx, `SELECT cancel_requested FROM runtime_commands WHERE id=$1`, c.ID).Scan(&canceled); err != nil {
		return err
	}
	if canceled && !c.Submitted {
		return errCanceledBeforeAdmission
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET submission_started=true,state='submitting' WHERE id=$1`, c.ID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	c.Submitted = true
	return nil
}

func (s *Ledger) SaveAdmission(ctx context.Context, c Command) error {
	result, err := s.db.ExecContext(ctx, `UPDATE runtime_commands SET run_id=$3,state='running' WHERE id=$1 AND lease_token=$2 AND lease_until>now()`, c.ID, c.Lease, c.RunID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errLeaseLost
	}
	return nil
}

// Finish commits the end event, business status and command result together.
// Only the current lease can publish completion. No terminal state is emitted
// before remote cancellation/completion has actually been observed.
func (s *Ledger) Finish(ctx context.Context, c Command, state, message string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialize terminal publication with question admission. The task intent
	// then environment order matches Continue and Restart admission.
	if c.TaskID != "" {
		var task string
		if err = tx.QueryRowContext(ctx, `SELECT task_id FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, c.TaskID).Scan(&task); err != nil {
			return err
		}
	}
	var sandbox string
	if err = tx.QueryRowContext(ctx, `SELECT sandbox_id FROM runtime_environments WHERE id=$1 FOR UPDATE`, c.EnvironmentID).Scan(&sandbox); err != nil {
		return err
	}
	if err = lockCommand(ctx, tx, c); err != nil {
		return err
	}
	if c.Operation == "task" {
		var pending bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_commands WHERE task_id=$1 AND operation='interaction' AND state IN ('pending','submitting','unknown','running'))`, c.TaskID).Scan(&pending); err != nil {
			return err
		}
		if pending {
			// Run completion is proven, but its final answer receipt can lag.
			// Persist that fact to close admission while the interaction worker
			// reconciles. Do not publish a stream-closing event before it.
			if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET state='running',result=jsonb_build_object('terminal_observed',true),run_id=$2,event_offset=$3,lease_token=NULL,lease_until=NULL,available_at=now()+interval '0.1 second',updated_at=now() WHERE id=$1`, c.ID, c.RunID, c.Offset); err != nil {
				return err
			}
			return tx.Commit()
		}
	}
	if c.TaskID != "" {
		appendEvent := func(key string, chunk taskflow.TaskChunk) error {
			chunk.Timestamp = time.Now().UnixNano()
			_, err := tx.ExecContext(ctx, `INSERT INTO runtime_events(task_id,command_id,source_key,turn,chunk) VALUES($1,$2,$3,$4,$5) ON CONFLICT(command_id,source_key) DO NOTHING`, c.TaskID, c.ID, key, c.Turn, string(mustJSON(chunk)))
			return err
		}
		if message != "" {
			if err = appendEvent("error", taskflow.TaskChunk{Event: "task-error", Data: mustJSON(map[string]string{"message": message})}); err != nil {
				return err
			}
		}
		if err = appendEvent("task-ended", taskflow.TaskChunk{Event: "task-ended", Data: mustJSON(map[string]string{"status": state})}); err != nil {
			return err
		}
		// Taskflow's task is a reusable Agent session. A terminal Run ends one
		// prompt round, while the business task remains interactive until its
		// environment is explicitly recycled by the existing lifecycle.
		if c.Operation == "prepare" {
			_, err = tx.ExecContext(ctx, `UPDATE tasks SET status='error',completed_at=now() WHERE id=$1`, c.TaskID)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE tasks SET completed_at=NULL WHERE id=$1 AND status='processing'`, c.TaskID)
		}
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET state=$2,run_id=$3,event_offset=$4,lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1`, c.ID, state, c.RunID, c.Offset); err != nil {
		return err
	}
	if (state == "failed" || state == "canceled") && sandbox == "" {
		var remotePossible, active bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_commands WHERE environment_id=$1 AND submission_started),
   EXISTS(SELECT 1 FROM runtime_commands WHERE environment_id=$1 AND state IN ('pending','submitting','unknown','running'))`, c.EnvironmentID).Scan(&remotePossible, &active); err != nil {
			return err
		}
		if !remotePossible && !active {
			if err = releaseReservationTx(ctx, tx, c.EnvironmentID, "failed_before_submission"); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE runtime_environments SET state='offline' WHERE id=$1 AND state NOT IN ('deleted','stopping')`, c.EnvironmentID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
