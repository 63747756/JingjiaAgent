package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

// Initial compatibility check for the actual pinned SDK/CLIs and model. This
// does not substitute for each provider's native approval/tool/resource checks.
func TestLiveRuntimeProviders(t *testing.T) {
	if os.Getenv("JINGJIAAGENT_RUNTIME_PROVIDERS_LIVE_TEST") != "1" {
		t.Skip("requires real SDK/CLIs, daemon, PostgreSQL and model")
	}
	var model struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
	}
	data, err := os.ReadFile(os.Getenv("JINGJIAAGENT_RUNTIME_MODEL_CONFIG"))
	if err != nil || json.Unmarshal(data, &model) != nil || model.APIKey == "" {
		t.Fatal("private model unavailable")
	}
	for _, provider := range []struct {
		name               string
		coding             taskflow.CodingAgent
		protocol, endpoint string
	}{{"codex", taskflow.CodingAgentCodex, "openai_responses", strings.TrimSuffix(model.BaseURL, "/v1")}, {"claude", taskflow.CodingAgentClaude, "anthropic", strings.TrimSuffix(model.BaseURL, "/v1") + "/anthropic"}} {
		t.Run(provider.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(strings.ReplaceAll(err.Error(), model.APIKey, "[redacted]"))
				}
			}
			l := testLedger(t)
			node := config.RuntimeNode{ID: "provider-live", URL: os.Getenv("JINGJIAAGENT_RUNTIME_TEST_URL"), TokenFile: os.Getenv("JINGJIAAGENT_RUNTIME_TEST_TOKEN_FILE"), GuestImage: os.Getenv("JINGJIAAGENT_RUNTIME_TEST_GUEST_IMAGE")}
			n, err := NewEngine(node)
			check(err)
			c := &Client{ledger: l, backend: "agent_compose", nodes: map[string]config.RuntimeNode{node.ID: node}, engines: map[string]*Engine{node.ID: n}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), poll: 100 * time.Millisecond, callbackToken: "providers-test-only"}
			taskID, owner := uuid.New(), uuid.NewString()
			vm, err := c.VirtualMachiner().Create(ctx, &taskflow.CreateVirtualMachineReq{UserID: owner, HostID: node.ID, TaskID: taskID, Cores: "1", Memory: 2 << 30})
			check(err)
			_, err = l.db.ExecContext(ctx, `INSERT INTO tasks(id,status) VALUES($1,'pending')`, taskID)
			check(err)
			callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer providers-test-only" {
					w.WriteHeader(401)
					return
				}
				if r.URL.Path == "/internal/vm-ready" {
					req, e := c.PreparedTask(r.Context(), taskID.String())
					if e != nil || req == nil {
						w.WriteHeader(500)
						return
					}
					if e = c.TaskManager().Create(r.Context(), *req); e != nil {
						w.WriteHeader(500)
						return
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"code":0}`))
			}))
			defer callback.Close()
			c.callbackURL, c.http = callback.URL, callback.Client()
			t.Cleanup(func() {
				cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
				defer done()
				e, err := l.Environment(cleanup, vm.ID)
				if err == nil && e.SandboxID != "" {
					_, _ = n.sandboxes.RemoveSandbox(cleanup, connect.NewRequest(&v2.RemoveSandboxRequest{SandboxId: e.SandboxID, Force: true}))
				}
			})
			marker := "PROVIDER_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			request := taskflow.CreateTaskReq{ID: taskID, VMID: vm.ID, CodingAgent: provider.coding, LLM: taskflow.LLM{ApiKey: model.APIKey, BaseURL: provider.endpoint, Model: model.Model, ApiType: provider.protocol}, Text: "Remember this token for the next turn: " + marker + ". Reply with only that token. Do not use tools."}
			var skillReceipt, ruleReceipt, mcpReceipt string
			var mcpCalls *atomic.Int32
			if os.Getenv("JINGJIAAGENT_RUNTIME_PROVIDER_ASSETS_LIVE_TEST") == "1" {
				resources, configs, skill, rule, _, _ := liveResources(t)
				resources.Plugins = nil // Plugin delivery is an existing OpenCode-only feature.
				request.AgentResources, request.Configs = resources, configs[:1]
				skillReceipt, ruleReceipt = skill, rule
				mcp, receipt, calls := liveMCP(t)
				request.McpConfigs, mcpReceipt, mcpCalls = []taskflow.McpServerConfig{mcp}, receipt, calls
			}
			_, err = c.StageTask(ctx, request)
			check(err)
			waitState := func(turn int, terminal, expected string) {
				t.Helper()
				for {
					if err = c.Step(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
						check(err)
					}
					var state string
					e := l.db.QueryRowContext(ctx, `SELECT state FROM runtime_commands WHERE task_id=$1 AND operation='task' AND turn=$2`, taskID, turn).Scan(&state)
					if e != nil && !errors.Is(e, sql.ErrNoRows) {
						check(e)
					}
					if state == terminal {
						break
					}
					if state == "failed" || state == "canceled" {
						var run string
						if l.db.QueryRowContext(ctx, `SELECT run_id FROM runtime_commands WHERE task_id=$1 AND operation='task' AND turn=$2`, taskID, turn).Scan(&run) == nil {
							detail, e := n.runs.GetRun(ctx, connect.NewRequest(&v2.GetRunRequest{RunId: run}))
							if e == nil {
								t.Log(strings.ReplaceAll(detail.Msg.GetRun().GetSummary().GetError(), model.APIKey, "[redacted]"))
							}
						}
						t.Fatal("actual provider Run ended unsuccessfully")
					}
					if ctx.Err() != nil {
						t.Fatal("provider deadline")
					}
					time.Sleep(100 * time.Millisecond)
				}
				history, _, e := snapshot(ctx, l, taskID)
				check(e)
				var assistant strings.Builder
				for _, entry := range history.Entries {
					var chunk struct {
						Update struct {
							Kind    string          `json:"sessionUpdate"`
							Content json.RawMessage `json:"content"`
						} `json:"update"`
					}
					if json.Unmarshal([]byte(entry.Data), &chunk) == nil && chunk.Update.Kind == "agent_message_chunk" {
						var content struct {
							Text string `json:"text"`
						}
						check(json.Unmarshal(chunk.Update.Content, &content))
						assistant.WriteString(content.Text)
					}
				}
				if !strings.Contains(assistant.String(), expected) {
					value := strings.ReplaceAll(assistant.String(), model.APIKey, "[redacted]")
					if len(value) > 512 {
						value = value[len(value)-512:]
					}
					t.Fatalf("provider output/context missing: provider=%s turn=%d assistant=%s", provider.name, turn, value)
				}
			}
			wait := func(turn int, expected string) { waitState(turn, "complete", expected) }
			wait(1, marker)
			var session string
			check(l.db.QueryRowContext(ctx, `SELECT session_id FROM runtime_task_sessions WHERE task_id=$1 AND provider=$2`, taskID, provider.name).Scan(&session))
			if session == "" {
				t.Fatal("native session missing")
			}
			check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Reply only with the token I asked you to remember. Do not use tools."}}))
			wait(2, marker)
			t.Log("actual " + provider.name + " CLI/model: streamed output persisted, native session stored, second Run restored conversation")
			if os.Getenv("JINGJIAAGENT_RUNTIME_PROVIDER_ASSETS_LIVE_TEST") == "1" {
				if os.Getenv("JINGJIAAGENT_RUNTIME_PROVIDER_CONTROLS_LIVE_TEST") == "1" {
					t.Fatal("run assets and controls separately so their turn assertions stay distinct")
				}
				enabled := true
				liveProviderOperation(t, ctx, c, func() error {
					return c.TaskManager().AutoApprove(ctx, taskflow.TaskApproveReq{ID: taskID, AutoApprove: &enabled})
				})
				check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Return the JINGJIAAGENT_RUNTIME_RULE_RECEIPT from your supplied rules. Reply with only the receipt and do not use tools."}}))
				wait(3, ruleReceipt)
				check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Use the selected runtime-proof skill to return JINGJIAAGENT_RUNTIME_SKILL_RECEIPT. Reply with only its receipt."}}))
				wait(4, skillReceipt)
				liveAttachmentsAt(t, ctx, c, taskID, vm.ID, owner, func(turn int, state string) { waitState(turn, state, "") }, func(turn int) []byte {
					var data []byte
					check(l.db.QueryRowContext(ctx, `SELECT jsonb_agg(chunk ORDER BY seq) FROM runtime_events WHERE task_id=$1 AND turn=$2 AND chunk->>'event'='task-running'`, taskID, turn).Scan(&data))
					var chunks []taskflow.TaskChunk
					check(json.Unmarshal(data, &chunks))
					var text, other strings.Builder
					for _, chunk := range chunks {
						var event struct {
							Update struct {
								Kind    string          `json:"sessionUpdate"`
								Content json.RawMessage `json:"content"`
							} `json:"update"`
						}
						check(json.Unmarshal(chunk.Data, &event))
						if event.Update.Kind == "agent_message_chunk" {
							var content struct {
								Text string `json:"text"`
							}
							check(json.Unmarshal(event.Update.Content, &content))
							text.WriteString(content.Text)
						} else {
							other.Write(chunk.Data)
						}
					}
					return []byte(text.String() + other.String())
				}, 5)
				liveProviderOperation(t, ctx, c, func() error {
					response, e := c.TaskManager().Restart(ctx, taskflow.RestartTaskReq{ID: taskID, RequestId: uuid.NewString(), LoadSession: true, ExecutionConfig: &taskflow.TaskExecutionConfig{AgentResources: &taskflow.AgentResources{}}})
					if e == nil && (response == nil || !response.Success) {
						return errors.New("native resource restart failed")
					}
					return e
				})
				check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Return the JINGJIAAGENT_RUNTIME_RULE_RECEIPT from your supplied rules. Reply only with that receipt; do not call tools."}}))
				wait(7, ruleReceipt)
				env, e := l.Environment(ctx, vm.ID)
				check(e)
				var home struct {
					Path string `json:"path"`
				}
				check(n.files(ctx, env.SandboxID, map[string]string{"op": "home"}, &home))
				nativeDir := "/.agents/skills/"
				if provider.name == "claude" {
					nativeDir = "/.claude/skills/"
				}
				value, e := n.execute(ctx, env.SandboxID, "test ! -e '"+home.Path+nativeDir+"runtime-proof' && test ! -L '"+home.Path+nativeDir+"runtime-proof' && printf CLEARED", 128)
				check(e)
				if value != "CLEARED" {
					t.Fatal("native CLI retained a cleared platform skill")
				}
				check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Call the acceptance MCP runtime_probe exactly once with value 21. Reply with its computed value and receipt. Do not use other tools or read configuration files."}}))
				wait(8, mcpReceipt)
				if mcpCalls.Load() != 1 {
					t.Fatal("native MCP did not execute exactly once")
				}
				liveProviderOperation(t, ctx, c, func() error {
					_, e := c.TaskManager().Restart(ctx, taskflow.RestartTaskReq{ID: taskID, RequestId: uuid.NewString(), LoadSession: true, ExecutionConfig: &taskflow.TaskExecutionConfig{McpServers: []taskflow.McpServerConfig{}}})
					return e
				})
				check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "The MCP selection was cleared. If runtime_probe is unavailable reply MCP_CLEARED. Do not use any other tool or read files; never invent a tool result."}}))
				wait(9, "MCP_CLEARED")
				if mcpCalls.Load() != 1 {
					t.Fatal("cleared native MCP remained callable")
				}
				t.Log("real " + provider.name + ": native rule and skill consumed unpredictable receipts; attachments matched; clearing skills retained rules; MCP used its authorized header and cleared without another call")
			}
			if os.Getenv("JINGJIAAGENT_RUNTIME_PROVIDER_CONTROLS_LIVE_TEST") == "1" {
				pending := func(turn int, kind string) nativeInteraction {
					t.Helper()
					for {
						if err = c.Step(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
							check(err)
						}
						var data []byte
						err = l.db.QueryRowContext(ctx, `SELECT chunk FROM runtime_events WHERE task_id=$1 AND turn=$2 AND source_key LIKE 'interaction/%' ORDER BY seq DESC LIMIT 1`, taskID, turn).Scan(&data)
						if err == nil {
							var chunk taskflow.TaskChunk
							check(json.Unmarshal(data, &chunk))
							var body struct {
								ToolCall struct {
									Input nativeInteraction `json:"rawInput"`
								} `json:"toolCall"`
							}
							check(json.Unmarshal(chunk.Data, &body))
							if body.ToolCall.Input.Kind != kind {
								t.Fatal("unexpected native request kind")
							}
							return body.ToolCall.Input
						}
						if !errors.Is(err, sql.ErrNoRows) {
							check(err)
						}
						var state string
						if e := l.db.QueryRowContext(ctx, `SELECT state FROM runtime_commands WHERE task_id=$1 AND operation='task' AND turn=$2`, taskID, turn).Scan(&state); e == nil && (state == "complete" || state == "failed" || state == "canceled") {
							t.Fatalf("native request missing: provider=%s turn=%d terminal=%s", provider.name, turn, state)
						}
						if ctx.Err() != nil {
							t.Fatal("native provider request deadline")
						}
						time.Sleep(100 * time.Millisecond)
					}
				}
				reply := func(native nativeInteraction, choice string) {
					t.Helper()
					done := make(chan error, 1)
					go func() {
						done <- c.TaskManager().AskUserQuestion(ctx, taskflow.AskUserQuestionResponse{TaskId: taskID.String(), RequestId: native.ID, AnswersJson: string(mustJSON(map[string]string{native.Questions[0].Question: choice}))})
					}()
					for {
						select {
						case e := <-done:
							check(e)
							return
						default:
						}
						if err = c.Step(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
							check(err)
						}
						if ctx.Err() != nil {
							t.Fatal("native reply deadline")
						}
						time.Sleep(100 * time.Millisecond)
					}
				}
				choice := "CHOICE_" + uuid.NewString()[:8]
				check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Use AskUserQuestion now to ask exactly one question 'SDK原生问题' with options '暂不执行' and '" + choice + "'. Do not use any other tools. Wait for the user answer, then reply only with the selected label."}}))
				question := pending(3, "question")
				reply(question, choice)
				wait(3, choice)
				check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Use Bash exactly once: printf '" + choice + "' > /workspace/claude-approved.txt . Wait for approval. Do not use other tools or retry if denied. Then reply TOOL_DONE."}}))
				permission := pending(4, "permission")
				env, err := l.Environment(ctx, vm.ID)
				check(err)
				value, err := n.execute(ctx, env.SandboxID, "test ! -f /workspace/claude-approved.txt && printf ABSENT", 128)
				check(err)
				if value != "ABSENT" {
					t.Fatal("native command ran before approval")
				}
				reply(permission, "允许一次")
				wait(4, "TOOL_DONE")
				value, err = n.execute(ctx, env.SandboxID, "cat /workspace/claude-approved.txt", 256)
				check(err)
				if value != choice {
					t.Fatal("approved native tool output missing")
				}
				check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Use Bash exactly once: printf 'NEVER' > /workspace/claude-denied.txt . Wait for approval. If denied reply DENIED_OK. Do not use other tools, retry, or bypass rejection."}}))
				permission = pending(5, "permission")
				reply(permission, "拒绝")
				wait(5, "DENIED_OK")
				value, err = n.execute(ctx, env.SandboxID, "test ! -f /workspace/claude-denied.txt && printf ABSENT", 128)
				check(err)
				if value != "ABSENT" {
					t.Fatal("denied native tool executed")
				}
				t.Log("real " + provider.name + ": question resumed with selected answer; native command held before approval, allowed once, and rejected without execution")
				restart := func(keep bool) *taskflow.RestartTaskResp {
					t.Helper()
					type result struct {
						response *taskflow.RestartTaskResp
						err      error
					}
					done := make(chan result, 1)
					go func() {
						r, e := c.TaskManager().Restart(ctx, taskflow.RestartTaskReq{ID: taskID, RequestId: uuid.NewString(), LoadSession: keep})
						done <- result{r, e}
					}()
					for {
						select {
						case value := <-done:
							check(value.err)
							if value.response == nil || !value.response.Success {
								t.Fatal("native restart failed")
							}
							return value.response
						default:
						}
						if err = c.Step(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
							check(err)
						}
						if ctx.Err() != nil {
							t.Fatal("native restart deadline")
						}
						time.Sleep(100 * time.Millisecond)
					}
				}
				kept := restart(true)
				if kept.SessionID != session {
					t.Fatal("native restart changed saved session")
				}
				check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Reply only with the PROVIDER token I first asked you to remember. Do not use tools."}}))
				wait(6, marker)
				reset := restart(false)
				if reset.SessionID != "" {
					t.Fatal("native session reset retained a pointer")
				}
				check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "If this conversation has no PROVIDER token, reply UNKNOWN. Do not read files or use tools."}}))
				wait(7, "UNKNOWN")
				value, err = n.execute(ctx, env.SandboxID, "cat /workspace/claude-approved.txt", 256)
				check(err)
				if value != choice {
					t.Fatal("native restart or reset removed workspace")
				}
				t.Log("real " + provider.name + ": keep/reset restart preserved workspace; kept conversation restored, cleared conversation reported UNKNOWN")
				policy := func(enabled bool) {
					t.Helper()
					done := make(chan error, 1)
					go func() {
						done <- c.TaskManager().AutoApprove(ctx, taskflow.TaskApproveReq{ID: taskID, AutoApprove: &enabled})
					}()
					for {
						select {
						case e := <-done:
							check(e)
							return
						default:
						}
						if e := c.Step(ctx); e != nil && !errors.Is(e, sql.ErrNoRows) {
							check(e)
						}
						check(ctx.Err())
						time.Sleep(100 * time.Millisecond)
					}
				}
				check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Use Bash exactly once: printf 'AUTO_ENABLED' > /workspace/sdk-auto.txt . Wait for approval. Then reply AUTO_DONE."}}))
				pending(8, "permission")
				value, err = n.execute(ctx, env.SandboxID, "test ! -f /workspace/sdk-auto.txt && printf ABSENT", 128)
				check(err)
				if value != "ABSENT" {
					t.Fatal("native command ran before auto approval was enabled")
				}
				policy(true)
				wait(8, "AUTO_DONE")
				value, err = n.execute(ctx, env.SandboxID, "cat /workspace/sdk-auto.txt", 128)
				check(err)
				if value != "AUTO_ENABLED" {
					t.Fatal("auto approval did not release pending native command")
				}
				restart(false)
				check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Use Bash exactly once: printf 'AUTO_RETAINED' > /workspace/sdk-auto-reset.txt . Then reply AUTO_RETAINED_DONE."}}))
				wait(9, "AUTO_RETAINED_DONE")
				value, err = n.execute(ctx, env.SandboxID, "cat /workspace/sdk-auto-reset.txt", 128)
				check(err)
				if value != "AUTO_RETAINED" {
					t.Fatal("native reset lost task auto approval policy")
				}
				policy(false)
				check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: `Call the actual shell execution tool now, exactly once, with this command: python3 -c 'from pathlib import Path; Path("/workspace/sdk-disabled.txt").write_text("NEVER")' . The host handles approval; do not ask in text or wait before calling the tool. If denied reply DISABLED_OK. Do not retry or use other tools.`}}))
				permission = pending(10, "permission")
				reply(permission, "拒绝")
				wait(10, "DISABLED_OK")
				value, err = n.execute(ctx, env.SandboxID, "test ! -f /workspace/sdk-disabled.txt && printf ABSENT", 128)
				check(err)
				if value != "ABSENT" {
					t.Fatal("disabled auto approval executed a rejected command")
				}
				policy(true)
				check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Use AskUserQuestion to ask '取消SDK等待' with options '继续' and '退出'. Wait for the answer. Do not use other tools or answer yourself."}}))
				question = pending(11, "question")
				check(c.TaskManager().Cancel(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID}}))
				waitState(11, "canceled", "")
				if c.TaskManager().AskUserQuestion(ctx, taskflow.AskUserQuestionResponse{TaskId: taskID.String(), RequestId: question.ID, AnswersJson: `{"取消SDK等待":"继续"}`}) == nil {
					t.Fatal("canceled native question accepted a stale answer")
				}
				t.Log("real " + provider.name + ": explicit auto approval released pending command, survived reset, returned to manual mode; questions still waited and cancel rejected stale answers")
			}
		})
	}
}

func liveProviderOperation(t *testing.T, ctx context.Context, c *Client, operation func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- operation() }()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal("native control operation failed")
			}
			return
		default:
		}
		if err := c.Step(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("native control worker failed")
		}
		if ctx.Err() != nil {
			t.Fatal("native control deadline")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
