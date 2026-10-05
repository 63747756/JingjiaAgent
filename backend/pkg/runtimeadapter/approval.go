package runtimeadapter

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

//go:embed guest/approval.py
var approvalScript []byte

type approvalCommand struct {
	TaskID    string `json:"task_id"`
	Enabled   bool   `json:"enabled"`
	Revision  int    `json:"revision"`
	CommandID string `json:"command_id"`
}

func (t *taskClient) approve(ctx context.Context, env Environment, request taskflow.TaskApproveReq) error {
	if request.AutoApprove == nil {
		return errors.New("missing approval policy")
	}
	if env.State != "online" || env.SandboxID == "" {
		return errors.New("task environment is not ready for approval policy")
	}
	tx, err := t.c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var locked string
	if err = tx.QueryRowContext(ctx, `SELECT task_id FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, request.ID).Scan(&locked); err != nil {
		return err
	}
	var previous []byte
	var id, state string
	var revision int
	err = tx.QueryRowContext(ctx, `SELECT id,state,turn,payload FROM runtime_commands WHERE task_id=$1 AND operation='approval' ORDER BY turn DESC LIMIT 1`, request.ID).Scan(&id, &state, &revision, &previous)
	if err == nil {
		decoded, err := t.c.ledger.open(id, previous)
		if err != nil {
			return err
		}
		var prior approvalCommand
		if err = json.Unmarshal(decoded, &prior); err != nil {
			return err
		}
		if state != "failed" && state != "canceled" {
			if prior.Enabled == *request.AutoApprove {
				if err = tx.Commit(); err != nil {
					return err
				}
				return t.waitApproval(ctx, id)
			}
			if state != "complete" {
				return errors.New("previous approval policy requires reconciliation")
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var canceled bool
	if err = tx.QueryRowContext(ctx, `SELECT cancel_requested FROM runtime_task_intents WHERE task_id=$1`, request.ID).Scan(&canceled); err != nil {
		return err
	}
	if canceled {
		return errors.New("task is canceled")
	}
	var activeRestart int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_commands WHERE task_id=$1 AND operation='restart' AND state IN ('pending','submitting','unknown','running')`, request.ID).Scan(&activeRestart); err != nil {
		return err
	}
	if activeRestart != 0 {
		return errors.New("task has a pending restart")
	}
	id = uuid.NewSHA1(uuid.NameSpaceOID, []byte(request.ID.String()+":approval:"+uuid.NewString())).String()
	payload := mustJSON(approvalCommand{TaskID: request.ID.String(), Enabled: *request.AutoApprove, Revision: revision + 1, CommandID: id})
	sealed, err := t.c.ledger.seal(id, payload)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_commands(id,environment_id,task_id,operation,turn,payload,payload_hash) VALUES($1,$2,$3,'approval',$4,$5,$6)`, id, env.ID, request.ID, revision+1, sealed, hash(payload)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return t.waitApproval(ctx, id)
}

func (t *taskClient) waitApproval(ctx context.Context, id string) error {
	// Policy acknowledgements share the same durable wait semantics as replies.
	return t.waitAnswer(ctx, id)
}

func (c *Client) processApproval(ctx context.Context, command *Command, env Environment, engine *Engine) error {
	var payload approvalCommand
	if json.Unmarshal(command.Payload, &payload) != nil || payload.TaskID != command.TaskID || payload.CommandID != command.ID || payload.Revision != command.Turn {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("invalid approval policy command"))
	}
	if !command.Submitted {
		if err := c.ledger.BeginSubmission(ctx, command); errors.Is(err, errCanceledBeforeAdmission) {
			return c.ledger.UpdateCommand(ctx, *command, "canceled", "", 0)
		} else if err != nil {
			return err
		}
	}
	raw := base64.StdEncoding.EncodeToString(mustJSON(payload))
	output, err := engine.execute(ctx, env.SandboxID, "python3 -c \"import base64;exec(base64.b64decode('"+base64.StdEncoding.EncodeToString(approvalScript)+"'))\" '"+raw+"'", 4096)
	if err != nil {
		return err
	}
	var result struct {
		Invalid bool `json:"invalid"`
		Data    struct {
			Success bool `json:"success"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(output), &result) != nil {
		return errors.New("invalid Guest approval policy result")
	}
	if result.Invalid {
		return c.ledger.UpdateCommand(ctx, *command, "failed", "", 0)
	}
	if !result.Data.Success {
		return errors.New("Guest approval policy requires reconciliation")
	}
	return c.ledger.UpdateCommand(ctx, *command, "complete", "", 0)
}
