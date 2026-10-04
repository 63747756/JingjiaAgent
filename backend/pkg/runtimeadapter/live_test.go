package runtimeadapter

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

// This is opt-in, spends real model tokens, and requires an isolated daemon and
// PostgreSQL database. The callback is a test fixture, so this is runtime
// integration evidence, not acceptance of the Web/auth/business lifecycle.
func TestLiveRuntime(t *testing.T) {
	if os.Getenv("RUNTIME_LIVE_TEST") != "1" {
		t.Skip("set RUNTIME_LIVE_TEST=1 with isolated runtime and model configuration")
	}
	var model struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
	}
	data, err := os.ReadFile(os.Getenv("RUNTIME_MODEL_CONFIG"))
	if err != nil {
		t.Fatal("cannot read live model config")
	}
	if json.Unmarshal(data, &model) != nil || model.APIKey == "" || model.Model == "" {
		t.Fatal("invalid live model config")
	}
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(strings.ReplaceAll(err.Error(), model.APIKey, "[redacted]"))
		}
	}
	l := testLedger(t)
	node := config.RuntimeNode{ID: "live", URL: os.Getenv("RUNTIME_TEST_URL"), TokenFile: os.Getenv("RUNTIME_TEST_TOKEN_FILE"), GuestImage: os.Getenv("RUNTIME_TEST_GUEST_IMAGE")}
	n, err := NewEngine(node)
	check(err)
	c := &Client{ledger: l, backend: "agent_compose", nodes: map[string]config.RuntimeNode{node.ID: node}, engines: map[string]*Engine{node.ID: n}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), poll: time.Millisecond * 100, callbackToken: "isolated-callback"}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	taskID, ownerID := uuid.New(), uuid.NewString()
	marker := "RUNTIME_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	vm, err := c.VirtualMachiner().Create(ctx, &taskflow.CreateVirtualMachineReq{UserID: ownerID, HostID: node.ID, TaskID: taskID, Cores: "1", Memory: 2 << 30})
	check(err)
	_, err = l.db.ExecContext(ctx, `INSERT INTO tasks(id,status) VALUES($1,'pending')`, taskID)
	check(err)
	mcp, mcpReceipt, mcpCalls := liveMCP(t)
	resources, configs, skillReceipt, ruleReceipt, pluginReceipt, assetDownloads := liveResources(t)
	req := taskflow.CreateTaskReq{ID: taskID, VMID: vm.ID, CodingAgent: taskflow.CodingAgentOpenCode,
		LLM:            taskflow.LLM{ApiKey: model.APIKey, BaseURL: model.BaseURL, Model: model.Model},
		McpConfigs:     []taskflow.McpServerConfig{mcp},
		AgentResources: resources,
		Configs:        configs,
		Text:           "Remember this exact token for the next turn: " + marker + ". Reply with only this token. Do not use any tools."}
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer isolated-callback" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/internal/vm-ready" {
			intent, e := c.PreparedTask(r.Context(), taskID.String())
			if e != nil || intent == nil {
				w.WriteHeader(500)
				return
			}
			if e = c.TaskManager().Create(r.Context(), *intent); e != nil {
				w.WriteHeader(500)
				return
			}
		} else if r.URL.Path != "/internal/vm-info" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer callback.Close()
	c.callbackURL, c.http = callback.URL, callback.Client()
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		e, err := l.Environment(cleanup, vm.ID)
		if t.Failed() && os.Getenv("RUNTIME_LIVE_KEEP_FAILURE") == "1" {
			t.Log("preserved isolated failure sandbox", e.SandboxID)
			return
		}
		if err == nil && e.SandboxID != "" && e.State != "deleted" {
			_, err = n.sandboxes.RemoveSandbox(cleanup, connect.NewRequest(&v2.RemoveSandboxRequest{SandboxId: e.SandboxID, Force: true}))
			if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
				t.Log("sandbox cleanup failed; inspect isolated PoC daemon")
			}
		}
	})
	staged, err := c.StageTask(ctx, req)
	check(err)
	if !staged {
		t.Fatal("new environment routed to legacy")
	}
	waitTurn := func(turn int, want string) {
		t.Helper()
		for ctx.Err() == nil {
			err := c.Step(ctx)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				check(err)
			}
			var state string
			err = l.db.QueryRowContext(ctx, `SELECT state FROM runtime_commands WHERE task_id=$1 AND operation='task' AND turn=$2`, taskID, turn).Scan(&state)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				check(err)
			}
			if state == "complete" || state == "failed" || state == "canceled" {
				if state != want {
					t.Fatalf("turn %d state=%s, want %s", turn, state, want)
				}
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(200 * time.Millisecond):
			}
		}
		check(ctx.Err())
	}
	waitTurn(1, "complete")
	first, watermark, err := snapshot(ctx, l, taskID)
	check(err)
	encoded, err := json.Marshal(first)
	check(err)
	if !bytes.Contains(encoded, []byte(marker)) {
		t.Fatal("real Agent reply missing from durable history")
	}
	t.Log("real OpenCode + model round one completed and persisted")
	environment, err := l.Environment(ctx, vm.ID)
	check(err)
	if environment.SandboxID == "" {
		t.Fatal("sandbox mapping missing")
	}
	var pluginContents []byte
	check(c.FileManager().Download(ctx, taskflow.FileReq{ID: vm.ID, UserID: ownerID, Path: "/workspace/plugin-loaded.txt"}, func(_ uint64, data []byte) error {
		pluginContents = append(pluginContents, data...)
		return nil
	}))
	if string(pluginContents) != pluginReceipt || assetDownloads.Load() != 2 {
		t.Fatal("selected plugin was not downloaded and loaded by the actual OpenCode process")
	}
	cpu, err := n.execute(ctx, environment.SandboxID, "cat /sys/fs/cgroup/cpu.max", 1024)
	check(err)
	memory, err := n.execute(ctx, environment.SandboxID, "cat /sys/fs/cgroup/memory.max", 1024)
	check(err)
	if strings.TrimSpace(cpu) != "100000 100000" || strings.TrimSpace(memory) != "2147483648" {
		t.Fatal("Docker CPU/memory limits not enforced")
	}
	var activityCount int
	check(l.db.QueryRowContext(ctx, `SELECT count(*) FROM runtime_events WHERE task_id=$1 AND source_key LIKE '%/%'`, taskID).Scan(&activityCount))
	if activityCount == 0 {
		t.Fatal("runtime persisted only the final answer, no live activities")
	}
	t.Log("structured activities persisted; Docker CPU/memory limits verified inside Guest")
	// A second owner must fail before any runtime RPC.
	_, err = c.FileManager().Operate(ctx, taskflow.FileReq{ID: vm.ID, UserID: uuid.NewString(), Operate: taskflow.FileOpList, Path: "/workspace"})
	if err == nil {
		t.Fatal("another owner accessed sandbox files")
	}
	payload := bytes.Repeat([]byte{0, 1, 127, 128, 255}, 22000)
	files := map[string][]byte{"中文文件.txt": []byte("中文内容\n" + marker), "binary.bin": payload, "empty.txt": {}}
	for name, content := range files {
		ch := make(chan []byte, 1)
		ch <- content
		close(ch)
		check(c.FileManager().Upload(ctx, taskflow.FileReq{ID: vm.ID, UserID: ownerID, Path: "/workspace/" + name}, ch))
	}
	readFiles := func() {
		t.Helper()
		for name, want := range files {
			var got bytes.Buffer
			var size uint64
			check(c.FileManager().Download(ctx, taskflow.FileReq{ID: vm.ID, UserID: ownerID, Path: "/workspace/" + name}, func(total uint64, b []byte) error {
				if b == nil {
					size = total
				}
				_, e := got.Write(b)
				return e
			}))
			if !bytes.Equal(want, got.Bytes()) || size != uint64(len(want)) {
				t.Fatalf("file contents/size changed: %s", name)
			}
		}
	}
	readFiles()
	listed, err := c.TaskManager().ListFiles(ctx, taskflow.RepoListFilesReq{TaskId: taskID.String(), RequestId: "live-list", Path: "."})
	check(err)
	if !listed.Success || len(listed.Files) < len(files) {
		t.Fatal("repository file listing lost actual files")
	}
	for name, want := range files {
		got, e := c.TaskManager().ReadFile(ctx, taskflow.RepoReadFileReq{TaskId: taskID.String(), RequestId: "live-read", Path: name})
		check(e)
		if !got.Success || got.IsTruncated || !bytes.Equal(got.Content, want) || got.TotalSize != int64(len(want)) {
			t.Fatalf("repository read changed contents: %s", name)
		}
	}
	if _, err = c.TaskManager().ReadFile(ctx, taskflow.RepoReadFileReq{TaskId: taskID.String(), Path: "../home/.config/opencode/auth.json"}); err == nil {
		t.Fatal("repository read escaped workspace")
	}
	testLiveTerminal(t, ctx, c, vm.ID)
	// Exercise the existing task repository contract through actual Guest Exec.
	baselinePath := taskflow.FileReq{ID: vm.ID, UserID: ownerID, Path: "/workspace/git-baseline.txt", Operate: taskflow.FileOpSave, Content: "before\n"}
	_, err = c.FileManager().Operate(ctx, baselinePath)
	check(err)
	_, err = n.execute(ctx, environment.SandboxID, "git init -q", 1024)
	check(err)
	_, err = n.execute(ctx, environment.SandboxID, "git add -- git-baseline.txt", 1024)
	check(err)
	_, err = n.execute(ctx, environment.SandboxID, "git -c user.name=RuntimePoC -c user.email=runtime@example.invalid commit -qm baseline", 1024)
	check(err)
	baselinePath.Content = "after\n"
	_, err = c.FileManager().Operate(ctx, baselinePath)
	check(err)
	changes, err := c.TaskManager().FileChanges(ctx, taskflow.RepoFileChangesReq{TaskId: taskID.String(), RequestId: "live-changes"})
	check(err)
	if !changes.Success || changes.RequestId != "live-changes" || changes.CommitHash == nil {
		t.Fatal("repository change response lost metadata")
	}
	foundChange := false
	for _, change := range changes.Changes {
		if change.Path == "git-baseline.txt" && change.Status == "M" && change.Additions != nil && *change.Additions == 1 && change.Deletions != nil && *change.Deletions == 1 {
			foundChange = true
		}
	}
	if !foundChange {
		t.Fatal("actual Git changes missing from repository contract")
	}
	diff, err := c.TaskManager().FileDiff(ctx, taskflow.RepoFileDiffReq{TaskId: taskID.String(), RequestId: "live-diff", Path: "git-baseline.txt"})
	check(err)
	if !diff.Success || !strings.Contains(diff.Diff, "-before") || !strings.Contains(diff.Diff, "+after") {
		t.Fatal("actual Git diff missing")
	}
	t.Log("actual Git change statistics and unified diff passed task repository contract")
	check(c.VirtualMachiner().Hibernate(ctx, &taskflow.HibernateVirtualMachineReq{ID: vm.ID, HostID: node.ID, UserID: ownerID}))
	check(c.VirtualMachiner().Resume(ctx, &taskflow.ResumeVirtualMachineReq{ID: vm.ID, HostID: node.ID, UserID: ownerID}))
	readFiles()
	t.Log("Unicode, binary and empty files survived sandbox stop/resume; cross-owner access rejected")
	// Optional control-plane restart is explicitly limited to the named test daemon.
	if container := os.Getenv("RUNTIME_TEST_DAEMON_CONTAINER"); container != "" {
		if container != "jingjia-runtime-poc-daemon-1" {
			t.Fatal("unexpected test daemon container")
		}
		if err = exec.CommandContext(ctx, "docker", "restart", container).Run(); err != nil {
			t.Fatal("test daemon restart failed")
		}
		for i := 0; i < 60; i++ {
			_, err = n.runs.GetRun(ctx, connect.NewRequest(&v2.GetRunRequest{RunId: func() string {
				var id string
				_ = l.db.QueryRowContext(ctx, `SELECT run_id FROM runtime_commands WHERE task_id=$1 AND operation='task' AND turn=1`, taskID).Scan(&id)
				return id
			}()}))
			if err == nil {
				break
			}
			time.Sleep(time.Second)
		}
		check(err)
		readFiles()
		t.Log("files remained accessible after daemon restart")
	}
	// Construct a new worker object to exercise recovery from committed state.
	c = &Client{ledger: c.ledger, legacy: c.legacy, backend: c.backend, nodes: c.nodes, engines: c.engines,
		logger: c.logger, poll: c.poll, callbackURL: c.callbackURL, callbackToken: c.callbackToken, http: c.http,
		objectStorage: c.objectStorage, builtinMCPURL: c.builtinMCPURL, agentMCPURL: c.agentMCPURL,
		preview: c.preview, registry: c.registry}
	if c.registry != nil {
		check(c.SyncNodes(ctx))
	}
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "What exact token did I ask you to remember in the previous turn? Reply with only that token. Do not read files or use tools."}}))
	waitTurn(2, "complete")
	events, err := l.Events(ctx, taskID.String(), int64(watermark))
	check(err)
	found := false
	for _, event := range events {
		if event.Event == "task-running" && bytes.Contains(event.Data, []byte(marker)) {
			found = true
		}
	}
	if !found {
		t.Fatal("provider session did not remember previous turn after recovery")
	}
	t.Log("second Run reused provider conversation after sandbox and daemon recovery")
	var originalSession string
	check(l.db.QueryRowContext(ctx, `SELECT session_id FROM runtime_task_sessions WHERE task_id=$1`, taskID).Scan(&originalSession))
	if originalSession == "" {
		t.Fatal("real provider session ID was not mapped")
	}
	restart := func(keep bool, config *taskflow.TaskExecutionConfig) *taskflow.RestartTaskResp {
		t.Helper()
		type result struct {
			response *taskflow.RestartTaskResp
			err      error
		}
		completed := make(chan result, 1)
		request := taskflow.RestartTaskReq{ID: taskID, RequestId: uuid.NewString(), LoadSession: keep, ExecutionConfig: config}
		go func() { response, err := c.TaskManager().Restart(ctx, request); completed <- result{response, err} }()
		for ctx.Err() == nil {
			select {
			case outcome := <-completed:
				check(outcome.err)
				if !outcome.response.Success {
					t.Fatal("restart was not accepted")
				}
				repeated, err := c.TaskManager().Restart(ctx, request)
				check(err)
				if repeated.SessionID != outcome.response.SessionID {
					t.Fatal("restart retry changed session")
				}
				return outcome.response
			default:
			}
			if err := c.Step(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
				check(err)
			}
			time.Sleep(100 * time.Millisecond)
		}
		check(ctx.Err())
		return nil
	}
	nextModel := req.LLM
	nextModel.ApiType = "openai_chat"
	retained := restart(true, &taskflow.TaskExecutionConfig{LLM: &nextModel})
	if retained.SessionID != originalSession {
		t.Fatal("restart lost the current provider session")
	}
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "What exact token did I ask you to remember? Reply with only that token. Do not use tools."}}))
	waitTurn(3, "complete")
	turnOutput := func(turn int) []byte {
		t.Helper()
		var data []byte
		check(l.db.QueryRowContext(ctx, `SELECT jsonb_agg(e.chunk) FROM runtime_events e JOIN runtime_commands c ON c.id=e.command_id WHERE c.task_id=$1 AND c.operation='task' AND c.turn=$2 AND e.chunk->>'event'='task-running'`, taskID, turn).Scan(&data))
		var chunks []taskflow.TaskChunk
		check(json.Unmarshal(data, &chunks))
		var output []byte
		for _, chunk := range chunks {
			output = append(output, chunk.Data...)
		}
		return output
	}
	retainedReply := turnOutput(3)
	if !bytes.Contains(retainedReply, []byte(marker)) {
		t.Fatal("restart did not retain provider context")
	}
	cleared := restart(false, nil)
	if cleared.SessionID != "" {
		t.Fatal("context reset retained old session pointer")
	}
	readFiles()
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "What exact token did I ask you to remember in this conversation? If no token was supplied in this conversation, reply UNKNOWN. Do not use tools or read files. Reply with only the token or UNKNOWN."}}))
	waitTurn(4, "complete")
	var newSession string
	check(l.db.QueryRowContext(ctx, `SELECT session_id FROM runtime_task_sessions WHERE task_id=$1`, taskID).Scan(&newSession))
	if newSession == "" || newSession == originalSession {
		t.Fatal("context reset did not create a new native session")
	}
	last := turnOutput(4)
	if !bytes.Contains(last, []byte("UNKNOWN")) || bytes.Contains(last, []byte(marker)) {
		t.Fatal("new provider session leaked the cleared context")
	}
	t.Log("real restart retained context; clear created a new native session, with files intact")
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Call the acceptance runtime_probe MCP tool with value 21. Reply with only the complete returned result including its acceptance receipt. Do not use bash, files or other tools."}}))
	waitTurn(5, "complete")
	if mcpCalls.Load() < 1 || !bytes.Contains(turnOutput(5), []byte(mcpReceipt)) {
		t.Fatal("real Agent did not execute the authenticated MCP tool and return its receipt")
	}
	t.Log("real HTTP MCP execution returned an unpredictable tool receipt; authorization header delivered and missing-header request denied")
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Load the runtime-proof skill to get RUNTIME_SKILL_RECEIPT, then follow its instructions. Do not use MCP, bash or other tools."}}))
	waitTurn(6, "complete")
	if !bytes.Contains(turnOutput(6), []byte(skillReceipt)) {
		t.Fatal("actual OpenCode did not load the selected Skill")
	}
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "RUNTIME_RULE_RECEIPT"}}))
	waitTurn(7, "complete")
	if !bytes.Contains(turnOutput(7), []byte(ruleReceipt)) || assetDownloads.Load() != 2 {
		t.Fatal("rule was not loaded, or an unchanged resource selection was unnecessarily downloaded again")
	}
	restart(true, &taskflow.TaskExecutionConfig{AgentResources: &taskflow.AgentResources{}})
	_, err = c.FileManager().Operate(ctx, taskflow.FileReq{ID: vm.ID, UserID: ownerID, Path: "/workspace/plugin-loaded.txt", Operate: taskflow.FileOpDelete})
	check(err)
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Reply only RESOURCE_CLEAR_OK. Do not use tools."}}))
	waitTurn(8, "complete")
	remaining, err := c.FileManager().Operate(ctx, taskflow.FileReq{ID: vm.ID, UserID: ownerID, Path: "/workspace", Operate: taskflow.FileOpList})
	check(err)
	for _, file := range remaining {
		if file.Name == "plugin-loaded.txt" {
			t.Fatal("cleared plugin was loaded again")
		}
	}
	for _, kind := range []string{"skills", "plugins"} {
		files, err := c.FileManager().Operate(ctx, taskflow.FileReq{ID: vm.ID, UserID: ownerID, Path: "~/.codingmatrix/project-tpl/.ai-ready/" + kind, Operate: taskflow.FileOpList})
		check(err)
		if len(files) != 0 {
			t.Fatal("resource clear retained installed assets")
		}
	}
	t.Log("actual Skill, rule and plugin loading passed; retained selection reused downloads, and explicit clear removed assets and plugin registration")
	liveInteractions(t, ctx, c, taskID, vm.ID, ownerID, restart, waitTurn, turnOutput)
	liveAttachments(t, ctx, c, taskID, vm.ID, ownerID, waitTurn, turnOutput)
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Write a very long explanation of prime numbers, including many examples."}}))
	check(c.Step(ctx))
	check(c.TaskManager().Cancel(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID}}))
	waitTurn(20, "canceled")
	readFiles()
	t.Log("explicit cancellation reached remote terminal status; files remained intact")
	managed, err := c.StopTaskAndWait(ctx, taskID.String())
	check(err)
	if !managed {
		t.Fatal("durable task stop lost backend routing")
	}
	check(c.VirtualMachiner().Delete(ctx, &taskflow.DeleteVirtualMachineReq{ID: vm.ID, HostID: node.ID, UserID: ownerID}))
	_, err = c.FileManager().Operate(ctx, taskflow.FileReq{ID: vm.ID, UserID: ownerID, Operate: taskflow.FileOpList, Path: "/workspace"})
	if err == nil {
		t.Fatal("recycled environment remained accessible")
	}
	t.Log("recycled environment returned unavailable without backend fallback")
}
