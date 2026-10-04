package runtimeadapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type controlTestServer struct {
	rpc.UnimplementedProjectServiceHandler
	rpc.UnimplementedExecServiceHandler
	mu              sync.Mutex
	session, model  string
	receipts        map[string]string
	resets          int
	failSessionOnce bool
}

func (s *controlTestServer) ApplyProject(_ context.Context, request *connect.Request[v2.ApplyProjectRequest]) (*connect.Response[v2.ApplyProjectResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model = request.Msg.Spec.Agents[0].Model
	return connect.NewResponse(&v2.ApplyProjectResponse{Project: &v2.Project{Summary: &v2.ProjectSummary{ProjectId: "project-1"}}}), nil
}

func (s *controlTestServer) Exec(_ context.Context, request *connect.Request[v2.ExecRequest]) (*connect.Response[v2.ExecResponse], error) {
	parts := strings.Split(request.Msg.Command.Args[1], "'")
	if len(parts) < 2 {
		return nil, errors.New("invalid Exec arguments")
	}
	data, err := base64.StdEncoding.DecodeString(parts[len(parts)-2])
	if err != nil {
		return nil, err
	}
	if result, ok := emptyAttachmentFixture(data); ok {
		return result, nil
	}
	var input struct {
		Op, Provider string
		ID           string `json:"command_id"`
		Keep         bool   `json:"load_session"`
	}
	if err = json.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	if input.Op != "restart" || input.Provider != "opencode" {
		return nil, errors.New("unexpected control operation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failSessionOnce {
		s.failSessionOnce = false
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("fixture control failure after project mutation"))
	}
	id, exists := s.receipts[input.ID]
	if !exists {
		if !input.Keep {
			s.session = ""
			s.resets++
		}
		id = s.session
		s.receipts[input.ID] = id
	}
	return connect.NewResponse(&v2.ExecResponse{Result: &v2.ExecResult{Stdout: string(mustJSON(map[string]any{"data": map[string]any{"session_id": id}}))}}), nil
}

func controlFixture(t *testing.T) (*Client, taskflow.CreateTaskReq, *runTestServer, *controlTestServer) {
	t.Helper()
	runs := &runTestServer{status: v2.RunStatus_RUN_STATUS_RUNNING}
	c, request := workerFixture(t, runs)
	controls := &controlTestServer{session: "ses_original", receipts: map[string]string{}}
	mux := http.NewServeMux()
	path, handler := rpc.NewRunServiceHandler(runs)
	mux.Handle(path, handler)
	path, handler = rpc.NewProjectServiceHandler(controls)
	mux.Handle(path, handler)
	path, handler = rpc.NewExecServiceHandler(controls)
	mux.Handle(path, handler)
	path, handler = rpc.NewSandboxServiceHandler(runs)
	mux.Handle(path, handler)
	c.engines["node"] = testEngine(t, mux)
	c.nodes["node"] = config.RuntimeNode{ID: "node", GuestImage: "test-image"}
	return c, request, runs, controls
}

func waitRestartAdmission(t *testing.T, c *Client, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := c.ledger.db.QueryRow(`SELECT count(*) FROM runtime_commands WHERE task_id=$1 AND operation='restart'`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("restart was not durably admitted")
}

func TestRestartWaitsForCancellationAndUpdatesFutureTurns(t *testing.T) {
	c, task, runs, controls := controlFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	request := taskflow.RestartTaskReq{ID: task.ID, RequestId: "switch-1", LoadSession: true, ExecutionConfig: &taskflow.TaskExecutionConfig{LLM: &taskflow.LLM{Model: "second-model", ApiKey: "second-key"}}}
	type outcome struct {
		Response *taskflow.RestartTaskResp
		Err      error
	}
	result := make(chan outcome, 1)
	go func() { response, err := c.TaskManager().Restart(ctx, request); result <- outcome{response, err} }()
	waitRestartAdmission(t, c, task.ID.String())
	if err := c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: task.ID, Text: "racing continuation"}}); err == nil {
		t.Fatal("continuation bypassed restart fence")
	}
	ready(t, c)
	if err := c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-result:
		t.Fatal("restart acknowledged before remote cancellation")
	default:
	}
	runs.mu.Lock()
	runs.status = v2.RunStatus_RUN_STATUS_CANCELED
	runs.mu.Unlock()
	for index := 0; index < 4; index++ {
		ready(t, c)
		if err := c.Step(ctx); err != nil {
			t.Fatal(err)
		}
		var state string
		if err := c.ledger.db.QueryRow(`SELECT state FROM runtime_commands WHERE task_id=$1 AND operation='restart'`, task.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "complete" {
			break
		}
	}
	got := <-result
	if got.Err != nil || got.Response == nil || !got.Response.Success || got.Response.SessionID != "ses_original" {
		t.Fatalf("restart result: %+v", got)
	}
	before := controls.resets
	repeated, err := c.TaskManager().Restart(ctx, request)
	if err != nil || repeated.SessionID != "ses_original" || controls.resets != before {
		t.Fatal("repeat request changed session")
	}
	request.LoadSession = false
	if _, err = c.TaskManager().Restart(ctx, request); err == nil {
		t.Fatal("conflicting repeated request accepted")
	}
	// A ready callback replay after a model switch still carries the original
	// admission request. It must neither create another Run nor restore the old
	// execution configuration for subsequent turns.
	if accepted, err := c.StageTask(ctx, task); !accepted || err != nil {
		t.Fatalf("original admission retry failed after restart: %v", err)
	}
	prepared, err := c.PreparedTask(ctx, task.ID.String())
	if err != nil || prepared == nil || prepared.LLM.Model != task.LLM.Model {
		t.Fatalf("ready callback returned mutable configuration: %v", err)
	}
	if err = c.TaskManager().Create(ctx, *prepared); err != nil {
		t.Fatalf("ready callback replay failed after restart: %v", err)
	}
	changed := task
	changed.Text = "conflicting initial submission"
	if _, err = c.StageTask(ctx, changed); err == nil {
		t.Fatal("conflicting initial submission accepted after restart")
	}
	var initialCommands int
	if err = c.ledger.db.QueryRowContext(ctx, `SELECT count(*) FROM runtime_commands WHERE task_id=$1 AND operation='task'`, task.ID).Scan(&initialCommands); err != nil || initialCommands != 1 {
		t.Fatalf("ready callback replay created an extra task command: %d, %v", initialCommands, err)
	}
	if err = c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: task.ID, Text: "new turn"}}); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	var commandID string
	if err = c.ledger.db.QueryRow(`SELECT id,payload FROM runtime_commands WHERE task_id=$1 AND operation='task' AND turn=2`, task.ID).Scan(&commandID, &payload); err != nil {
		t.Fatal(err)
	}
	decoded, err := c.ledger.open(commandID, payload)
	if err != nil {
		t.Fatal(err)
	}
	var next taskflow.CreateTaskReq
	if err = json.Unmarshal(decoded, &next); err != nil {
		t.Fatal(err)
	}
	if next.LLM.Model != "second-model" || next.LLM.ApiKey != "second-key" {
		t.Fatal("continuation used stale model configuration")
	}
}

func TestRestartSurvivesCallerTimeoutAndClearsOnlyOnce(t *testing.T) {
	c, task, _, controls := controlFixture(t)
	// No active Run; this task command has not crossed admission.
	request := taskflow.RestartTaskReq{ID: task.ID, RequestId: "clear-1", LoadSession: false}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.TaskManager().Restart(ctx, request); err == nil {
		t.Fatal("restart acknowledged without Worker")
	}
	for index := 0; index < 2; index++ {
		ready(t, c)
		if err := c.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	response, err := c.TaskManager().Restart(context.Background(), request)
	if err != nil || !response.Success || response.SessionID != "" || controls.resets != 1 {
		t.Fatalf("lost caller result was not recovered: %v", err)
	}
	var session string
	if err = c.ledger.db.QueryRow(`SELECT session_id FROM runtime_task_sessions WHERE task_id=$1`, task.ID).Scan(&session); err != nil || session != "" {
		t.Fatal("session reset mapping missing")
	}
}

func TestRestartMutationFailureRetainsFenceUntilReconciled(t *testing.T) {
	c, task, _, controls := controlFixture(t)
	controls.failSessionOnce = true
	request := taskflow.RestartTaskReq{ID: task.ID, RequestId: "uncertain-control", LoadSession: true,
		ExecutionConfig: &taskflow.TaskExecutionConfig{LLM: &taskflow.LLM{Model: "replacement-model"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.TaskManager().Restart(ctx, request); err == nil {
		t.Fatal("restart acknowledged without a Worker")
	}
	ready(t, c)
	if err := c.Step(context.Background()); err != nil { // Cancel the unsubmitted task command.
		t.Fatal(err)
	}
	ready(t, c)
	if err := c.Step(context.Background()); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected post-mutation control failure: %v", err)
	}
	var state string
	var submitted bool
	if err := c.ledger.db.QueryRow(`SELECT state,submission_started FROM runtime_commands WHERE task_id=$1 AND operation='restart'`, task.ID).Scan(&state, &submitted); err != nil || state != "unknown" || !submitted {
		t.Fatalf("partially applied restart lost its reconciliation fence: %s, %v", state, err)
	}
	if err := c.TaskManager().Continue(context.Background(), taskflow.TaskReq{Task: &taskflow.Task{ID: task.ID, Text: "must remain blocked"}}); err == nil {
		t.Fatal("continuation observed partially applied runtime configuration")
	}
	ready(t, c)
	if err := c.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, err := c.TaskManager().Restart(context.Background(), request)
	if err != nil || !response.Success || response.SessionID != "ses_original" {
		t.Fatalf("restart did not reconcile the original request: %v", err)
	}
	if err = c.TaskManager().Continue(context.Background(), taskflow.TaskReq{Task: &taskflow.Task{ID: task.ID, Text: "recovered turn"}}); err != nil {
		t.Fatal(err)
	}
}
