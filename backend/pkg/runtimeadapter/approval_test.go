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
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type approvalTestServer struct {
	rpc.UnimplementedExecServiceHandler
	mu        sync.Mutex
	receipts  map[string]approvalCommand
	loseReply bool
}

func (s *approvalTestServer) Exec(_ context.Context, req *connect.Request[v2.ExecRequest]) (*connect.Response[v2.ExecResponse], error) {
	parts := strings.Split(req.Msg.Command.Args[1], "'")
	raw, err := base64.StdEncoding.DecodeString(parts[len(parts)-2])
	if err != nil {
		return nil, err
	}
	var policy approvalCommand
	if err = json.Unmarshal(raw, &policy); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.receipts[policy.CommandID]; ok && previous != policy {
		return nil, errors.New("policy replay conflict")
	}
	s.receipts[policy.CommandID] = policy
	if s.loseReply {
		s.loseReply = false
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("lost policy acknowledgement"))
	}
	return connect.NewResponse(&v2.ExecResponse{Result: &v2.ExecResult{Stdout: `{"data":{"success":true}}`}}), nil
}

func TestApprovalPolicyReconcilesBeforeAcknowledgingAndSerializesChanges(t *testing.T) {
	c, task, _, _ := controlFixture(t)
	server := &approvalTestServer{receipts: map[string]approvalCommand{}, loseReply: true}
	mux := http.NewServeMux()
	path, handler := rpc.NewExecServiceHandler(server)
	mux.Handle(path, handler)
	c.engines["node"] = testEngine(t, mux)
	if _, err := c.ledger.db.Exec(`UPDATE runtime_commands SET state='complete'`); err != nil {
		t.Fatal(err)
	}
	enabled := true
	request := taskflow.TaskApproveReq{ID: task.ID, AutoApprove: &enabled}
	// The request must commit its durable command before the wait expires. A
	// 20ms budget can expire during PostgreSQL admission under the race detector.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.TaskManager().AutoApprove(ctx, request); err == nil {
		t.Fatal("policy acknowledged before Worker executed")
	}
	if err := c.TaskManager().AutoApprove(ctx, request); err == nil {
		t.Fatal("replayed policy acknowledged while pending")
	}
	var count int
	if err := c.ledger.db.QueryRow(`SELECT count(*) FROM runtime_commands WHERE operation='approval'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate policy command: %d %v", count, err)
	}
	disabled := false
	if c.TaskManager().AutoApprove(context.Background(), taskflow.TaskApproveReq{ID: task.ID, AutoApprove: &disabled}) == nil {
		t.Fatal("opposite policy bypassed reconciliation fence")
	}
	if err := c.Step(context.Background()); err == nil {
		t.Fatal("lost acknowledgement was reported successful")
	}
	var state string
	if err := c.ledger.db.QueryRow(`SELECT state FROM runtime_commands WHERE operation='approval'`).Scan(&state); err != nil || state != "unknown" {
		t.Fatalf("mutation uncertainty lost: %s %v", state, err)
	}
	ready(t, c)
	if err := c.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.TaskManager().AutoApprove(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	if len(server.receipts) != 1 {
		t.Fatal("lost acknowledgement repeated a distinct policy")
	}
	for _, policy := range server.receipts {
		if !policy.Enabled || policy.TaskID != task.ID.String() || policy.Revision != 1 {
			t.Fatal("policy lost task scope or revision")
		}
	}
	server.mu.Unlock()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if c.TaskManager().AutoApprove(ctx2, taskflow.TaskApproveReq{ID: task.ID, AutoApprove: &disabled}) == nil {
		t.Fatal("disabled policy acknowledged before observation")
	}
	ready(t, c)
	if err := c.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.TaskManager().AutoApprove(context.Background(), taskflow.TaskApproveReq{ID: task.ID, AutoApprove: &disabled}); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.receipts) != 2 {
		t.Fatal("policy transition did not use a new revision")
	}
	for _, policy := range server.receipts {
		if policy.Revision == 2 && policy.Enabled {
			t.Fatal("disable policy was reversed")
		}
	}
}
