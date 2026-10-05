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
	e, n, err := c.environment(ctx, cmd.EnvironmentID)
	if err != nil {
		return err
	}
	if err = c.ledger.ensureReservation(ctx, e.ID); err != nil {
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
	if cmd.Operation != "prepare" && cmd.Operation != "task" {
		return ErrParity
	}
	var canceled bool
	if err = c.ledger.db.QueryRowContext(ctx, `SELECT cancel_requested FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&canceled); err != nil {
		return err
	}
	if canceled && !cmd.Submitted {
		return c.ledger.Finish(ctx, *cmd, "canceled", "")
	}
	if cmd.Operation == "prepare" && e.ProjectID == "" {
		spec, err := projectSpec(e, task, c.nodes[e.NodeID].GuestImage)
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
			// Only a new durable user turn may resume a Guest fenced after a
			// daemon interruption. Polling an admitted Run must never do so.
			sandbox, err := n.sandboxes.GetSandbox(ctx, connect.NewRequest(&v2.GetSandboxRequest{SandboxId: e.SandboxID}))
			if err != nil {
				return err
			}
			if sandbox.Msg.Sandbox.Status == v2.SandboxStatus_SANDBOX_STATUS_STOPPED {
				if _, err := n.sandboxes.ResumeSandbox(ctx, connect.NewRequest(&v2.ResumeSandboxRequest{SandboxId: e.SandboxID})); err != nil {
					return err
				}
			}
			if err = c.writeTaskConfigs(ctx, e, task); err != nil {
				return err
			}
			if err = n.attachments(ctx, e.SandboxID, *cmd, task); err != nil {
				return err
			}
		}
		// Fallback for commands persisted before transactional input echoes.
		if err = c.ledger.Append(ctx, *cmd, "user-input", taskflow.TaskChunk{Event: "user-input", Data: mustJSON(map[string]any{"content": []byte(task.Text), "attachments": c.publicAttachments(e.OwnerID, task.Attachments), "client_message_id": task.ClientMessageID})}); err != nil {
			return err
		}
	}
	if cmd.RunID == "" {
		r := &v2.RunAgentRequest{ProjectId: e.ProjectID, AgentName: "worker", Source: v2.RunSource_RUN_SOURCE_API, ClientRequestId: cmd.ID, CleanupPolicy: v2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING, Labels: map[string]string{"jingjiaagent_environment": e.ID, "jingjiaagent_command": cmd.ID}}
		if cmd.Operation == "prepare" {
			r.Command = "true"
		} else {
			r.Prompt = task.Text
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
	if cmd.Operation == "prepare" && canceled {
		state = "canceled"
	}
	if cmd.Operation == "prepare" {
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
		if err = c.callback(ctx, "/internal/vm-info", projectVM(e)); err != nil {
			return err
		}
		if err = c.callback(ctx, "/internal/vm-ready", projectVM(e)); err != nil {
			return err
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
		if ok {
			env[key] = value
		}
	}
	for k, value := range t.Env {
		env[k] = value
	}
	env["OPENAI_API_KEY"] = t.LLM.ApiKey
	env["OPENAI_BASE_URL"] = t.LLM.BaseURL
	env["OPENAI_MODEL"] = t.LLM.Model
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
	if provider == "claude" {
		env["ANTHROPIC_API_KEY"] = t.LLM.ApiKey
		env["ANTHROPIC_BASE_URL"] = t.LLM.BaseURL
		env["ANTHROPIC_MODEL"] = t.LLM.Model
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
	return &v2.ProjectSpec{Name: "jingjiaagent-" + e.ID, Agents: []*v2.AgentSpec{{Name: "worker", Provider: provider, Model: t.LLM.Model, SystemPrompt: t.SystemPrompt, Image: image, Driver: &v2.DriverSpec{Name: "docker", Config: &v2.DriverSpec_Docker{Docker: &v2.DockerDriverSpec{}}}, Workspace: workspace, Env: vars, McpServers: mcps, Sandbox: &v2.SandboxSpec{StoppedRuntimePolicy: "retain"}}}}, nil
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
