package runtimeadapter

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

func marshal(v any) ([]byte, error) { return json.Marshal(v) }

// RunWorker owns background execution. API/browser contexts never own a Run.
func (c *Client) RunWorker(ctx context.Context) error {
	tick := time.NewTicker(c.poll)
	defer tick.Stop()
	for {
		if err := c.Step(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, context.Canceled) {
			c.logger.WarnContext(ctx, "runtime worker iteration failed; durable command retained")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}
func (c *Client) Step(ctx context.Context) error {
	cmd, err := c.ledger.Claim(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	err = c.process(ctx, &cmd)
	if errors.Is(err, errWorkQueued) {

		return nil
	}
	if err != nil {
		if c.logger != nil {
			// RPC diagnostics can reflect model credentials; log safe correlation data.
			c.logger.WarnContext(ctx, "runtime command requires reconciliation", "command_id", cmd.ID, "environment_id", cmd.EnvironmentID, "task_id", cmd.TaskID, "operation", cmd.Operation, "run_id", cmd.RunID, "rpc_code", connect.CodeOf(err).String())
		}
		if errors.Is(err, errLeaseLost) {
			return err
		}
		cleanup, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cleanupCancel()
		if errors.Is(err, errCanceledBeforeAdmission) {
			return c.ledger.Finish(cleanup, cmd, "canceled", "")
		}
		var policyError *schedulingError
		if errors.As(err, &policyError) && !cmd.Submitted {
			if cmd.Operation == "task" {
				return c.waitContinuation(cleanup, cmd, "failed", policyError.Error(), true, nil)
			}
			return c.ledger.Finish(cleanup, cmd, "failed", policyError.Error())
		}
		if cmd.Operation == "task" && !cmd.Submitted && c.ledger.workEnabled {
			var phase string
			if checkErr := c.ledger.db.QueryRowContext(cleanup, `SELECT preparation_phase FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&phase); checkErr == nil && phase != "" {
				var expired bool
				if checkErr = c.ledger.db.QueryRowContext(cleanup, `SELECT COALESCE(preparation_started_at<now()-interval '5 minutes',false) FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&expired); checkErr != nil {
					return checkErr
				}
				return c.waitContinuation(cleanup, cmd, "preparing", "执行环境准备失败，正在重试；请检查模型配置、技能包、附件和运行容量。", expired, nil)
			}
		}
		// A validation error after admission cannot prove that the Run stopped.
		if !cmd.Submitted && (connect.CodeOf(err) == connect.CodeInvalidArgument || errors.Is(err, ErrParity)) {
			if cmd.Operation == "restart" || cmd.Operation == "interaction" || cmd.Operation == "approval" {
				return c.ledger.UpdateCommand(cleanup, cmd, "failed", "", 0)
			}
			if finalErr := c.ledger.Finish(cleanup, cmd, "failed", "Remote runtime compatibility check failed; review server configuration"); finalErr != nil {
				return finalErr
			}
			return err
		}
		if updateErr := c.ledger.UpdateCommand(cleanup, cmd, "unknown", cmd.RunID, cmd.Offset); updateErr != nil {
			return updateErr
		}
	}
	return err
}
func (c *Client) process(ctx context.Context, cmd *Command) error {
	if cmd.State == "waiting" {
		return c.prepareContinuation(ctx, cmd)
	}
	e, err := c.ledger.Environment(ctx, cmd.EnvironmentID)
	if err != nil {
		return err
	}
	n := c.engine(e.NodeID)
	workRevoked := false
	if c.workGuard != nil && cmd.TaskID != "" {
		uid, parseErr := uuid.Parse(e.OwnerID)
		tid, taskErr := uuid.Parse(cmd.TaskID)
		if parseErr != nil || taskErr != nil {
			return errors.New("invalid work owner")
		}
		if accessErr := c.workGuard.CheckWorkTaskAccess(ctx, uid, tid, true); accessErr != nil {
			workRevoked = true
			if _, err = c.ledger.db.ExecContext(ctx, `UPDATE runtime_commands SET cancel_requested=true WHERE id=$1`, cmd.ID); err != nil {
				return err
			}
		}
	}
	if workRevoked && (cmd.Operation == "restart" || cmd.Operation == "interaction" || cmd.Operation == "approval") {
		// Do not replay a submitted answer, approval or model control after its
		// owner's grant is removed. Stop the Guest before settling the command.
		if e.SandboxID != "" && n != nil {
			if _, err = n.sandboxes.StopSandbox(ctx, connect.NewRequest(&v2.StopSandboxRequest{SandboxId: e.SandboxID})); err != nil {
				return err
			}
		}
		return c.ledger.UpdateCommand(ctx, *cmd, "canceled", cmd.RunID, cmd.Offset)
	}
	var canceled bool
	if err = c.ledger.db.QueryRowContext(ctx, `SELECT cancel_requested FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&canceled); err != nil {
		return err
	}
	if canceled && !cmd.Submitted {
		if cmd.Operation == "restart" || cmd.Operation == "interaction" || cmd.Operation == "approval" {
			return c.ledger.UpdateCommand(ctx, *cmd, "canceled", cmd.RunID, cmd.Offset)
		}
		return c.ledger.Finish(ctx, *cmd, "canceled", "")
	}
	if c.ledger.workEnabled && (cmd.Operation == "prepare" || cmd.Operation == "restore_prepare" || cmd.Operation == "task") && !cmd.Submitted {
		if err = c.acquireWorkSlot(ctx, *cmd); err != nil {
			return err
		}
	}
	if c.ledger.workEnabled && !cmd.Submitted && (cmd.Operation == "prepare" || cmd.Operation == "restore_prepare" || cmd.Operation == "task") {
		if err = c.scheduleEnvironment(ctx, *cmd, e); err != nil {
			return err
		}
	} else if err = c.ledger.ensureReservation(ctx, e.ID); err != nil {
		return err
	}
	e, n, err = c.environmentUnchecked(ctx, cmd.EnvironmentID)
	if err != nil {
		return err
	}

	if cmd.Operation == "restart" {
		return c.processRestart(ctx, cmd, e, n)
	}
	if cmd.Operation == "interaction" {
		return c.processInteraction(ctx, cmd, e, n)
	}
	if cmd.Operation == "approval" {
		return c.processApproval(ctx, cmd, e, n)
	}
	var task taskflow.CreateTaskReq
	if err = json.Unmarshal(cmd.Payload, &task); err != nil {
		return err
	}
	isPrepare := cmd.Operation == "prepare" || cmd.Operation == "restore_prepare"
	if !isPrepare && cmd.Operation != "task" {
		return ErrParity
	}
	if err = c.ledger.db.QueryRowContext(ctx, `SELECT cancel_requested FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&canceled); err != nil {
		return err
	}
	if canceled && !cmd.Submitted {
		return c.ledger.Finish(ctx, *cmd, "canceled", "")
	}
	if c.ledger.workEnabled {
		var work bool
		if err = c.ledger.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_tasks WHERE task_id=$1)`, cmd.TaskID).Scan(&work); err != nil {
			return err
		}
		if work {
			if task.Env == nil {
				task.Env = map[string]string{}
			}
			task.Env["JINGJIAAGENT_WORK_MODE"] = "1"
		}
		if !cmd.Submitted {
			if err = c.refreshWorkResources(ctx, &task); err != nil {
				return err
			}
		}
	}
	if err = c.refreshRestoreWorkspace(ctx, &e, *cmd); err != nil {
		return err
	}
	if isPrepare && (e.ProjectID == "" || (cmd.Operation == "restore_prepare" && !cmd.Submitted)) {
		spec, err := c.projectSpec(e, task)
		if err != nil {
			return err
		}
		res, err := n.projects.ApplyProject(ctx, connect.NewRequest(&v2.ApplyProjectRequest{Spec: spec}))
		if err != nil {
			return err
		}
		for _, issue := range res.Msg.Issues {
			if issue.Severity == v2.ProjectValidationSeverity_PROJECT_VALIDATION_SEVERITY_ERROR {
				return connect.NewError(connect.CodeInvalidArgument, errors.New("runtime project specification was rejected"))
			}
		}
		e.ProjectID = res.Msg.GetProject().GetSummary().GetProjectId()
		if e.ProjectID == "" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("runtime did not apply the project specification"))
		}
		if err = c.ledger.SetEnvironment(ctx, e.ID, e.ProjectID, e.SandboxID, e.State); err != nil {
			return err
		}
	}
	// Reconcile a lost admission response before touching Guest configuration.
	if cmd.RunID == "" && cmd.Submitted {
		found, err := n.runs.ListRuns(ctx, connect.NewRequest(&v2.ListRunsRequest{ProjectId: e.ProjectID, AgentName: "worker", Source: v2.RunSource_RUN_SOURCE_API, Labels: map[string]string{"jingjiaagent_command": cmd.ID}, Limit: 2}))
		if err != nil {
			return err
		}
		if len(found.Msg.Runs) > 1 {
			return errors.New("runtime returned conflicting admission records")
		}
		if len(found.Msg.Runs) == 1 {
			cmd.RunID = found.Msg.Runs[0].RunId
			if err = c.ledger.SaveAdmission(ctx, *cmd); err != nil {
				return err
			}
		} else if canceled {
			return errAdmissionUncertain
		}
	}
	if cmd.Operation == "task" {
		if e.SandboxID == "" {
			return errors.New("prepared sandbox is not available")
		}
		if !cmd.Submitted {
			if task.ModelSelectionID != "" {
				// Apply this accepted round's selection only when it is about to
				// execute. Saving a future selection never changes an active Run.
				spec, err := c.projectSpec(e, task)
				if err != nil {
					return err
				}
				applied, err := n.projects.ApplyProject(ctx, connect.NewRequest(&v2.ApplyProjectRequest{Spec: spec}))
				if err != nil {
					return err
				}
				for _, issue := range applied.Msg.Issues {
					if issue.Severity == v2.ProjectValidationSeverity_PROJECT_VALIDATION_SEVERITY_ERROR {
						return connect.NewError(connect.CodeInvalidArgument, errors.New("selected model execution configuration rejected"))
					}
				}
				if applied.Msg.GetProject().GetSummary().GetProjectId() != e.ProjectID {
					return errors.New("model selection changed runtime project identity")
				}
			}
			if err = c.prepareTaskSandbox(ctx, e, *cmd, n); err != nil {
				return err
			}
			if err = c.applyContextReset(ctx, e, *cmd, task, n); err != nil {
				return err
			}
			promptSession, err := c.preparePromptSession(ctx, n, e.SandboxID, *cmd, &task)
			if err != nil {
				return err
			}
			if c.ledger.workEnabled && cmd.Turn > 1 {
				var phase string
				if err = c.ledger.db.QueryRowContext(ctx, `SELECT preparation_phase FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&phase); err != nil {
					return err
				}
				if phase != "" {
					if promptSession == nil {
						provider, err := runtimeProvider(task.CodingAgent)
						if err != nil {
							return err
						}
						session, err := n.session(ctx, e.SandboxID, map[string]any{"op": "read", "provider": provider})
						if err != nil {
							return err
						}
						promptSession = &session
					}
					if promptSession.ID == "" {
						task.Recovered = true
						if err = c.freezeRecoveryContext(ctx, cmd, &task); err != nil {
							return err
						}
					} else {
						// A persisted business-history projection is not evidence of
						// native session loss. Keep ordinary continuations untouched.
						task.Recovered, task.RecoveryContext, task.RecoveryInputs = false, "", nil
						task.RecoveryHistory = nil
					}
				}
			}
			if err = c.writeTaskConfigs(ctx, e, task); err != nil {
				return err
			}
			originalText := task.Text
			if err = c.stageWorkInputs(ctx, e, *cmd, &task); err != nil {
				return err
			}
			if err = c.stageRecoveryHistory(ctx, e, *cmd, &task); err != nil {
				return err
			}
			if err = c.refreshRoundAttachments(ctx, e.OwnerID, &task); err != nil {
				return err
			}
			if err = n.attachments(ctx, e.SandboxID, *cmd, task); err != nil {
				return err
			}
			if err = c.hydrateCanonicalOutputs(ctx, e); err != nil {
				return err
			}
			if cmd.Turn > 1 && (c.ledger.workEnabled || promptSession != nil) {
				// Freeze actual recovery locations before admission. A lost Run
				// response must retry this same prompt, without another index or
				// wrapping the user text a second time.
				prepared := task
				prepared.Text = originalText
				if err = c.persistPreparedRound(ctx, cmd, prepared); err != nil {
					return err
				}
			}
		}
		// Fallback for commands persisted before transactional input echoes.
		if cmd.Submitted {
			if err = c.stageWorkInputs(ctx, e, *cmd, &task); err != nil {
				return err
			}
		}
		if err = c.ledger.Append(ctx, *cmd, "user-input", taskflow.TaskChunk{Event: "user-input", Data: mustJSON(map[string]any{"content": []byte(task.InputText()), "attachments": c.publicAttachments(e.OwnerID, task.Attachments), "client_message_id": task.ClientMessageID, "skill_mention": task.SkillMention})}); err != nil {
			return err
		}
	}
	if cmd.RunID == "" {
		// Staging inputs and configuring a Guest may take time. Recheck live
		// grants immediately before a first admission, while preserving the
		// reconciliation path for requests already submitted to the daemon.
		if !cmd.Submitted {
			if err = c.checkWorkEnvironmentAccess(ctx, e, true); err != nil {
				return c.ledger.Finish(ctx, *cmd, "canceled", "")
			}
		}
		r := &v2.RunAgentRequest{ProjectId: e.ProjectID, AgentName: "worker", Source: v2.RunSource_RUN_SOURCE_API, ClientRequestId: cmd.ID, CleanupPolicy: v2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING, Labels: map[string]string{"jingjiaagent_environment": e.ID, "jingjiaagent_command": cmd.ID}}
		if isPrepare {
			r.Command = "true"
		} else {
			r.Prompt = executionPrompt(task, cmd.Turn)
			r.SandboxId = e.SandboxID
		}
		if err = c.ledger.BeginSubmission(ctx, cmd); errors.Is(err, errCanceledBeforeAdmission) {
			return c.ledger.Finish(ctx, *cmd, "canceled", "")
		} else if err != nil {
			return err
		}
		res, err := n.runs.StartAgentRun(ctx, connect.NewRequest(&v2.StartAgentRunRequest{Run: r}))
		if err != nil {
			return err
		}
		cmd.RunID = res.Msg.GetRun().GetRunId()
		if cmd.RunID == "" {
			return errors.New("runtime did not return run ID")
		}
		// Save admission before any event, callback or guest operation.
		err = c.ledger.SaveAdmission(ctx, *cmd)
		if err != nil {
			return err
		}
	}
	if err = c.ledger.db.QueryRowContext(ctx, `SELECT cancel_requested FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&canceled); err != nil {
		return err
	}
	if canceled {
		if _, err = n.runs.StopRun(ctx, connect.NewRequest(&v2.StopRunRequest{RunId: cmd.RunID})); err != nil {
			return err
		}
		if workRevoked && e.SandboxID != "" {
			if _, err = n.sandboxes.StopSandbox(ctx, connect.NewRequest(&v2.StopSandboxRequest{SandboxId: e.SandboxID})); err != nil {
				return err
			}
		}
	}
	run, err := n.runs.GetRun(ctx, connect.NewRequest(&v2.GetRunRequest{RunId: cmd.RunID}))
	if err != nil {
		return err
	}
	summary := run.Msg.GetRun().GetSummary()
	if summary == nil {
		return errors.New("runtime run summary is missing")
	}
	if summary.SandboxId != "" && e.SandboxID == "" {
		e.SandboxID = summary.SandboxId
		if err = c.ledger.SetEnvironment(ctx, e.ID, e.ProjectID, e.SandboxID, "pending"); err != nil {
			return err
		}
	}
	if cmd.Operation == "task" {
		events, err := n.runs.ListRunEvents(ctx, connect.NewRequest(&v2.ListRunEventsRequest{RunId: cmd.RunID, Offset: uint32(cmd.Offset), Limit: 500}))
		if err != nil {
			return err
		}
		for _, event := range events.Msg.Events {
			if err = c.ingestEvent(ctx, *cmd, event); err != nil {
				return err
			}
			cmd.Offset++
		}
		// Drain every page before committing a terminal state.
		if cmd.Offset < int64(events.Msg.Total) {
			return c.ledger.UpdateCommand(ctx, *cmd, "running", cmd.RunID, cmd.Offset)
		}
	}
	terminal := summary.Status == v2.RunStatus_RUN_STATUS_SUCCEEDED || summary.Status == v2.RunStatus_RUN_STATUS_FAILED || summary.Status == v2.RunStatus_RUN_STATUS_CANCELED
	if !terminal {
		return c.ledger.UpdateCommand(ctx, *cmd, "running", cmd.RunID, cmd.Offset)
	}
	state := "complete"
	if summary.Status == v2.RunStatus_RUN_STATUS_FAILED {
		state = "failed"
	}
	if summary.Status == v2.RunStatus_RUN_STATUS_CANCELED {
		state = "canceled"
	}
	if isPrepare && canceled {
		state = "canceled"
	}
	if isPrepare {
		if state != "complete" {
			if err = c.ledger.SetEnvironment(ctx, e.ID, e.ProjectID, e.SandboxID, "offline"); err != nil {
				return err
			}
			return c.ledger.Finish(ctx, *cmd, state, "Remote environment preparation did not complete")
		}
		if err = c.ledger.SetEnvironment(ctx, e.ID, e.ProjectID, e.SandboxID, "online"); err != nil {
			return err
		}
		e.State = "online"
		if cmd.Operation == "restore_prepare" {
			if err = c.commitRestore(ctx, *cmd, e); err != nil {
				return err
			}
		}
		if err = c.callback(ctx, "/internal/vm-info", projectVM(e)); err != nil {
			return err
		}
		if cmd.Operation != "restore_prepare" {
			if err = c.callback(ctx, "/internal/vm-ready", projectVM(e)); err != nil {
				return err
			}
		}
	} else {
		var metadata struct {
			Provider  string `json:"agent"`
			SessionID string `json:"agentThreadId"`
		}
		if result := run.Msg.GetRun().GetResultJson(); result != "" {
			if json.Unmarshal([]byte(result), &metadata) != nil || len(metadata.SessionID) > 256 || strings.ContainsAny(metadata.SessionID, "\x00\r\n") {
				return errors.New("invalid runtime Agent session result")
			}
			if metadata.SessionID != "" {
				if err = c.ledger.SaveSession(ctx, *cmd, metadata.Provider, metadata.SessionID); err != nil {
					return err
				}
			}
		}
		message := ""
		if state == "failed" {
			message = "Remote Agent run failed"
		}
		return c.ledger.Finish(ctx, *cmd, state, message)
	}
	return c.ledger.UpdateCommand(ctx, *cmd, state, cmd.RunID, cmd.Offset)
}

func (c *Client) prepareTaskSandbox(ctx context.Context, e Environment, cmd Command, n *Engine) error {
	// Only a new durable user turn may resume a Guest fenced after a
	// daemon interruption. Polling an admitted Run must never do so.
	sandbox, err := n.sandboxes.GetSandbox(ctx, connect.NewRequest(&v2.GetSandboxRequest{SandboxId: e.SandboxID}))
	if err != nil {
		return err
	}
	if sandbox.Msg.GetSandbox().GetStatus() == v2.SandboxStatus_SANDBOX_STATUS_STOPPED {
		if err = c.setContinuationPhase(ctx, cmd, "waking"); err != nil {
			return err
		}
		if _, err = n.sandboxes.ResumeSandbox(ctx, connect.NewRequest(&v2.ResumeSandboxRequest{SandboxId: e.SandboxID})); err != nil {
			return err
		}
	}
	// Also clears waking when a worker retries after a successful Resume RPC.
	return c.setContinuationPhase(ctx, cmd, "starting")
}

func projectSpec(e Environment, t taskflow.CreateTaskReq, fallbackImage string) (*v2.ProjectSpec, error) {
	provider := "opencode"
	switch t.CodingAgent {
	case taskflow.CodingAgentCodex:
		provider = "codex"
	case taskflow.CodingAgentClaude:
		provider = "claude"
	case 0, taskflow.CodingAgentOpenCode:
	default:
		return nil, ErrParity
	}
	image := e.Request.ImageURL
	if image == "" {
		image = fallbackImage
	}
	if image == "" {
		return nil, errors.New("runtime Guest image is required")
	}
	var workspace *v2.WorkspaceSpec
	if e.Request.Git.URL != "" {
		workspace = &v2.WorkspaceSpec{Provider: "git", Url: e.Request.Git.URL, Ref: e.Request.Git.Branch, Username: e.Request.Git.Username, Token: e.Request.Git.Token}
	}
	if e.Request.ZipUrl != "" {
		workspace = &v2.WorkspaceSpec{Provider: "http", Url: e.Request.ZipUrl, Format: "zip"}
	}
	env := map[string]string{}
	for _, item := range e.Request.Envs {
		key, value, ok := strings.Cut(item, "=")
		// Project business variables are available to both work and coding
		// tasks, but cannot replace platform/runtime control variables.
		if ok && !strings.HasPrefix(key, "JINGJIAAGENT_") && !strings.HasPrefix(key, "AGENT_COMPOSE_") && !strings.HasPrefix(key, "LLM_") {
			env[key] = value
		}
	}
	for k, value := range t.Env {
		env[k] = value
	}
	// Vendor names imply a fixed first-party protocol in agent-compose and
	// take precedence over LLM_API_PROTOCOL. Declare the selected business
	// model through the generic contract, regardless of the native CLI.
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "DEEPSEEK_API_KEY", "OPENROUTER_API_KEY", "AZURE_OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_BASE_URL", "OPENAI_MODEL", "ANTHROPIC_MODEL"} {
		delete(env, key)
	}
	env["LLM_API_KEY"] = t.LLM.ApiKey
	env["LLM_API_ENDPOINT"] = t.LLM.BaseURL
	env["LLM_MODEL"] = t.LLM.Model
	// Assigned after merging inputs: Agent-controlled variables cannot select
	// another task's persisted approval policy.
	env["JINGJIAAGENT_TASK_ID"] = t.ID.String()
	// Native SDK rewriting and original CLI settings must not mix a facade
	// credential with the product model-proxy endpoint. Secret flags below
	// apply equally to these immutable per-environment credential variables.
	env["JINGJIAAGENT_MODEL_API_KEY"] = t.LLM.ApiKey
	env["JINGJIAAGENT_MODEL_BASE_URL"] = t.LLM.BaseURL
	env["JINGJIAAGENT_MODEL_NAME"] = t.LLM.Model
	switch t.LLM.ApiType {
	case "", "openai_chat":
		env["LLM_API_PROTOCOL"] = "chat_completions"
	case "openai_responses":
		env["LLM_API_PROTOCOL"] = "responses"
	case "anthropic":
		env["LLM_API_PROTOCOL"] = "anthropic_messages"
	default:
		return nil, ErrParity
	}
	cpu, memory, err := requestedResources(e.Request)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	env["JINGJIAAGENT_SANDBOX_CPUS"] = strconv.FormatInt(cpu/1000, 10)
	if cpu%1000 != 0 {
		env["JINGJIAAGENT_SANDBOX_CPUS"] += "." + strings.TrimRight(fmt.Sprintf("%03d", cpu%1000), "0")
	}
	env["JINGJIAAGENT_SANDBOX_MEMORY"] = strconv.FormatInt(memory, 10)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	vars := []*v2.EnvVarSpec{}
	for _, k := range keys {
		vars = append(vars, &v2.EnvVarSpec{Name: k, Value: env[k], Secret: true})
	}
	mcps := []*v2.MCPServerSpec{}
	for _, m := range t.McpConfigs {
		spec := &v2.MCPServerSpec{Name: m.Name, Type: "remote", Transport: m.Type}
		if spec.Transport == "" {
			spec.Transport = "http"
		}
		if m.Command != nil && strings.TrimSpace(*m.Command) != "" {
			spec.Type = "local"
			spec.Transport = ""
			spec.Command = *m.Command
			spec.Args = m.Args
		}
		if m.Url != nil {
			spec.Url = *m.Url
		}
		for key, value := range m.Env {
			spec.Env = append(spec.Env, &v2.EnvVarSpec{Name: key, Value: value, Secret: true})
		}
		for _, header := range m.Headers {
			if header != nil {
				spec.Headers = append(spec.Headers, &v2.EnvVarSpec{Name: header.Name, Value: header.Value, Secret: true})
			}
		}
		mcps = append(mcps, spec)
	}
	return &v2.ProjectSpec{Name: "jingjiaagent-" + e.ID, Agents: []*v2.AgentSpec{{Name: "worker", Provider: provider, Model: t.LLM.Model, SystemPrompt: t.PromptContext.SystemInstructions(), Image: image, Driver: &v2.DriverSpec{Name: "docker", Config: &v2.DriverSpec_Docker{Docker: &v2.DockerDriverSpec{}}}, Workspace: workspace, Env: vars, McpServers: mcps, Sandbox: &v2.SandboxSpec{StoppedRuntimePolicy: "retain"}}}}, nil
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func (c *Client) callback(ctx context.Context, path string, v any) error {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.callbackURL, "/")+path, bytes.NewReader(mustJSON(v)))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+c.callbackToken)
	r.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	var body taskflow.Resp[json.RawMessage]
	if res.StatusCode != 200 || json.Unmarshal(b, &body) != nil || body.Code != 0 {
		return errors.New("runtime lifecycle callback failed")
	}
	return nil
}
