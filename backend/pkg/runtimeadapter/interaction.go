package runtimeadapter

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"connectrpc.com/connect"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
)

//go:embed guest/interaction.py
var interactionScript []byte

var interactionID = regexp.MustCompile(`^(que|per)[A-Za-z0-9_-]{1,128}$`)

type nativeQuestion struct {
	Question string `json:"question"`
	Multiple bool   `json:"multiple"`
	Custom   *bool  `json:"custom"`
	Options  []struct {
		Label string `json:"label"`
	} `json:"options"`
}
type nativeInteraction struct {
	ID        string           `json:"request_id"`
	RunID     string           `json:"run_id"`
	SessionID string           `json:"session_id"`
	Kind      string           `json:"request_kind"`
	Questions []nativeQuestion `json:"questions"`
}
type interactionCommand struct {
	Request taskflow.AskUserQuestionResponse `json:"request"`
	Target  string                           `json:"target"`
	Native  nativeInteraction                `json:"native"`
	Answers [][]string                       `json:"answers"`
	Turn    int                              `json:"turn"`
}

func orderedAnswers(request taskflow.AskUserQuestionResponse, native nativeInteraction) ([][]string, error) {
	if request.Cancelled {
		return nil, nil
	}
	if len(request.AnswersJson) > 1<<20 {
		return nil, errors.New("question response exceeds limit")
	}
	var supplied map[string]json.RawMessage
	if json.Unmarshal([]byte(request.AnswersJson), &supplied) != nil || len(supplied) != len(native.Questions) {
		return nil, errors.New("invalid question answers")
	}
	answers := make([][]string, 0, len(native.Questions))
	seen := map[string]bool{}
	for _, question := range native.Questions {
		raw, exists := supplied[question.Question]
		if !exists || seen[question.Question] {
			return nil, errors.New("question response does not match request")
		}
		seen[question.Question] = true
		var labels []string
		if json.Unmarshal(raw, &labels) != nil {
			var label string
			if json.Unmarshal(raw, &label) != nil {
				return nil, errors.New("invalid question answer")
			}
			labels = []string{label}
		}
		if len(labels) == 0 || (!question.Multiple && len(labels) != 1) {
			return nil, errors.New("invalid question selection count")
		}
		selected := map[string]bool{}
		for _, label := range labels {
			if len(label) == 0 || len(label) > 16384 || selected[label] {
				return nil, errors.New("invalid question selection")
			}
			selected[label] = true
			allowed := question.Custom == nil || *question.Custom
			for _, option := range question.Options {
				if option.Label == label {
					allowed = true
				}
			}
			if !allowed {
				return nil, errors.New("question selection is not allowed")
			}
		}
		answers = append(answers, labels)
	}
	return answers, nil
}

func (t *taskClient) answer(ctx context.Context, env Environment, request taskflow.AskUserQuestionResponse) error {
	if !interactionID.MatchString(request.RequestId) {
		return errors.New("invalid native question request ID")
	}
	request.LogStore = "" // Internal routing does not use client-selected storage.
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(request.TaskId+":interaction:"+request.RequestId)).String()
	tx, err := t.c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var locked string
	if err = tx.QueryRowContext(ctx, `SELECT task_id FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, request.TaskId).Scan(&locked); err != nil {
		return err
	}
	var old []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM runtime_commands WHERE id=$1`, id).Scan(&old)
	if err == nil {
		decoded, err := t.c.ledger.open(id, old)
		if err != nil {
			return err
		}
		var previous interactionCommand
		if json.Unmarshal(decoded, &previous) != nil || hash(mustJSON(previous.Request)) != hash(mustJSON(request)) {
			return errors.New("native response replay conflict")
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return t.waitAnswer(ctx, id)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var data []byte
	var target, run, state string
	var taskTurn int
	err = tx.QueryRowContext(ctx, `SELECT e.chunk,c.id,c.run_id,c.state,c.turn FROM runtime_events e JOIN runtime_commands c ON c.id=e.command_id WHERE e.task_id=$1 AND e.source_key=$2 AND c.operation='task' ORDER BY e.seq DESC LIMIT 1`, request.TaskId, "interaction/"+request.RequestId).Scan(&data, &target, &run, &state, &taskTurn)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("native question does not belong to task")
	}
	if err != nil {
		return err
	}
	if state != "running" && state != "unknown" && state != "submitting" {
		return errors.New("native question is no longer active")
	}
	var chunk taskflow.TaskChunk
	var body struct {
		ToolCall struct {
			Input nativeInteraction `json:"rawInput"`
		} `json:"toolCall"`
	}
	if json.Unmarshal(data, &chunk) != nil || json.Unmarshal(chunk.Data, &body) != nil {
		return errors.New("invalid native question history")
	}
	native := body.ToolCall.Input
	if native.ID != request.RequestId || native.RunID != run || native.SessionID == "" || len(native.Questions) == 0 || len(native.Questions) > 32 || (native.Kind != "question" && native.Kind != "permission") {
		return errors.New("native question correlation mismatch")
	}
	answers, err := orderedAnswers(request, native)
	if err != nil {
		return err
	}
	var turn int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(turn),0)+1 FROM runtime_commands WHERE task_id=$1 AND operation='interaction'`, request.TaskId).Scan(&turn); err != nil {
		return err
	}
	payload := mustJSON(interactionCommand{Request: request, Target: target, Native: native, Answers: answers, Turn: taskTurn})
	sealed, err := t.c.ledger.seal(id, payload)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_commands(id,environment_id,task_id,operation,turn,payload,payload_hash) VALUES($1,$2,$3,'interaction',$4,$5,$6)`, id, env.ID, request.TaskId, turn, sealed, hash(payload)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return t.waitAnswer(ctx, id)
}

func (t *taskClient) waitAnswer(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		var state string
		if err := t.c.ledger.db.QueryRowContext(ctx, `SELECT state FROM runtime_commands WHERE id=$1`, id).Scan(&state); err != nil {
			return err
		}
		if state == "complete" {
			return nil
		}
		if state == "canceled" || state == "failed" {
			return errors.New("native question response was rejected or expired")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (c *Client) processInteraction(ctx context.Context, command *Command, env Environment, engine *Engine) error {
	var payload interactionCommand
	if json.Unmarshal(command.Payload, &payload) != nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("invalid interaction command"))
	}
	// Try the Guest receipt even after the Run ends: a successful response may
	// have completed that Run before the Worker committed its acknowledgement.
	if !command.Submitted {
		if err := c.ledger.BeginSubmission(ctx, command); errors.Is(err, errCanceledBeforeAdmission) {
			return c.ledger.UpdateCommand(ctx, *command, "canceled", "", 0)
		} else if err != nil {
			return err
		}
	}
	request := map[string]any{"run_id": payload.Native.RunID, "session_id": payload.Native.SessionID, "request_id": payload.Native.ID, "request_kind": payload.Native.Kind, "answers": payload.Answers, "cancelled": payload.Request.Cancelled}
	raw := mustJSON(request)
	text, err := engine.execute(ctx, env.SandboxID, "python3 -c \"import base64;exec(base64.b64decode('"+base64.StdEncoding.EncodeToString(interactionScript)+"'))\" '"+base64.StdEncoding.EncodeToString(raw)+"'", 4096)
	if err != nil {
		return err
	}
	var result struct {
		Error   string `json:"error"`
		Invalid bool   `json:"invalid"`
		Data    struct {
			Success bool `json:"success"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(text), &result) != nil {
		return errors.New("invalid Guest interaction result")
	}
	if result.Invalid {
		return c.ledger.UpdateCommand(ctx, *command, "failed", "", 0)
	}
	if result.Error != "" || !result.Data.Success {
		return errors.New("Guest interaction requires reconciliation")
	}
	return c.finishInteraction(ctx, *command, payload)
}

func (c *Client) finishInteraction(ctx context.Context, command Command, payload interactionCommand) error {
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockCommand(ctx, tx, command); err != nil {
		return err
	}
	request := payload.Request
	if request.AnswersJson == "" {
		request.AnswersJson = "{}"
	}
	chunk := taskflow.TaskChunk{Event: "reply-question", Timestamp: time.Now().UnixNano(), Data: mustJSON(request)}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_events(task_id,command_id,source_key,turn,chunk) VALUES($1,$2,'reply-question',$3,$4) ON CONFLICT(command_id,source_key) DO NOTHING`, command.TaskID, command.ID, payload.Turn, string(mustJSON(chunk))); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET state='complete',lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1`, command.ID); err != nil {
		return err
	}
	return tx.Commit()
}
