package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func (t *taskClient) SelectNextTurnModel(ctx context.Context, req taskflow.NextTurnModelReq) error {
	if req.TaskID == uuid.Nil || req.OwnerID == uuid.Nil || req.ModelID == uuid.Nil || req.RequestID == "" || len(req.RequestID) > 256 || req.Config == nil || req.Config.LLM == nil || req.Config.AgentResources != nil || req.Config.McpServers != nil {
		return errors.New("invalid next-turn model selection")
	}
	tx, err := t.c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	var environmentID string
	if err = tx.QueryRowContext(ctx, `SELECT payload,environment_id FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, req.TaskID).Scan(&raw, &environmentID); err != nil {
		return err
	}
	var owner, state string
	if err = tx.QueryRowContext(ctx, `SELECT owner_id,state FROM runtime_environments WHERE id=$1`, environmentID).Scan(&owner, &state); err != nil {
		return err
	}
	if owner != req.OwnerID.String() {
		return errors.New("task environment is unavailable or belongs to another owner")
	}
	var taskOwner uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT user_id FROM tasks WHERE id=$1 FOR UPDATE`, req.TaskID).Scan(&taskOwner); err != nil {
		return err
	}
	if taskOwner != req.OwnerID {
		return errors.New("task owner mismatch")
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(req.TaskID.String()+":next-turn-model:"+req.RequestID))
	var selected uuid.UUID
	var success sql.NullBool
	err = tx.QueryRowContext(ctx, `SELECT to_model_id,success FROM task_model_switches WHERE id=$1 AND task_id=$2 AND user_id=$3`, id, req.TaskID, req.OwnerID).Scan(&selected, &success)
	if err == nil {
		if selected != req.ModelID || !success.Valid || !success.Bool {
			return errors.New("model selection request ID conflict")
		}
		return tx.Commit() // Replaying an older selection must not replace a newer one.
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var restarting bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_commands WHERE task_id=$1 AND operation='restart' AND state IN ('pending','submitting','unknown','running'))`, req.TaskID).Scan(&restarting); err != nil {
		return err
	}
	if restarting {
		return errors.New("任务配置正在更新，请完成后再选择模型")
	}
	decoded, err := t.c.ledger.open(req.TaskID.String(), raw)
	if err != nil {
		return err
	}
	var intent taskflow.CreateTaskReq
	if err = json.Unmarshal(decoded, &intent); err != nil {
		return err
	}
	if err = mergeExecution(&intent, req.Config); err != nil {
		return err
	}
	intent.ModelSelectionID = id.String()
	data := mustJSON(intent)
	sealed, err := t.c.ledger.seal(req.TaskID.String(), data)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_task_intents SET payload=$2,payload_hash=$3 WHERE task_id=$1`, req.TaskID, sealed, hash(data)); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO task_model_switches(id,task_id,user_id,from_model_id,to_model_id,request_id,load_session,success,message) SELECT $1,$2,$3,model_id,$4,$5,true,true,'Saved for next turn' FROM project_tasks WHERE task_id=$2`, id, req.TaskID, req.OwnerID, req.ModelID, req.RequestID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errors.New("task model selection record was not saved")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE project_tasks SET model_id=$2 WHERE task_id=$1`, req.TaskID, req.ModelID); err != nil {
		return err
	}
	return tx.Commit()
}
