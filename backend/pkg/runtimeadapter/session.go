package runtimeadapter

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

//go:embed guest/session.py
var sessionScript []byte

type providerSession struct {
	ID string `json:"session_id"`
}

func runtimeProvider(agent taskflow.CodingAgent) (string, error) {
	switch agent {
	case 0, taskflow.CodingAgentOpenCode:
		return "opencode", nil
	case taskflow.CodingAgentCodex:
		return "codex", nil
	case taskflow.CodingAgentClaude:
		return "claude", nil
	default:
		return "", ErrParity
	}
}

func (e *Engine) session(ctx context.Context, sandbox string, request any) (providerSession, error) {
	var result providerSession
	b, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	command := "python3 -c \"import base64;exec(base64.b64decode('" + base64.StdEncoding.EncodeToString(sessionScript) + "'))\" '" + base64.StdEncoding.EncodeToString(b) + "'"
	text, err := e.execute(ctx, sandbox, command, 4096)
	if err != nil {
		return result, err
	}
	var response struct {
		Data  providerSession `json:"data"`
		Error string          `json:"error"`
	}
	if json.Unmarshal([]byte(text), &response) != nil || response.Error != "" {
		return result, errors.New("Guest provider session control failed")
	}
	return response.Data, nil
}

func (s *Ledger) SaveSession(ctx context.Context, command Command, provider, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockCommand(ctx, tx, command); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_task_sessions(task_id,provider,session_id,command_id) VALUES($1,$2,$3,$4) ON CONFLICT(task_id) DO UPDATE SET provider=EXCLUDED.provider,session_id=EXCLUDED.session_id,command_id=EXCLUDED.command_id,updated_at=now()`, command.TaskID, provider, id, command.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET result=$2 WHERE id=$1`, command.ID, string(mustJSON(map[string]string{"provider": provider, "session_id": id}))); err != nil {
		return err
	}
	return tx.Commit()
}
