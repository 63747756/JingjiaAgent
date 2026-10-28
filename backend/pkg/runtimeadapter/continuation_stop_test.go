package runtimeadapter

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func blockedContinuationForStop(t *testing.T, c *Client, req taskflow.CreateTaskReq) Command {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := rpc.NewSandboxServiceHandler(unreachableSandbox{})
	mux.Handle(path, handler)
	c.engines["node"] = testEngine(t, mux)
	cmd := acceptedContinuation(t, c, req, "continue after preparation recovers")
	if _, err := c.ledger.db.Exec(`UPDATE runtime_commands SET preparation_started_at=now()-interval '6 minutes' WHERE id=$1`, cmd.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.prepareContinuation(context.Background(), &cmd); err != nil {
		t.Fatal(err)
	}
	if state, _ := continuationState(t, c, cmd.ID); state != "blocked" {
		t.Fatalf("preparation failure did not block continuation: %s", state)
	}
	return cmd
}

func assertStoppedContinuation(t *testing.T, c *Client, cmd Command) {
	t.Helper()
	var canceled, unleased bool
	var state string
	var ended int
	if err := c.ledger.db.QueryRow(`SELECT state,cancel_requested,lease_token IS NULL AND lease_until IS NULL FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&state, &canceled, &unleased); err != nil {
		t.Fatal(err)
	}
	if state != "canceled" || !canceled || !unleased {
		t.Fatalf("continuation not settled: state=%s canceled=%v unleased=%v", state, canceled, unleased)
	}
	if err := c.ledger.db.QueryRow(`SELECT count(*) FROM runtime_events WHERE command_id=$1 AND source_key='task-ended'`, cmd.ID).Scan(&ended); err != nil || ended != 1 {
		t.Fatalf("expected one terminal receipt, got %d: %v", ended, err)
	}
}

func TestBusinessStopSettlesWaitingAndBlockedContinuations(t *testing.T) {
	for _, state := range []string{"waiting", "blocked"} {
		t.Run(state, func(t *testing.T) {
			c, req, _ := continuationFixture(t)
			var cmd Command
			if state == "blocked" {
				cmd = blockedContinuationForStop(t, c, req)
			} else {
				cmd = acceptedContinuation(t, c, req, "queued message")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if managed, err := c.StopTaskAndWait(ctx, req.ID.String()); err != nil || !managed {
				t.Fatalf("business stop failed: managed=%v err=%v", managed, err)
			}
			assertStoppedContinuation(t, c, cmd)
			var environmentState string
			if err := c.ledger.db.QueryRow(`SELECT state FROM runtime_environments WHERE id=$1`, req.VMID).Scan(&environmentState); err != nil || environmentState != "stopping" {
				t.Fatalf("business stop skipped the environment fence: %s %v", environmentState, err)
			}
			active, err := c.ledger.ActiveCommands(ctx, req.ID.String())
			if err != nil || len(active) != 0 {
				t.Fatalf("stopped task still has active commands: %v %v", active, err)
			}
		})
	}
}

func TestBusinessStopCancelsQueuedMessagesAndWaitsForSubmittedRestore(t *testing.T) {
	c, req, _ := continuationFixture(t)
	blocked := blockedContinuationForStop(t, c, req)
	waiting := acceptedContinuation(t, c, req, "another queued message")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// A restore may still be running when the waiting continuation times out.
	// Ordinary input cancellation excludes it; a full stop must still stop it.
	if err := c.ledger.SetEnvironment(ctx, req.VMID, "project-1", "sandbox-1", "online"); err != nil {
		t.Fatal(err)
	}
	if err := c.ledger.Enqueue(ctx, req.VMID, req.ID.String(), "restore_prepare", 1, req); err != nil {
		t.Fatal(err)
	}
	running, err := c.ledger.Claim(ctx)
	if err != nil || running.Operation != "restore_prepare" {
		t.Fatalf("claim restore: %+v %v", running, err)
	}
	if err := c.ledger.BeginSubmission(ctx, &running); err != nil {
		t.Fatal(err)
	}
	running.RunID = "run-1"
	if err := c.ledger.SaveAdmission(ctx, running); err != nil {
		t.Fatal(err)
	}
	if err := c.ledger.UpdateCommand(ctx, running, "running", running.RunID, 0); err != nil {
		t.Fatal(err)
	}
	server := &runTestServer{status: v2.RunStatus_RUN_STATUS_RUNNING}
	mux := http.NewServeMux()
	path, handler := rpc.NewRunServiceHandler(server)
	mux.Handle(path, handler)
	c.engines["node"] = testEngine(t, mux)
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
		select {
		case err := <-done:
			t.Fatalf("business stop returned before stopping admitted restore: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("stop RPC was not delivered")
		}
		time.Sleep(time.Millisecond)
	}
	assertStoppedContinuation(t, c, blocked)
	assertStoppedContinuation(t, c, waiting)
	select {
	case err := <-done:
		t.Fatalf("business stop returned before remote cancellation was confirmed: %v", err)
	default:
	}
	active, err := c.ledger.ActiveCommands(ctx, req.ID.String())
	if err != nil || len(active) != 1 || active[0].ID != running.ID {
		t.Fatalf("admitted restore disappeared before confirmation: %v %v", active, err)
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
	active, err = c.ledger.ActiveCommands(ctx, req.ID.String())
	if err != nil || len(active) != 0 {
		t.Fatalf("business stop left active commands: %v %v", active, err)
	}
}

func TestCancelWaitingAndBlockedContinuationsKeepsEnvironmentInteractive(t *testing.T) {
	for _, state := range []string{"waiting", "blocked"} {
		t.Run(state, func(t *testing.T) {
			c, req, _ := continuationFixture(t)
			var cmd Command
			if state == "blocked" {
				cmd = blockedContinuationForStop(t, c, req)
			} else {
				cmd = acceptedContinuation(t, c, req, "queued message")
			}
			if err := c.TaskManager().Cancel(context.Background(), taskflow.TaskReq{Task: &taskflow.Task{ID: req.ID}}); err != nil {
				t.Fatal(err)
			}
			assertStoppedContinuation(t, c, cmd)
			var environmentState string
			var taskCanceled bool
			if err := c.ledger.db.QueryRow(`SELECT e.state,i.cancel_requested FROM runtime_task_intents i JOIN runtime_environments e ON e.id=i.environment_id WHERE i.task_id=$1`, req.ID).Scan(&environmentState, &taskCanceled); err != nil || environmentState != "hibernated" || taskCanceled {
				t.Fatalf("input cancellation changed task lifecycle: state=%s canceled=%v err=%v", environmentState, taskCanceled, err)
			}
		})
	}
}
