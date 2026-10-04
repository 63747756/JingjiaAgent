package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskmutation"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

type restartCommand struct {
	Request taskflow.RestartTaskReq `json:"request"`
	Intent  taskflow.CreateTaskReq  `json:"intent"`
}

func mergeExecution(intent *taskflow.CreateTaskReq, config *taskflow.TaskExecutionConfig) error {
	if config == nil {
		return nil
	}
	if config.AgentResources != nil {
		intent.AgentResources = config.AgentResources
	}
	if config.LLM != nil {
		intent.LLM = *config.LLM
	}
	if intent.Env == nil {
		intent.Env = map[string]string{}
	}
	for key, value := range config.Envs {
		intent.Env[key] = value
	}
	for _, file := range config.ConfigFiles {
		found := false
		for index := range intent.Configs {
			if intent.Configs[index].Path != file.Path {
				continue
			}
			// A model switch leaves installed plugin selections intact.
			if file.Path == "~/.config/opencode/opencode.json" {
				var existing, next map[string]json.RawMessage
				if json.Unmarshal([]byte(intent.Configs[index].Content), &existing) != nil || json.Unmarshal([]byte(file.Content), &next) != nil {
					return errors.New("invalid OpenCode execution configuration")
				}
				if _, supplied := next["plugin"]; !supplied {
					if plugins, ok := existing["plugin"]; ok {
						next["plugin"] = plugins
					}
				}
				data, err := json.Marshal(next)
				if err != nil {
					return err
				}
				file.Content = string(data)
			}
			intent.Configs[index] = file
			found = true
			break
		}
		if !found {
			intent.Configs = append(intent.Configs, file)
		}
	}
	if config.McpServers != nil {
		intent.McpConfigs = config.McpServers
	}
	return nil
}

// Restart admission and the cancellation fence are committed together. A
// repeated request ID observes the original result rather than repeating reset.
func (t *taskClient) restart(ctx context.Context, env Environment, request taskflow.RestartTaskReq) (*taskflow.RestartTaskResp, error) {
	if env.State != "online" || env.SandboxID == "" {
		return nil, errors.New("task environment is not ready for restart")
	}
	if request.RequestId == "" {
		request.RequestId = uuid.NewString()
	}
	if len(request.RequestId) > 256 {
		return nil, errors.New("restart request ID exceeds limit")
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(request.ID.String()+":restart:"+request.RequestId)).String()
	tx, err := t.c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var raw []byte
	if err = tx.QueryRowContext(ctx, `SELECT payload FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, request.ID).Scan(&raw); err != nil {
		return nil, err
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM runtime_environments WHERE id=$1 FOR UPDATE`, env.ID).Scan(&state); err != nil {
		return nil, err
	}
	if state != "online" {
		return nil, errors.New("task environment is stopping or unavailable")
	}
	var previous []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM runtime_commands WHERE id=$1`, id).Scan(&previous)
	if err == nil {
		decoded, err := t.c.ledger.open(id, previous)
		if err != nil {
			return nil, err
		}
		var prior restartCommand
		if err = json.Unmarshal(decoded, &prior); err != nil {
			return nil, err
		}
		if hash(mustJSON(prior.Request)) != hash(mustJSON(request)) {
			return nil, &taskflow.RestartPendingError{Err: errors.New("restart request ID payload conflict")}
		}
		if err = tx.Commit(); err != nil {
			return nil, &taskflow.RestartPendingError{Err: err}
		}
		return t.waitRestart(ctx, id)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	decoded, err := t.c.ledger.open(request.ID.String(), raw)
	if err != nil {
		return nil, err
	}
	var intent taskflow.CreateTaskReq
	if err = json.Unmarshal(decoded, &intent); err != nil {
		return nil, err
	}
	if _, err = runtimeProvider(intent.CodingAgent); err != nil {
		return nil, err
	}
	if err = mergeExecution(&intent, request.ExecutionConfig); err != nil {
		return nil, err
	}
	if request.ExecutionConfig != nil && request.ExecutionConfig.McpServers != nil {
		intent.McpConfigs = t.c.runtimeMCPConfigs(intent.McpConfigs)
	}
	if _, err = projectSpec(env, intent, t.c.nodes[env.NodeID].GuestImage); err != nil {
		return nil, err
	}
	var active, turn int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE state IN ('pending','submitting','unknown','running')),COALESCE(max(turn),0) FROM runtime_commands WHERE task_id=$1 AND operation='restart'`, request.ID).Scan(&active, &turn); err != nil {
		return nil, err
	}
	if active != 0 {
		return nil, errors.New("task already has a pending restart")
	}
	if err = taskmutation.ValidateRestart(ctx, tx, request.ID.String(), env.OwnerID, request); err != nil {
		return nil, err
	}
	payload := mustJSON(restartCommand{Request: request, Intent: intent})
	sealed, err := t.c.ledger.seal(id, payload)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_commands(id,environment_id,task_id,operation,turn,payload,payload_hash) VALUES($1,$2,$3,'restart',$4,$5,$6)`, id, env.ID, request.ID, turn+1, sealed, hash(payload)); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET cancel_requested=true WHERE task_id=$1 AND operation='task' AND state IN ('pending','submitting','unknown','running')`, request.ID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, &taskflow.RestartPendingError{Err: err}
	}
	return t.waitRestart(ctx, id)
}

func (t *taskClient) waitRestart(ctx context.Context, id string) (*taskflow.RestartTaskResp, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		var state string
		var data []byte
		if err := t.c.ledger.db.QueryRowContext(ctx, `SELECT state,result FROM runtime_commands WHERE id=$1`, id).Scan(&state, &data); err != nil {
			return nil, &taskflow.RestartPendingError{Err: err}
		}
		if state == "complete" {
			var response taskflow.RestartTaskResp
			if err := json.Unmarshal(data, &response); err != nil {
				return nil, &taskflow.RestartPendingError{Err: err}
			}
			return &response, nil
		}
		if state == "failed" || state == "canceled" {
			return nil, errors.New("remote Agent restart did not complete")
		}
		select {
		case <-ctx.Done():
			return nil, &taskflow.RestartPendingError{Err: ctx.Err()}
		case <-tick.C:
		}
	}
}

func (c *Client) processRestart(ctx context.Context, command *Command, env Environment, engine *Engine) error {
	var payload restartCommand
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		return err
	}
	if env.State != "online" {
		return c.ledger.UpdateCommand(ctx, *command, "canceled", "", 0)
	}
	var canceled bool
	if err := c.ledger.db.QueryRowContext(ctx, `SELECT cancel_requested FROM runtime_commands WHERE id=$1`, command.ID).Scan(&canceled); err != nil {
		return err
	}
	if canceled {
		return c.ledger.UpdateCommand(ctx, *command, "canceled", "", 0)
	}
	active, err := c.ledger.ActiveCommands(ctx, command.TaskID)
	if err != nil {
		return err
	}
	for _, pending := range active {
		if pending.ID == command.ID {
			continue
		}
		if pending.RunID != "" {
			if _, err = engine.runs.StopRun(ctx, connect.NewRequest(&v2.StopRunRequest{RunId: pending.RunID})); err != nil {
				return err
			}
		}
		// The normal Worker confirms terminal status and drains every event.
		return c.ledger.UpdateCommand(ctx, *command, "pending", "", 0)
	}
	spec, err := projectSpec(env, payload.Intent, c.nodes[env.NodeID].GuestImage)
	if err != nil {
		return err
	}
	// Project, resource and config changes cannot form a distributed database
	// transaction. Cross the durable mutation boundary before the first RPC;
	// later failures retain the restart fence until reconciliation completes.
	if !command.Submitted {
		if err = c.ledger.BeginSubmission(ctx, command); errors.Is(err, errCanceledBeforeAdmission) {
			return c.ledger.UpdateCommand(ctx, *command, "canceled", "", 0)
		} else if err != nil {
			return err
		}
	}
	applied, err := engine.projects.ApplyProject(ctx, connect.NewRequest(&v2.ApplyProjectRequest{Spec: spec}))
	if err != nil {
		return err
	}
	for _, issue := range applied.Msg.Issues {
		if issue.Severity == v2.ProjectValidationSeverity_PROJECT_VALIDATION_SEVERITY_ERROR {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("restart project configuration rejected"))
		}
	}
	if applied.Msg.GetProject().GetSummary().GetProjectId() != env.ProjectID {
		return errors.New("restart changed runtime project identity")
	}
	if err = c.writeTaskConfigs(ctx, env, payload.Intent); err != nil {
		return err
	}
	provider, err := runtimeProvider(payload.Intent.CodingAgent)
	if err != nil {
		return err
	}
	session, err := engine.session(ctx, env.SandboxID, map[string]any{"op": "restart", "provider": provider, "command_id": command.ID, "load_session": payload.Request.LoadSession})
	if err != nil {
		return err
	}
	response := taskflow.RestartTaskResp{ID: payload.Request.ID, RequestId: payload.Request.RequestId, Success: true, Message: "Remote Agent restarted", SessionID: session.ID}
	return c.finishRestart(ctx, *command, payload.Intent, response)
}

func (c *Client) finishRestart(ctx context.Context, command Command, intent taskflow.CreateTaskReq, response taskflow.RestartTaskResp) error {
	var payload restartCommand
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		return err
	}
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var canceled bool
	if err = tx.QueryRowContext(ctx, `SELECT cancel_requested FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, command.TaskID).Scan(&canceled); err != nil {
		return err
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM runtime_environments WHERE id=$1 FOR UPDATE`, command.EnvironmentID).Scan(&state); err != nil {
		return err
	}
	if err = lockCommand(ctx, tx, command); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT cancel_requested FROM runtime_commands WHERE id=$1`, command.ID).Scan(&canceled); err != nil {
		return err
	}
	if state != "online" || canceled {
		response.Success = false
		response.Message = "Remote Agent restart was canceled"
		if err = taskmutation.ApplyRestart(ctx, tx, command.TaskID, payload.Request.BusinessMutation, response); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET state='canceled',lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1`, command.ID); err != nil {
			return err
		}
		return tx.Commit()
	}
	data := mustJSON(intent)
	sealed, err := c.ledger.seal(command.TaskID, data)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_task_intents SET payload=$2,payload_hash=$3,cancel_requested=false WHERE task_id=$1`, command.TaskID, sealed, hash(data)); err != nil {
		return err
	}
	provider, err := runtimeProvider(intent.CodingAgent)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_task_sessions(task_id,provider,session_id,command_id) VALUES($1,$2,$3,$4) ON CONFLICT(task_id) DO UPDATE SET provider=EXCLUDED.provider,session_id=EXCLUDED.session_id,command_id=EXCLUDED.command_id,updated_at=now()`, command.TaskID, provider, response.SessionID, command.ID); err != nil {
		return err
	}
	if err = taskmutation.ApplyRestart(ctx, tx, command.TaskID, payload.Request.BusinessMutation, response); err != nil {
		return err
	}
	response.BusinessStateCommitted = payload.Request.BusinessMutation != nil
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET state='complete',result=$2,lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1`, command.ID, string(mustJSON(response))); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Client) writeTaskConfigs(ctx context.Context, env Environment, task taskflow.CreateTaskReq) error {
	_, engine, err := c.environment(ctx, env.ID)
	if err != nil {
		return err
	}
	if err := c.writeGitCredentialBridge(ctx, env, task); err != nil {
		return err
	}
	if len(task.Configs) == 0 {
		if err := engine.resources(ctx, env.SandboxID, task.AgentResources); err != nil {
			return err
		}
		if err := c.writeNativeModel(ctx, env, task); err != nil {
			return err
		}
		return c.writeNativeRules(ctx, env, task, "")
	}
	var home struct {
		Path string `json:"path"`
	}
	if err := engine.files(ctx, env.SandboxID, map[string]string{"op": "home"}, &home); err != nil {
		return err
	}
	prepared := make([]taskflow.ConfigFile, 0, len(task.Configs))
	for _, file := range task.Configs {
		file.Path = guestConfigPath(file.Path, home.Path)
		if file.Path == "~/.config/opencode/opencode.json" || file.Path == home.Path+"/.config/opencode/opencode.json" {
			var err error
			file.Content, err = renderOpenCodePaths(file.Content, home.Path, task.AgentResources)
			if err != nil {
				return err
			}
		}
		prepared = append(prepared, file)
	}
	if err := engine.resources(ctx, env.SandboxID, task.AgentResources); err != nil {
		return err
	}
	for _, file := range prepared {
		chunks := make(chan []byte, 1)
		chunks <- []byte(file.Content)
		close(chunks)
		mode := uint32(0600)
		if file.Mode != nil {
			mode = *file.Mode
		}
		if err := (&fileClient{c}).upload(ctx, taskflow.FileReq{ID: env.ID, Path: file.Path, UserID: env.OwnerID}, chunks, &mode, true); err != nil {
			return err
		}
	}
	if err := c.writeNativeModel(ctx, env, task); err != nil {
		return err
	}
	return c.writeNativeRules(ctx, env, task, home.Path)
}

// Failed/canceled controls settle the business audit in the same transaction as
// the runtime fence. In particular, HTTP timeout never performs this write.
func (s *Ledger) finishRestartFailure(ctx context.Context, command Command, state string) error {
	var payload restartCommand
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var task string
	if err = tx.QueryRowContext(ctx, `SELECT task_id FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, command.TaskID).Scan(&task); err != nil {
		return err
	}
	if err = lockCommand(ctx, tx, command); err != nil {
		return err
	}
	response := taskflow.RestartTaskResp{ID: payload.Request.ID, RequestId: payload.Request.RequestId, Success: false, Message: "Remote Agent restart " + state}
	if err = taskmutation.ApplyRestart(ctx, tx, command.TaskID, payload.Request.BusinessMutation, response); err != nil {
		return err
	}
	response.BusinessStateCommitted = payload.Request.BusinessMutation != nil
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_commands SET state=$2,result=$3,lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1`, command.ID, state, string(mustJSON(response))); err != nil {
		return err
	}
	return tx.Commit()
}

// ResumeRestart observes an already admitted request before the usecase touches
// model-proxy credentials or regenerates a configuration from mutable resources.
// It never admits work and deliberately compares only the authorized business
// selection; the original encrypted execution configuration remains authoritative.
func (t *taskClient) ResumeRestart(ctx context.Context, request taskflow.RestartTaskReq) (*taskflow.RestartTaskResp, bool, error) {
	if request.RequestId == "" {
		return nil, false, nil
	}
	env, err := t.c.ledger.EnvironmentForTask(ctx, request.ID.String())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, &taskflow.RestartPendingError{Err: err}
	}
	if request.BusinessMutation != nil && request.BusinessMutation.OwnerID.String() != env.OwnerID {
		return nil, true, errors.New("restart business mutation owner mismatch")
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(request.ID.String()+":restart:"+request.RequestId)).String()
	var sealed []byte
	err = t.c.ledger.db.QueryRowContext(ctx, `SELECT payload FROM runtime_commands WHERE id=$1 AND task_id=$2 AND operation='restart'`, id, request.ID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, &taskflow.RestartPendingError{Err: err}
	}
	plain, err := t.c.ledger.open(id, sealed)
	if err != nil {
		return nil, true, &taskflow.RestartPendingError{Err: err}
	}
	var previous restartCommand
	if err = json.Unmarshal(plain, &previous); err != nil {
		return nil, true, &taskflow.RestartPendingError{Err: err}
	}
	if previous.Request.LoadSession != request.LoadSession || hash(mustJSON(previous.Request.BusinessMutation)) != hash(mustJSON(request.BusinessMutation)) {
		return nil, true, errors.New("restart request ID payload conflict")
	}
	response, err := t.waitRestart(ctx, id)
	return response, true, err
}
