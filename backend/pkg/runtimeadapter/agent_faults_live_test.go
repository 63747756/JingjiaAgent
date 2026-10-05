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
	"net/http/httputil"
	"net/url"
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

// Uses a real Agent/model/tool and durable PostgreSQL. Only the authenticated
// business callback is a fixture; a transport proxy injects lost replies/outages.
func TestLiveAgentFaultRecovery(t *testing.T) {
	if os.Getenv("JINGJIAAGENT_RUNTIME_AGENT_FAULT_LIVE_TEST") != "1" {
		t.Skip("requires isolated daemon, PostgreSQL and real model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var model struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
	}
	data, err := os.ReadFile(os.Getenv("JINGJIAAGENT_RUNTIME_MODEL_CONFIG"))
	if err != nil || json.Unmarshal(data, &model) != nil || model.APIKey == "" {
		t.Fatal("private model config unavailable")
	}
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(strings.ReplaceAll(err.Error(), model.APIKey, "[redacted]"))
		}
	}
	l := testLedger(t)
	l.capacity = config.RuntimeCapacity{Enabled: true, MaxCPUMillis: 1000, MaxMemoryBytes: 2 << 30}
	node := config.RuntimeNode{ID: "agent-fault", URL: os.Getenv("JINGJIAAGENT_RUNTIME_TEST_URL"), TokenFile: os.Getenv("JINGJIAAGENT_RUNTIME_TEST_TOKEN_FILE"), GuestImage: os.Getenv("JINGJIAAGENT_RUNTIME_TEST_GUEST_IMAGE")}
	direct, err := NewEngine(node)
	check(err)
	target, err := url.Parse(node.URL)
	check(err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(502) }
	var lose, outage atomic.Bool
	var submissions, lookups atomic.Int32
	fault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if outage.Load() {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/agentcompose.v2.RunService/ListRuns" {
			lookups.Add(1)
		}
		if r.URL.Path == "/agentcompose.v2.RunService/StartAgentRun" && lose.CompareAndSwap(true, false) {
			submissions.Add(1)
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, r)
			if rec.Code != 200 {
				t.Error("actual Agent submission was not accepted")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"code":"unavailable","message":"fixture discarded task admission reply"}`))
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer fault.Close()
	proxied := node
	proxied.URL = fault.URL
	engine, err := NewEngine(proxied)
	check(err)
	c := &Client{ledger: l, backend: "agent_compose", nodes: map[string]config.RuntimeNode{node.ID: proxied}, engines: map[string]*Engine{node.ID: engine}, registry: &nodeRegistryFixture{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), poll: 100 * time.Millisecond, callbackToken: "agent-fault-test-only"}
	check(c.SyncNodes(ctx))
	taskID, owner := uuid.New(), uuid.NewString()
	vm, err := c.VirtualMachiner().Create(ctx, &taskflow.CreateVirtualMachineReq{UserID: owner, HostID: node.ID, TaskID: taskID, Cores: "1", Memory: 2 << 30})
	check(err)
	_, err = l.db.ExecContext(ctx, `INSERT INTO tasks(id,status) VALUES($1,'pending')`, taskID)
	check(err)
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-fault-test-only" {
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
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		env, e := l.Environment(cleanup, vm.ID)
		if e == nil && env.SandboxID != "" {
			_, _ = direct.sandboxes.RemoveSandbox(cleanup, connect.NewRequest(&v2.RemoveSandboxRequest{SandboxId: env.SandboxID, Force: true}))
		}
	})
	marker := "FAULT_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	duration := "12"
	if os.Getenv("JINGJIAAGENT_RUNTIME_DAEMON_RESTART_LIVE_TEST") == "1" {
		duration = "90"
	}
	req := taskflow.CreateTaskReq{ID: taskID, VMID: vm.ID, CodingAgent: taskflow.CodingAgentOpenCode, LLM: taskflow.LLM{BaseURL: model.BaseURL, ApiKey: model.APIKey, Model: model.Model}, Text: "Use bash exactly once to run: printf 'started\\n' >> /workspace/fault-counter.txt; sleep " + duration + "; printf '" + marker + "' > /workspace/fault-result.txt. Then reply only with " + marker + ". Do not retry the command."}
	staged, err := c.StageTask(ctx, req)
	check(err)
	if !staged {
		t.Fatal("not routed to compose")
	}
	step := func() {
		t.Helper()
		e := c.Step(ctx)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			check(e)
		}
	}
	for {
		step()
		var state string
		check(l.db.QueryRowContext(ctx, `SELECT state FROM runtime_commands WHERE environment_id=$1 AND operation='prepare'`, vm.ID).Scan(&state))
		if state == "complete" {
			break
		}
		if state == "failed" || ctx.Err() != nil {
			t.Fatal("preparation failed or timed out")
		}
		time.Sleep(100 * time.Millisecond)
	}
	lose.Store(true)
	for {
		e := c.Step(ctx)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("lost-reply injection not reached")
		}
		time.Sleep(100 * time.Millisecond)
	}
	var commandID, state, runID string
	var submitted bool
	check(l.db.QueryRowContext(ctx, `SELECT id,state,run_id,submission_started FROM runtime_commands WHERE task_id=$1 AND operation='task'`, taskID).Scan(&commandID, &state, &runID, &submitted))
	if state != "unknown" || runID != "" || !submitted || submissions.Load() != 1 {
		t.Fatal("uncertain business Run not retained")
	}
	fresh := func() {
		c = &Client{ledger: l, backend: c.backend, nodes: c.nodes, engines: c.engines, registry: &nodeRegistryFixture{}, logger: c.logger, poll: c.poll, callbackURL: c.callbackURL, callbackToken: c.callbackToken, http: c.http}
	}
	fresh()
	check(c.SyncNodes(ctx))
	for runID == "" {
		step()
		check(l.db.QueryRowContext(ctx, `SELECT state,run_id FROM runtime_commands WHERE id=$1`, commandID).Scan(&state, &runID))
		if ctx.Err() != nil {
			t.Fatal("lost admission not reconciled")
		}
		time.Sleep(100 * time.Millisecond)
	}
	env, err := l.Environment(ctx, vm.ID)
	check(err)
	for {
		value, e := direct.execute(ctx, env.SandboxID, "test ! -f /workspace/fault-counter.txt || cat /workspace/fault-counter.txt", 1024)
		check(e)
		if strings.Contains(value, "started") {
			break
		}
		step()
		if ctx.Err() != nil {
			t.Fatal("real model did not execute tool")
		}
		time.Sleep(200 * time.Millisecond)
	}
	_, watermark, err := snapshot(ctx, l, taskID)
	check(err)
	if watermark == 0 {
		t.Fatal("no pre-outage durable output")
	}
	outage.Store(true)
	check(c.SyncNodes(ctx)) // Individual transport failures are recorded, not returned.
	var ready bool
	check(l.db.QueryRowContext(ctx, `SELECT ready FROM runtime_nodes WHERE node_id=$1`, node.ID).Scan(&ready))
	if ready {
		t.Fatal("node outage not observed")
	}
	_, err = l.db.ExecContext(ctx, `UPDATE runtime_commands SET available_at=now() WHERE id=$1`, commandID)
	check(err)
	if c.Step(ctx) == nil {
		t.Fatal("outage treated as successful completion")
	}
	check(l.db.QueryRowContext(ctx, `SELECT state,run_id FROM runtime_commands WHERE id=$1`, commandID).Scan(&state, &runID))
	if state != "unknown" || runID == "" {
		t.Fatal("outage lost admitted Run or marked terminal")
	}
	if count, _, _ := capacityUsage(t, l); count != 1 {
		t.Fatal("outage released capacity")
	}
	crash := os.Getenv("JINGJIAAGENT_RUNTIME_DAEMON_RESTART_LIVE_TEST") == "1"
	if crash {
		// The runner exposes this operation only for its validated, independent
		// daemon. Never infer a production container name in a test.
		request, e := http.NewRequestWithContext(ctx, http.MethodPost, os.Getenv("JINGJIAAGENT_RUNTIME_TEST_FAULT_CONTROLLER")+"/restart", strings.NewReader(string(mustJSON(map[string]string{"run_id": runID, "sandbox_id": env.SandboxID}))))
		check(e)
		request.Header.Set("Authorization", "Bearer "+os.Getenv("JINGJIAAGENT_RUNTIME_TEST_FAULT_TOKEN"))
		request.Header.Set("Content-Type", "application/json")
		response, e := http.DefaultClient.Do(request)
		check(e)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatal("isolated daemon process restart failed")
		}
	}
	// Real Guest/tool continues while all business-side node calls are unavailable.
	time.Sleep(14 * time.Second)
	outage.Store(false)
	fresh()
	check(c.SyncNodes(ctx))
	for {
		step()
		check(l.db.QueryRowContext(ctx, `SELECT state FROM runtime_commands WHERE id=$1`, commandID).Scan(&state))
		if state == "complete" || (crash && state == "failed") {
			break
		}
		if state == "failed" || state == "canceled" || ctx.Err() != nil {
			t.Fatal("business Run did not recover")
		}
		time.Sleep(100 * time.Millisecond)
	}
	runs, err := direct.runs.ListRuns(ctx, connect.NewRequest(&v2.ListRunsRequest{ProjectId: env.ProjectID, Labels: map[string]string{"jingjiaagent_command": commandID}, Limit: 2}))
	check(err)
	if len(runs.Msg.Runs) != 1 || runs.Msg.Total != 1 || runs.Msg.Runs[0].RunId != runID || submissions.Load() != 1 || lookups.Load() < 1 {
		t.Fatal("business Run duplicated")
	}
	if crash {
		sandbox, e := direct.sandboxes.GetSandbox(ctx, connect.NewRequest(&v2.GetSandboxRequest{SandboxId: env.SandboxID}))
		check(e)
		if sandbox.Msg.Sandbox.Status != v2.SandboxStatus_SANDBOX_STATUS_STOPPED {
			t.Fatalf("interrupted Guest was not fenced before publishing Run failure: status=%s", sandbox.Msg.Sandbox.Status)
		}
		_, e = direct.sandboxes.ResumeSandbox(ctx, connect.NewRequest(&v2.ResumeSandboxRequest{SandboxId: env.SandboxID}))
		check(e)
	}
	readProof := "cat /workspace/fault-counter.txt; test ! -f /workspace/fault-result.txt || cat /workspace/fault-result.txt"
	contents, err := direct.execute(ctx, env.SandboxID, readProof, 2048)
	check(err)
	if contents != "started\n"+marker && (!crash || contents != "started\n") {
		t.Fatal("real tool repeated or lost workspace output")
	}
	if crash {
		if state != "failed" {
			t.Fatal("daemon interruption did not reach an explicit failed state")
		}
		detail, e := direct.runs.GetRun(ctx, connect.NewRequest(&v2.GetRunRequest{RunId: runID}))
		check(e)
		if !strings.Contains(detail.Msg.Run.Summary.Error, "daemon interrupted") {
			t.Fatal("daemon interruption reason missing")
		}
		priorContents := contents
		_, e = direct.sandboxes.StopSandbox(ctx, connect.NewRequest(&v2.StopSandboxRequest{SandboxId: env.SandboxID}))
		check(e)
		check(c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: taskID, Text: "Read /workspace/fault-counter.txt with a file read tool, reply with its contents followed by RECOVERY_OK. Do not use bash, rewrite files, or retry any previous command."}}))
		for {
			step()
			check(l.db.QueryRowContext(ctx, `SELECT state FROM runtime_commands WHERE task_id=$1 AND operation='task' AND turn=2`, taskID).Scan(&state))
			if state == "complete" {
				break
			}
			if state == "failed" || state == "canceled" || ctx.Err() != nil {
				t.Fatal("user follow-up after daemon recovery failed")
			}
			time.Sleep(100 * time.Millisecond)
		}
		contents, e = direct.execute(ctx, env.SandboxID, readProof, 2048)
		check(e)
		if contents != priorContents {
			t.Fatal("daemon recovery or follow-up repeated an earlier side effect")
		}
		t.Log("actual daemon SIGKILL/restart: original Run explicitly failed, no replay; workspace retained; subsequent user follow-up succeeded without repeating the tool")
	}
	var duplicates, ended int
	check(l.db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT command_id,source_key,count(*) FROM runtime_events WHERE task_id=$1 GROUP BY command_id,source_key HAVING count(*)>1) d`, taskID).Scan(&duplicates))
	check(l.db.QueryRowContext(ctx, `SELECT count(*) FROM runtime_events WHERE command_id=$1 AND source_key='task-ended'`, commandID).Scan(&ended))
	if duplicates != 0 || ended != 1 {
		t.Fatal("replayed events duplicated or terminal event missing")
	}
	events, err := l.Events(ctx, taskID.String(), int64(watermark))
	check(err)
	if len(events) == 0 {
		t.Fatal("reconnect did not replay missed output")
	}
	final, _, err := snapshot(ctx, l, taskID)
	check(err)
	encoded, err := json.Marshal(final)
	check(err)
	expected := marker
	if crash {
		expected = "RECOVERY_OK"
	}
	if !strings.Contains(string(encoded), expected) {
		t.Fatal("final model output missing from durable history")
	}
	if !crash {
		t.Log("real OpenCode/DeepSeek tool: lost task admission reply reconciled to one Run; node transport outage plus fresh Worker recovered output and one terminal event; tool executed once; capacity retained")
	}
}
