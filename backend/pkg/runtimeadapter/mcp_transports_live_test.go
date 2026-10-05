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
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/google/uuid"
)

// Real model + Guest evidence for raw runtime MCP configs, NOT a Web local-
// command authoring flow. The original product currently exposes URL upstreams.
func TestLiveMCPTransports(t *testing.T) {
	if os.Getenv("JINGJIAAGENT_RUNTIME_MCP_LIVE_TEST") != "1" {
		t.Skip("requires private isolated MCP/runtime/model fixtures")
	}
	var model struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
	}
	data, err := os.ReadFile(os.Getenv("JINGJIAAGENT_RUNTIME_MODEL_CONFIG"))
	if err != nil || json.Unmarshal(data, &model) != nil || model.APIKey == "" {
		t.Fatal("invalid private model fixture")
	}
	var fixture struct {
		Token         string `json:"token"`
		StreamURL     string `json:"stream_url"`
		LegacyURL     string `json:"legacy_url"`
		StreamReceipt string `json:"stream_receipt"`
		LegacyReceipt string `json:"legacy_receipt"`
	}
	data, err = os.ReadFile(os.Getenv("JINGJIAAGENT_RUNTIME_MCP_TRANSPORT_CONFIG"))
	if err != nil || json.Unmarshal(data, &fixture) != nil || fixture.Token == "" {
		t.Fatal("invalid private transport fixture")
	}
	check := func(err error) {
		t.Helper()
		if err != nil {
			msg := strings.ReplaceAll(err.Error(), model.APIKey, "[redacted]")
			msg = strings.ReplaceAll(msg, fixture.Token, "[redacted]")
			t.Fatal(msg)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	l := testLedger(t)
	node := config.RuntimeNode{ID: "mcp-live", URL: os.Getenv("JINGJIAAGENT_RUNTIME_TEST_URL"), TokenFile: os.Getenv("JINGJIAAGENT_RUNTIME_TEST_TOKEN_FILE"), GuestImage: os.Getenv("JINGJIAAGENT_RUNTIME_TEST_GUEST_IMAGE")}
	engine, err := NewEngine(node)
	check(err)
	c := &Client{ledger: l, backend: "agent_compose", nodes: map[string]config.RuntimeNode{node.ID: node}, engines: map[string]*Engine{node.ID: engine}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), poll: 100 * time.Millisecond, callbackToken: "mcp-isolated-callback"}
	taskID, owner := uuid.New(), uuid.NewString()
	vm, err := c.VirtualMachiner().Create(ctx, &taskflow.CreateVirtualMachineReq{UserID: owner, HostID: node.ID, TaskID: taskID, Cores: "1", Memory: 2 << 30})
	check(err)
	_, err = l.db.ExecContext(ctx, "INSERT INTO tasks(id,status) VALUES($1,'pending')", taskID)
	check(err)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		e, err := l.Environment(cleanup, vm.ID)
		if err == nil && e.SandboxID != "" {
			_, err = engine.sandboxes.RemoveSandbox(cleanup, connect.NewRequest(&v2.RemoveSandboxRequest{SandboxId: e.SandboxID, Force: true}))
			if err != nil {
				t.Error("isolated sandbox cleanup failed")
			}
		}
	})
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mcp-isolated-callback" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/internal/vm-ready" {
			intent, e := c.PreparedTask(r.Context(), taskID.String())
			if e != nil || intent == nil || c.TaskManager().Create(r.Context(), *intent) != nil {
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
	localReceipt := "LOCAL_MCP_" + uuid.NewString()
	command := "node"
	// stdio JSON-RPC, no packages/network. Receipt is delivered through an
	// encrypted secret env, absent from the model prompt and process args.
	localScript := `const readline=require("node:readline"),fs=require("node:fs");readline.createInterface({input:process.stdin}).on("line",line=>{let r;try{r=JSON.parse(line)}catch{return}if(r.id===undefined)return;let result;if(r.method==="initialize")result={protocolVersion:r.params.protocolVersion,capabilities:{tools:{}},serverInfo:{name:"local-proof",version:"1"}};else if(r.method==="tools/list")result={tools:[{name:"local_transport_probe",description:"Compute three times value and return a private local receipt.",inputSchema:{type:"object",properties:{value:{type:"integer"}},required:["value"],additionalProperties:false}}]};else if(r.method==="tools/call"&&r.params.name==="local_transport_probe"){fs.appendFileSync("/workspace/local-mcp-audit.txt",String(r.params.arguments.value)+" "+process.env.MCP_PROOF_RECEIPT+"\n");result={content:[{type:"text",text:String(r.params.arguments.value*3)+" "+process.env.MCP_PROOF_RECEIPT}],isError:false}}else if(r.method==="ping")result={};else{console.log(JSON.stringify({jsonrpc:"2.0",id:r.id,error:{code:-32601,message:"unknown method"}}));return}console.log(JSON.stringify({jsonrpc:"2.0",id:r.id,result}))});`
	streamURL := strings.Replace(fixture.StreamURL, "127.0.0.1", "host.docker.internal", 1)
	legacyURL := strings.Replace(fixture.LegacyURL, "127.0.0.1", "host.docker.internal", 1)
	mcps := []taskflow.McpServerConfig{
		{Name: "stream-proof", Type: "http", Url: &streamURL, Headers: []*taskflow.McpHttpHeader{{Name: "Authorization", Value: "Bearer " + fixture.Token}}},
		{Name: "legacy-proof", Type: "sse", Url: &legacyURL, Headers: []*taskflow.McpHttpHeader{{Name: "Authorization", Value: "Bearer " + fixture.Token}}},
		{Name: "local-proof", Type: "stdio", Command: &command, Args: []string{"-e", localScript}, Env: map[string]string{"MCP_PROOF_RECEIPT": localReceipt}},
	}
	req := taskflow.CreateTaskReq{ID: taskID, VMID: vm.ID, CodingAgent: taskflow.CodingAgentOpenCode, LLM: taskflow.LLM{ApiKey: model.APIKey, BaseURL: model.BaseURL, Model: model.Model}, McpConfigs: mcps,
		Text: "Call stream_transport_probe once with value 21, legacy_transport_probe once with value 22, and local_transport_probe once with value 14. Return all three computed values and private receipts from the tools. Do not read any configuration, environment variables, or tool source files."}
	staged, err := c.StageTask(ctx, req)
	check(err)
	if !staged {
		t.Fatal("unexpected legacy route")
	}
	waitTurn := func(turn int) {
		t.Helper()
		for ctx.Err() == nil {
			err := c.Step(ctx)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				check(err)
			}
			var status string
			err = l.db.QueryRowContext(ctx, "SELECT state FROM runtime_commands WHERE task_id=$1 AND operation='task' AND turn=$2", taskID, turn).Scan(&status)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				check(err)
			}
			if status == "complete" {
				return
			}
			if status == "failed" || status == "canceled" {
				t.Fatalf("real MCP turn %d failed: %s", turn, status)
			}
			select {
			case <-ctx.Done():
			case <-time.After(200 * time.Millisecond):
			}
		}
		check(ctx.Err())
	}
	waitTurn(1)
	output := func(turn int) []byte {
		t.Helper()
		var encoded []byte
		check(l.db.QueryRowContext(ctx, "SELECT jsonb_agg(chunk ORDER BY seq) FROM runtime_events WHERE task_id=$1 AND turn=$2 AND chunk->>'event'='task-running'", taskID, turn).Scan(&encoded))
		var chunks []taskflow.TaskChunk
		check(json.Unmarshal(encoded, &chunks))
		var text []byte
		for _, chunk := range chunks {
			text = append(text, chunk.Data...)
		}
		return text
	}
	firstOutput := output(1)
	for _, receipt := range []string{fixture.StreamReceipt, fixture.LegacyReceipt, localReceipt} {
		if !bytes.Contains(firstOutput, []byte(receipt)) {
			t.Fatal("actual model/tool receipt missing from persisted history")
		}
	}
	environment, err := l.Environment(ctx, vm.ID)
	check(err)
	audit, err := engine.execute(ctx, environment.SandboxID, "cat /workspace/local-mcp-audit.txt", 4096)
	check(err)
	if strings.TrimSpace(audit) != "14 "+localReceipt {
		t.Fatal("local stdio process did not execute exactly once with projected secret env")
	}
	var sessionBefore string
	check(l.db.QueryRowContext(ctx, "SELECT session_id FROM runtime_task_sessions WHERE task_id=$1", taskID).Scan(&sessionBefore))
	if sessionBefore == "" {
		t.Fatal("provider session absent")
	}
	t.Log("real OpenCode/model executed persistent HTTP-SSE, legacy SSE, and local stdio; local secret env and audit verified")
	// This isolated test drives Step manually, unlike the product's resident
	// Worker. Drive it while the synchronous control API awaits its command.
	restarted := make(chan error, 1)
	go func() {
		resp, err := c.TaskManager().Restart(ctx, taskflow.RestartTaskReq{ID: taskID, RequestId: uuid.NewString(), LoadSession: true, ExecutionConfig: &taskflow.TaskExecutionConfig{McpServers: []taskflow.McpServerConfig{}}})
		if err == nil && (!resp.Success || resp.SessionID != sessionBefore) {
			err = errors.New("MCP clear did not retain provider session")
		}
		restarted <- err
	}()
awaitRestart:
	for ctx.Err() == nil {
		select {
		case err := <-restarted:
			check(err)
			break awaitRestart
		default:
		}
		err := c.Step(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			check(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	check(ctx.Err())
	check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "The MCP configuration has been cleared. If local_transport_probe, stream_transport_probe and legacy_transport_probe are all absent from available tools, reply MCP_TRANSPORTS_CLEARED. Do not read files or try other tools."}}))
	waitTurn(2)
	var sessionAfter string
	check(l.db.QueryRowContext(ctx, "SELECT session_id FROM runtime_task_sessions WHERE task_id=$1", taskID).Scan(&sessionAfter))
	if sessionAfter != sessionBefore {
		t.Fatal("MCP clear changed provider session")
	}
	generated, err := engine.execute(ctx, environment.SandboxID, "cat /root/.config/opencode/opencode.json", 64<<10)
	check(err)
	var cfg struct {
		MCP map[string]any `json:"mcp"`
	}
	check(json.Unmarshal([]byte(generated), &cfg))
	if len(cfg.MCP) != 0 {
		t.Fatal("explicit empty MCP list retained stale Guest configs")
	}
	if !bytes.Contains(output(2), []byte("MCP_TRANSPORTS_CLEARED")) {
		t.Fatal("model MCP-clear verification absent")
	}
	if !bytes.Equal(firstOutput, output(1)) {
		t.Fatal("MCP clear changed previous persisted output")
	}
	audit, err = engine.execute(ctx, environment.SandboxID, "cat /workspace/local-mcp-audit.txt", 4096)
	check(err)
	if strings.TrimSpace(audit) != "14 "+localReceipt {
		t.Fatal("removed local MCP executed again")
	}
	t.Log("explicit MCP clear removed generated configs while retaining provider session and previous history")
}
