package runtimeadapter

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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

type runTestServer struct {
	rpc.UnimplementedRunServiceHandler
	rpc.UnimplementedExecServiceHandler
	rpc.UnimplementedSandboxServiceHandler
	mu                                          sync.Mutex
	status                                      v2.RunStatus
	requestID                                   string
	admitted, loseResponse, loseBeforeAdmission bool
	starts, lookups, stops, eventCount          int
	sandboxStopped                              bool
	resumes                                     int
}

func (s *runTestServer) GetSandbox(context.Context, *connect.Request[v2.GetSandboxRequest]) (*connect.Response[v2.GetSandboxResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := v2.SandboxStatus_SANDBOX_STATUS_RUNNING
	if s.sandboxStopped {
		status = v2.SandboxStatus_SANDBOX_STATUS_STOPPED
	}
	return connect.NewResponse(&v2.GetSandboxResponse{Sandbox: &v2.Sandbox{SandboxId: "sandbox-1", Status: status}}), nil
}
func (s *runTestServer) ResumeSandbox(context.Context, *connect.Request[v2.ResumeSandboxRequest]) (*connect.Response[v2.ResumeSandboxResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumes++
	s.sandboxStopped = false
	return connect.NewResponse(&v2.ResumeSandboxResponse{Sandbox: &v2.Sandbox{SandboxId: "sandbox-1", Status: v2.SandboxStatus_SANDBOX_STATUS_RUNNING}}), nil
}

// Existing lifecycle fixtures have no attachments; support the new explicit
// empty-selection receipt without simulating any file download or model input.
func emptyAttachmentFixture(raw []byte) (*connect.Response[v2.ExecResponse], bool) {
	var input map[string]json.RawMessage
	if json.Unmarshal(raw, &input) != nil {
		return nil, false
	}
	value, exists := input["attachments"]
	var files []taskflow.Attachment
	if !exists || json.Unmarshal(value, &files) != nil || len(files) != 0 {
		return nil, false
	}
	return connect.NewResponse(&v2.ExecResponse{Result: &v2.ExecResult{Stdout: `{"data":{"installed":true}}`}}), true
}
func (s *runTestServer) Exec(_ context.Context, req *connect.Request[v2.ExecRequest]) (*connect.Response[v2.ExecResponse], error) {
	parts := strings.Split(req.Msg.Command.Args[1], "'")
	raw, err := base64.StdEncoding.DecodeString(parts[len(parts)-2])
	if err != nil {
		return nil, err
	}
	if result, ok := emptyAttachmentFixture(raw); ok {
		return result, nil
	}
	var terminal struct {
		Op string `json:"op"`
	}
	if json.Unmarshal(raw, &terminal) == nil && terminal.Op == "attach" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.sandboxStopped {
			return nil, errors.New("terminal Exec attempted in a stopped sandbox")
		}
		return connect.NewResponse(&v2.ExecResponse{Result: &v2.ExecResult{Stdout: `{"data":{"offset":0}}`}}), nil
	}
	return nil, errors.New("unexpected lifecycle fixture Exec")
}

func (s *runTestServer) summary() *v2.RunSummary {
	return &v2.RunSummary{RunId: "run-1", SandboxId: "sandbox-1", Status: s.status}
}
func (s *runTestServer) StartAgentRun(_ context.Context, r *connect.Request[v2.StartAgentRunRequest]) (*connect.Response[v2.StartAgentRunResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts++
	if r.Msg.Run.ClientRequestId == "" || r.Msg.Run.CleanupPolicy != v2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING || r.Msg.Run.SandboxId != "sandbox-1" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid run contract"))
	}
	if s.requestID != "" && s.requestID != r.Msg.Run.ClientRequestId {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("changed admission key"))
	}
	s.requestID = r.Msg.Run.ClientRequestId
	if s.loseBeforeAdmission {
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("request outcome unknown"))
	}
	s.admitted = true
	if s.loseResponse {
		s.loseResponse = false
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("response lost after commit"))
	}
	return connect.NewResponse(&v2.StartAgentRunResponse{Run: s.summary()}), nil
}
func (s *runTestServer) ListRuns(_ context.Context, r *connect.Request[v2.ListRunsRequest]) (*connect.Response[v2.ListRunsResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups++
	if r.Msg.Labels["monkeycode_command"] != s.requestID {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("reconciliation did not use command identity"))
	}
	response := &v2.ListRunsResponse{}
	if s.admitted {
		response.Runs = []*v2.RunSummary{s.summary()}
		response.Total = 1
	}
	return connect.NewResponse(response), nil
}
func (s *runTestServer) GetRun(context.Context, *connect.Request[v2.GetRunRequest]) (*connect.Response[v2.GetRunResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return connect.NewResponse(&v2.GetRunResponse{Run: &v2.RunDetail{Summary: s.summary()}}), nil
}
func (s *runTestServer) StopRun(context.Context, *connect.Request[v2.StopRunRequest]) (*connect.Response[v2.StopRunResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stops++
	return connect.NewResponse(&v2.StopRunResponse{}), nil
}
func (s *runTestServer) ListRunEvents(_ context.Context, r *connect.Request[v2.ListRunEventsRequest]) (*connect.Response[v2.ListRunEventsResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	response := &v2.ListRunEventsResponse{Total: uint32(s.eventCount)}
	for i := r.Msg.Offset; i < uint32(s.eventCount) && i < r.Msg.Offset+r.Msg.Limit; i++ {
		response.Events = append(response.Events, &v2.RunEvent{Seq: uint64(i + 1), Kind: v2.RunEventKind_RUN_EVENT_KIND_AGENT_MESSAGE, Text: "片段"})
	}
	return connect.NewResponse(response), nil
}
func workerFixture(t *testing.T, server *runTestServer) (*Client, taskflow.CreateTaskReq) {
	t.Helper()
	l := testLedger(t)
	req := stageFixture(t, l)
	ctx := context.Background()
	prepare, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.UpdateCommand(ctx, prepare, "complete", "prepare-run", 0); err != nil {
		t.Fatal(err)
	}
	if err = l.SetEnvironment(ctx, req.VMID, "project-1", "sandbox-1", "online"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	path, handler := rpc.NewRunServiceHandler(server)
	mux.Handle(path, handler)
	path, handler = rpc.NewExecServiceHandler(server)
	mux.Handle(path, handler)
	path, handler = rpc.NewSandboxServiceHandler(server)
	mux.Handle(path, handler)
	c := &Client{ledger: l, engines: map[string]*Engine{"node": testEngine(t, mux)}, nodes: map[string]config.RuntimeNode{"node": {ID: "node"}}, poll: time.Millisecond, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err = c.TaskManager().Create(ctx, req); err != nil {
		t.Fatal(err)
	}
	return c, req
}
func TestNewUserTurnResumesFencedGuestButAdmissionPollingDoesNot(t *testing.T) {
	server := &runTestServer{status: v2.RunStatus_RUN_STATUS_RUNNING, sandboxStopped: true, loseResponse: true}
	c, _ := workerFixture(t, server)
	if err := c.Step(context.Background()); connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Fatal("expected lost submission reply", err)
	}
	server.mu.Lock()
	if server.resumes != 1 {
		t.Fatal("new user turn did not resume exactly once")
	}
	server.sandboxStopped = true // A later daemon interruption fenced this Run.
	server.mu.Unlock()
	ready(t, c)
	if err := c.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.resumes != 1 || !server.sandboxStopped || server.starts != 1 {
		t.Fatal("admission reconciliation resumed or replayed an interrupted Run")
	}
}

func ready(t *testing.T, c *Client) {
	t.Helper()
	if _, err := c.ledger.db.Exec(`UPDATE runtime_commands SET available_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
}
func taskState(t *testing.T, c *Client, id string) string {
	t.Helper()
	var state string
	if err := c.ledger.db.QueryRow(`SELECT status FROM tasks WHERE id=$1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}
func TestWorkerLostAdmissionAndEventDrain(t *testing.T) {
	server := &runTestServer{status: v2.RunStatus_RUN_STATUS_SUCCEEDED, loseResponse: true, eventCount: 501}
	c, req := workerFixture(t, server)
	ctx := context.Background()
	if err := c.Step(ctx); connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Fatalf("expected ambiguous admission: %v", err)
	}
	ready(t, c)
	if err := c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if taskState(t, c, req.ID.String()) != "processing" {
		t.Fatal("task finished before all events were drained")
	}
	ready(t, c)
	if err := c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := c.ledger.Events(ctx, req.ID.String(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 503 || events[len(events)-1].Event != "task-ended" {
		t.Fatalf("missing/duplicate events: %d", len(events))
	}
	if server.starts != 1 || server.lookups != 1 {
		t.Fatalf("submission replayed instead of reconciled: starts=%d lookups=%d", server.starts, server.lookups)
	}
	if taskState(t, c, req.ID.String()) != "processing" {
		t.Fatal("completed round closed the reusable business task")
	}
	// A repeated VM-ready callback must not reopen a recycled business task.
	if _, err = c.ledger.db.Exec(`UPDATE tasks SET status='finished' WHERE id=$1`, req.ID); err != nil {
		t.Fatal(err)
	}
	if err = c.TaskManager().Create(ctx, req); err != nil {
		t.Fatal(err)
	}
	if taskState(t, c, req.ID.String()) != "finished" {
		t.Fatal("replayed admission regressed task status")
	}
}
func TestWorkerCancellationWaitsForRemoteTerminal(t *testing.T) {
	server := &runTestServer{status: v2.RunStatus_RUN_STATUS_RUNNING, loseResponse: true}
	c, req := workerFixture(t, server)
	ctx := context.Background()
	_ = c.Step(ctx)
	if err := c.TaskManager().Cancel(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: req.ID}}); err != nil {
		t.Fatal(err)
	}
	ready(t, c)
	if err := c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if taskState(t, c, req.ID.String()) != "processing" || server.stops != 1 {
		t.Fatal("cancellation acknowledged as completion before remote stop")
	}
	server.mu.Lock()
	server.status = v2.RunStatus_RUN_STATUS_CANCELED
	server.mu.Unlock()
	ready(t, c)
	if err := c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if taskState(t, c, req.ID.String()) != "processing" {
		t.Fatal("confirmed round cancellation closed the business task")
	}
}
func TestCanceledUnknownAdmissionDoesNotStartNewRun(t *testing.T) {
	server := &runTestServer{status: v2.RunStatus_RUN_STATUS_RUNNING, loseBeforeAdmission: true}
	c, req := workerFixture(t, server)
	ctx := context.Background()
	_ = c.Step(ctx)
	if err := c.TaskManager().Cancel(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: req.ID}}); err != nil {
		t.Fatal(err)
	}
	ready(t, c)
	if err := c.Step(ctx); !errors.Is(err, errAdmissionUncertain) {
		t.Fatalf("ambiguous cancellation incorrectly finalized: %v", err)
	}
	if server.starts != 1 || taskState(t, c, req.ID.String()) != "processing" {
		t.Fatal("uncertain request replayed or falsely finished")
	}
}
func TestCancellationBeforeSubmission(t *testing.T) {
	server := &runTestServer{status: v2.RunStatus_RUN_STATUS_RUNNING}
	c, req := workerFixture(t, server)
	ctx := context.Background()
	if err := c.TaskManager().Cancel(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: req.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if server.starts != 0 || taskState(t, c, req.ID.String()) != "processing" {
		t.Fatal("canceled pending command executed")
	}
}

type rejectedProjectServer struct {
	rpc.UnimplementedProjectServiceHandler
}

func (*rejectedProjectServer) ApplyProject(context.Context, *connect.Request[v2.ApplyProjectRequest]) (*connect.Response[v2.ApplyProjectResponse], error) {
	// ApplyProject can return HTTP success with a failed validation result.
	return connect.NewResponse(&v2.ApplyProjectResponse{Issues: []*v2.ProjectValidationIssue{{Severity: v2.ProjectValidationSeverity_PROJECT_VALIDATION_SEVERITY_ERROR, Message: "invalid transport"}}}), nil
}

func TestRejectedProjectDoesNotRetryOrStartRun(t *testing.T) {
	l := testLedger(t)
	req := stageFixture(t, l)
	mux := http.NewServeMux()
	path, handler := rpc.NewProjectServiceHandler(&rejectedProjectServer{})
	mux.Handle(path, handler)
	server := &runTestServer{}
	path, handler = rpc.NewRunServiceHandler(server)
	mux.Handle(path, handler)
	c := &Client{ledger: l, engines: map[string]*Engine{"node": testEngine(t, mux)}, nodes: map[string]config.RuntimeNode{"node": {ID: "node", GuestImage: "image"}}}
	if err := c.Step(context.Background()); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("validation issues were ignored: %v", err)
	}
	var state string
	if err := l.db.QueryRow(`SELECT state FROM runtime_commands WHERE environment_id=$1`, req.VMID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || taskState(t, c, req.ID.String()) != "error" || server.starts != 0 {
		t.Fatal("invalid project retried or admitted a Run")
	}
	if err := c.Step(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid project was retried: %v", err)
	}
}

func TestBusinessStopWaitsAndRejectsContinuation(t *testing.T) {
	server := &runTestServer{status: v2.RunStatus_RUN_STATUS_RUNNING}
	c, req := workerFixture(t, server)
	if err := c.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.StopTaskAndWait(ctx, req.ID.String()); done <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		stopped := server.stops > 0
		server.mu.Unlock()
		if stopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stop request not delivered")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("business stop returned before remote cancellation: %v", err)
	default:
	}
	if c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: req.ID, Text: "racing input"}}) == nil {
		t.Fatal("continuation admitted while stopping")
	}
	server.mu.Lock()
	server.status = v2.RunStatus_RUN_STATUS_CANCELED
	server.mu.Unlock()
	ready(t, c)
	if err := c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if taskState(t, c, req.ID.String()) != "processing" {
		t.Fatal("runtime finalized business status before recycle")
	}
	e, err := c.ledger.Environment(ctx, req.VMID)
	if err != nil || e.State != "stopping" {
		t.Fatalf("stop state lost: %v", err)
	}
}

func TestBusinessStopTimeoutDoesNotFinalize(t *testing.T) {
	server := &runTestServer{status: v2.RunStatus_RUN_STATUS_RUNNING}
	c, req := workerFixture(t, server)
	if err := c.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	managed, err := c.StopTaskAndWait(ctx, req.ID.String())
	if !managed || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop timeout lost: %v", err)
	}
	commands, err := c.ledger.ActiveCommands(context.Background(), req.ID.String())
	if err != nil || len(commands) != 1 || taskState(t, c, req.ID.String()) != "processing" {
		t.Fatal("uncertain cancellation finalized the task")
	}
}
