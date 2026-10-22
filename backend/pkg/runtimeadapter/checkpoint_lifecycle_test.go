package runtimeadapter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/google/uuid"
)

type checkpointResumeServer struct {
	runTestServer
	fail    bool
	missing bool
	halts   int
}

func (s *checkpointResumeServer) GetSandbox(ctx context.Context, req *connect.Request[v2.GetSandboxRequest]) (*connect.Response[v2.GetSandboxResponse], error) {
	if s.missing {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("sandbox removed"))
	}
	return s.runTestServer.GetSandbox(ctx, req)
}
func (s *checkpointResumeServer) StopSandbox(context.Context, *connect.Request[v2.StopSandboxRequest]) (*connect.Response[v2.StopSandboxResponse], error) {
	s.halts++
	s.sandboxStopped = true
	return connect.NewResponse(&v2.StopSandboxResponse{Sandbox: &v2.Sandbox{SandboxId: "sandbox-1", Status: v2.SandboxStatus_SANDBOX_STATUS_STOPPED}}), nil
}

func (s *checkpointResumeServer) ResumeSandbox(ctx context.Context, req *connect.Request[v2.ResumeSandboxRequest]) (*connect.Response[v2.ResumeSandboxResponse], error) {
	if s.fail {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("node temporarily unavailable"))
	}
	return s.runTestServer.ResumeSandbox(ctx, req)
}

func checkpointCompletionFixture(t *testing.T, target string) (*Client, taskflow.CreateTaskReq, uuid.UUID, *checkpointResumeServer) {
	t.Helper()
	l, c, p := workRuntimeLedger(t)
	l.capacity = config.RuntimeCapacity{Enabled: true, MaxCPUMillis: 1000, MaxMemoryBytes: 1 << 30}
	capacityNode(t, l, "node", strings.Repeat("a", 64))
	req := workRuntimeTask(t, l, p)
	ctx := context.Background()
	if err := l.ensureReservation(ctx, req.VMID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec("UPDATE runtime_commands SET state='complete' WHERE task_id=$1", req.ID); err != nil {
		t.Fatal(err)
	}
	cp := uuid.New()
	if _, err := l.db.Exec("INSERT INTO work_storage_jobs(id,task_id,environment_id,request) VALUES($1,$2,$3,jsonb_build_object('resume_state',$4::text))", cp, req.ID, req.VMID, target); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec("INSERT INTO workspace_checkpoints(id,task_id,environment_id,state,manifest,payload_sha256) VALUES($1,$2,$3,'committed','{\"schema\":\"jingjiaagent.workspace.checkpoint.v3\"}',$4)", cp, req.ID, req.VMID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec("UPDATE runtime_environments SET state='checkpointing',sandbox_id='sandbox-1',archiving_checkpoint_id=$2 WHERE id=$1", req.VMID, cp); err != nil {
		t.Fatal(err)
	}
	server := &checkpointResumeServer{runTestServer: runTestServer{sandboxStopped: true}}
	mux := http.NewServeMux()
	path, handler := rpc.NewSandboxServiceHandler(server)
	mux.Handle(path, handler)
	c.engines["node"] = testEngine(t, mux)
	return c, req, cp, server
}

func TestCheckpointCompletionResumesInsteadOfSleepingAndKeepsCapacity(t *testing.T) {
	c, req, cp, server := checkpointCompletionFixture(t, "online")
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := c.CompleteCheckpoint(ctx, req.ID, cp, req.VMID); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	var clean, noFence bool
	if err := c.ledger.db.QueryRow("SELECT state,checkpoint_id IS NULL,archiving_checkpoint_id IS NULL FROM runtime_environments WHERE id=$1", req.VMID).Scan(&state, &clean, &noFence); err != nil {
		t.Fatal(err)
	}
	if state != "online" || !clean || !noFence || server.resumes != 1 || server.sandboxStopped {
		t.Fatalf("save did not restore the running environment exactly once: state=%s clean=%v fence=%v resumes=%d", state, clean, noFence, server.resumes)
	}
	if count, _, _ := capacityUsage(t, c.ledger); count != 1 {
		t.Fatal("saving released the capacity of a running environment")
	}
	if err := c.ledger.SaveEnvironment(ctx, capacityEnv("competitor", "node")); !errors.Is(err, errcode.ErrRuntimeCapacityExhausted) {
		t.Fatalf("a competitor stole capacity during autosave: %v", err)
	}
	// A running environment may have new background writes. The idle recycler
	// must arrange a final snapshot even while an old converter job is retrying.
	if _, err := c.ledger.db.Exec("UPDATE work_storage_jobs SET state='failed' WHERE id=$1", cp); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ledger.beginRecycle(ctx, req.VMID); err == nil {
		t.Fatal("recycled without a final stable snapshot")
	}
	var final int
	if err := c.ledger.db.QueryRow("SELECT count(*) FROM work_storage_jobs WHERE environment_id=$1 AND id<>$2 AND request->>'resume_state'='hibernated'", req.VMID, cp).Scan(&final); err != nil || final != 1 {
		t.Fatalf("final snapshot was blocked by historical conversion: %d %v", final, err)
	}
	input := &taskflow.Task{ID: req.ID, Text: "message while final policy save is pending", ClientMessageID: uuid.NewString()}
	if err := c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: input}); err != nil {
		t.Fatal(err)
	}
	cmd, err := c.ledger.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.prepareContinuation(ctx, &cmd); err != nil {
		t.Fatal(err)
	}
	if state, phase := continuationState(t, c, cmd.ID); state != "waiting" || phase != "saving" {
		t.Fatalf("message escaped the final archive fence: %s %s", state, phase)
	}
}

func TestCheckpointResumeFailureRetainsFenceAndRetriesCommittedSnapshot(t *testing.T) {
	c, req, cp, server := checkpointCompletionFixture(t, "online")
	ctx := context.Background()
	server.fail = true
	if err := c.CompleteCheckpoint(ctx, req.ID, cp, req.VMID); err == nil {
		t.Fatal("failed resume was reported as successful")
	}
	var state string
	if err := c.ledger.db.QueryRow("SELECT state FROM runtime_environments WHERE id=$1", req.VMID).Scan(&state); err != nil || state != "checkpointing" {
		t.Fatalf("lost retry fence after failed resume: %s %v", state, err)
	}
	if count, _, _ := capacityUsage(t, c.ledger); count != 1 {
		t.Fatal("temporary export stop released capacity")
	}
	server.fail = false
	if err := c.CompleteCheckpoint(ctx, req.ID, cp, req.VMID); err != nil {
		t.Fatal(err)
	}
	var committed int
	if err := c.ledger.db.QueryRow("SELECT count(*) FROM workspace_checkpoints WHERE task_id=$1 AND state='committed'", req.ID).Scan(&committed); err != nil || committed != 1 {
		t.Fatalf("retry changed the immutable snapshot: %d %v", committed, err)
	}
}

func TestCheckpointPreservesPolicySleepAndRecyclesAfterFinalSave(t *testing.T) {
	c, req, cp, server := checkpointCompletionFixture(t, "hibernated")
	ctx := context.Background()
	if err := c.CompleteCheckpoint(ctx, req.ID, cp, req.VMID); err != nil {
		t.Fatal(err)
	}
	var valid bool
	if err := c.ledger.db.QueryRow("SELECT state='hibernated' AND checkpoint_id=$2 AND archiving_checkpoint_id IS NULL FROM runtime_environments WHERE id=$1", req.VMID, cp).Scan(&valid); err != nil || !valid || server.resumes != 0 {
		t.Fatalf("saving woke an environment already stopped by policy: %v %v", valid, err)
	}
	if _, err := c.ledger.beginRecycle(ctx, req.VMID); err != nil {
		t.Fatalf("final verified snapshot did not permit recycling: %v", err)
	}
}

func TestCheckpointCannotResumeUncommittedOrDeletingTask(t *testing.T) {
	c, req, cp, server := checkpointCompletionFixture(t, "online")
	ctx := context.Background()
	if _, err := c.ledger.db.Exec("UPDATE workspace_checkpoints SET state='pending' WHERE id=$1", cp); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteCheckpoint(ctx, req.ID, cp, req.VMID); err == nil || server.resumes != 0 {
		t.Fatal("uncommitted files allowed execution to resume")
	}
	if _, err := c.ledger.db.Exec("UPDATE workspace_checkpoints SET state='committed' WHERE id=$1", cp); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ledger.db.Exec("UPDATE runtime_task_intents SET deletion_requested=true WHERE task_id=$1", req.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteCheckpoint(ctx, req.ID, cp, req.VMID); err != nil || server.resumes != 0 {
		t.Fatalf("saving resurrected a deleting task: %v", err)
	}
}

func TestCheckpointFinishingAllowsTheQueuedNextRound(t *testing.T) {
	c, req, cp, _ := checkpointCompletionFixture(t, "online")
	ctx := context.Background()
	if _, err := c.ledger.db.Exec("UPDATE tasks SET status='processing',last_active_at=now()-interval '2 hours' WHERE id=$1", req.ID); err != nil {
		t.Fatal(err)
	}
	input := &taskflow.Task{ID: req.ID, Text: "continue the report", ClientMessageID: uuid.NewString()}
	if err := c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: input}); err != nil {
		t.Fatal(err)
	}
	cmd, err := c.ledger.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.prepareContinuation(ctx, &cmd); err != nil {
		t.Fatal(err)
	}
	if state, phase := continuationState(t, c, cmd.ID); state != "waiting" || phase != "saving" {
		t.Fatalf("next round entered an archive still being saved: %s %s", state, phase)
	}
	if err := c.CompleteCheckpoint(ctx, req.ID, cp, req.VMID); err != nil {
		t.Fatal(err)
	}
	ready(t, c)
	cmd, err = c.ledger.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.prepareContinuation(ctx, &cmd); err != nil {
		t.Fatal(err)
	}
	if state, _ := continuationState(t, c, cmd.ID); state != "pending" {
		t.Fatalf("queued round did not become runnable: %s", state)
	}
	ready(t, c)
	cmd, err = c.ledger.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ledger.db.Exec("UPDATE tasks SET last_active_at=now()-interval '2 hours' WHERE id=$1", req.ID); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Second)
	if err := c.ledger.Finish(ctx, cmd, "complete", ""); err != nil {
		t.Fatal(err)
	}
	var last time.Time
	if err := c.ledger.db.QueryRow("SELECT last_active_at FROM tasks WHERE id=$1", req.ID).Scan(&last); err != nil || last.Before(before) {
		t.Fatalf("idle clock still measures from the start of a long round: %v %v", last, err)
	}
	var onlineTarget bool
	if err := c.ledger.db.QueryRow("SELECT request->>'resume_state'='online' FROM work_storage_jobs WHERE command_id=$1", cmd.ID).Scan(&onlineTarget); err != nil || !onlineTarget {
		t.Fatalf("completed round did not request an online autosave: %v %v", onlineTarget, err)
	}
}

func TestCheckpointExportKeepsRunningContainerAndNativeSession(t *testing.T) {
	c, req, cp, _ := checkpointCompletionFixture(t, "online")
	server := &runTestServer{}
	mux := http.NewServeMux()
	path, handler := rpc.NewSandboxServiceHandler(server)
	mux.Handle(path, handler)
	mux.HandleFunc("/jingjiaagent/workspace/export", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("layout") != "managed-v1" || r.URL.Query().Get("mode") != "running" || r.URL.Query().Get("sandbox_id") != "sandbox-1" || r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "incorrect live snapshot request", 400)
			return
		}
		_, _ = w.Write([]byte("stable live snapshot"))
	})
	c.engines["node"] = testEngine(t, mux)
	stream, err := c.ExportCheckpoint(context.Background(), req.ID, cp, req.VMID)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil || string(data) != "stable live snapshot" {
		t.Fatalf("export changed: %s %v", data, err)
	}
	if err := c.CompleteCheckpoint(context.Background(), req.ID, cp, req.VMID); err != nil {
		t.Fatal(err)
	}
	if server.resumes != 0 || server.sandboxStopped {
		t.Fatal("ordinary autosave stopped/restarted an already running session")
	}
}

func TestDirectArtifactFinalizationRequiresDurableFilesWithoutCheckpoint(t *testing.T) {
	c, req, id, _ := checkpointCompletionFixture(t, "online")
	ctx := context.Background()
	if _, err := c.ledger.db.Exec(`DELETE FROM workspace_checkpoints WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ledger.db.Exec(`UPDATE work_storage_jobs SET kind='artifacts',request=request || '{"scope":"artifacts"}' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteCheckpoint(ctx, req.ID, id, req.VMID); err == nil {
		t.Fatal("unverified artifact files released the fence")
	}
	if _, err := c.ledger.db.Exec(`UPDATE work_storage_jobs SET request=request || '{"artifacts_saved":true}' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteCheckpoint(ctx, req.ID, id, req.VMID); err != nil {
		t.Fatal(err)
	}
	var ready bool
	if err := c.ledger.db.QueryRow(`SELECT state='online' AND checkpoint_id IS NULL AND archiving_checkpoint_id IS NULL FROM runtime_environments WHERE id=$1`, req.VMID).Scan(&ready); err != nil || !ready {
		t.Fatal("direct artifact save left a snapshot or fence", ready, err)
	}
}

func TestCheckpointFinalizationDoesNotReviveRevokedLiveEnvironment(t *testing.T) {
	c, req, cp, server := checkpointCompletionFixture(t, "online")
	server.sandboxStopped = false
	guard := &stagingRevocationGuard{}
	guard.revoked.Store(true)
	c.workGuard = guard
	if err := c.CompleteCheckpoint(context.Background(), req.ID, cp, req.VMID); err != nil {
		t.Fatal(err)
	}
	if !server.sandboxStopped || server.halts != 1 || server.resumes != 0 {
		t.Fatal("revoked live process was left running")
	}
	if count, _, _ := capacityUsage(t, c.ledger); count != 0 {
		t.Fatal("verified stopped checkpoint retained revoked execution capacity")
	}
}

func TestCheckpointFinalizationOfMissingSandboxAllowsRecovery(t *testing.T) {
	c, req, cp, server := checkpointCompletionFixture(t, "online")
	server.missing = true
	if err := c.CompleteCheckpoint(context.Background(), req.ID, cp, req.VMID); err != nil {
		t.Fatal(err)
	}
	var recoverable bool
	if err := c.ledger.db.QueryRow("SELECT state='offline' AND checkpoint_id=$2 AND archiving_checkpoint_id IS NULL FROM runtime_environments WHERE id=$1", req.VMID, cp).Scan(&recoverable); err != nil || !recoverable {
		t.Fatalf("durable files were stranded behind a missing sandbox: %v %v", recoverable, err)
	}
}
