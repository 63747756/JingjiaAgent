package runtimeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/google/uuid"
)

func continuationFixture(t *testing.T) (*Client, taskflow.CreateTaskReq, uuid.UUID) {
	t.Helper()
	l, c, p := workRuntimeLedger(t)
	req := workRuntimeTask(t, l, p)
	if err := l.Enqueue(context.Background(), req.VMID, req.ID.String(), "task", 1, req); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`UPDATE runtime_commands SET state='complete' WHERE task_id=$1;`, req.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`UPDATE runtime_environments SET state='hibernated',sandbox_id='sandbox-1' WHERE id=$1`, req.VMID); err != nil {
		t.Fatal(err)
	}
	var owner uuid.UUID
	if err := l.db.QueryRow(`SELECT owner_id FROM runtime_environments WHERE id=$1`, req.VMID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	path, handler := rpc.NewSandboxServiceHandler(&runTestServer{})
	mux.Handle(path, handler)
	c.engines["node"] = testEngine(t, mux)
	return c, req, owner
}
func acceptedContinuation(t *testing.T, c *Client, task taskflow.CreateTaskReq, text string) Command {
	t.Helper()
	if err := c.TaskManager().Continue(context.Background(), taskflow.TaskReq{Task: &taskflow.Task{ID: task.ID, Text: text, ClientMessageID: uuid.NewString()}}); err != nil {
		t.Fatal(err)
	}
	cmd, err := c.ledger.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}
func continuationState(t *testing.T, c *Client, id string) (string, string) {
	t.Helper()
	var state, phase string
	if err := c.ledger.db.QueryRow(`SELECT state,preparation_phase FROM runtime_commands WHERE id=$1`, id).Scan(&state, &phase); err != nil {
		t.Fatal(err)
	}
	return state, phase
}
func TestContinuationAcceptsDuringSaveFreezesSkillsAndDeduplicates(t *testing.T) {
	c, req, owner := continuationFixture(t)
	ctx := context.Background()
	if _, err := c.ledger.db.Exec(`UPDATE runtime_environments SET state='checkpointing' WHERE id=$1`, req.VMID); err != nil {
		t.Fatal(err)
	}
	resolver := &roundResolverFixture{pins: []taskflow.RoundSkillPin{{SkillID: uuid.New(), Name: "test", Version: "1"}}}
	c.skillResolver = resolver
	input := &taskflow.Task{ID: req.ID, Text: "new message", ClientMessageID: uuid.NewString()}
	for i := 0; i < 2; i++ {
		if err := c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: input}); err != nil {
			t.Fatal(err)
		}
	}
	if resolver.calls != 1 || resolver.owner != owner {
		t.Fatal("accepted input re-resolved or used wrong owner")
	}
	var count int
	if err := c.ledger.db.QueryRow(`SELECT count(*) FROM runtime_commands WHERE task_id=$1 AND turn=2`, req.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate accepted command", err)
	}
	input.Text = "different"
	if err := c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: input}); err == nil {
		t.Fatal("conflicting retry accepted")
	}
	cmd, err := c.ledger.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.prepareContinuation(ctx, &cmd); err != nil {
		t.Fatal(err)
	}
	if state, phase := continuationState(t, c, cmd.ID); state != "waiting" || phase != "saving" {
		t.Fatal(state, phase)
	}
	if _, err = c.ledger.db.Exec(`UPDATE runtime_environments SET state='hibernated' WHERE id=$1`, req.VMID); err != nil {
		t.Fatal(err)
	}
	ready(t, c)
	cmd, err = c.ledger.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.prepareContinuation(ctx, &cmd); err != nil {
		t.Fatal(err)
	}
	if state, _ := continuationState(t, c, cmd.ID); state != "pending" {
		t.Fatal(state)
	}
	var sealed []byte
	if err = c.ledger.db.QueryRow(`SELECT payload FROM runtime_commands WHERE id=$1`, cmd.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	b, err := c.ledger.open(cmd.ID, sealed)
	if err != nil {
		t.Fatal(err)
	}
	var task taskflow.CreateTaskReq
	if err = json.Unmarshal(b, &task); err != nil {
		t.Fatal(err)
	}
	if len(task.RoundSkills.Pins) != 1 || task.RecoveryContext == "" || task.InputText() != "new message" {
		t.Fatal("round lost immutable pins, history context or original text")
	}
}

func TestDeletionFenceRejectsNewContinuation(t *testing.T) {
	c, req, _ := continuationFixture(t)
	if _, err := c.ledger.db.Exec(`UPDATE runtime_task_intents SET deletion_requested=true WHERE task_id=$1`, req.ID); err != nil {
		t.Fatal(err)
	}
	err := c.acceptContinuation(context.Background(), &taskflow.Task{ID: req.ID, Text: "during deletion", ClientMessageID: uuid.NewString()})
	if err == nil || !strings.Contains(err.Error(), "正在删除") {
		t.Fatalf("new message escaped deletion fence: %v", err)
	}
	var count int
	if err := c.ledger.db.QueryRow(`SELECT count(*) FROM runtime_commands WHERE task_id=$1 AND operation='task'`, req.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("new command admitted: %d %v", count, err)
	}
}
func TestContinuationLostEnvironmentRequiresAuthorizedRecoveryChoice(t *testing.T) {
	c, req, owner := continuationFixture(t)
	ctx := context.Background()
	if _, err := c.ledger.db.Exec(`UPDATE runtime_environments SET state='deleted' WHERE id=$1`, req.VMID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	// Test recovery error/choice without a remote runtime or unsafe replay.
	c.SetContinuationRestore(func(_ context.Context, u, task, _ uuid.UUID) (string, error) {
		calls++
		if u != owner || task != req.ID {
			t.Fatal("wrong restoration owner")
		}
		return "", &RecoveryChoiceError{CheckpointID: uuid.New(), Message: "newer progress is missing"}
	})
	cmd := acceptedContinuation(t, c, req, "continue once")
	if err := c.prepareContinuation(ctx, &cmd); err != nil {
		t.Fatal(err)
	}
	if state, _ := continuationState(t, c, cmd.ID); state != "blocked" || calls != 1 {
		t.Fatal(state, calls)
	}
	states, err := c.Continuations(ctx, owner, req.ID)
	if err != nil || len(states) != 1 || states[0].CheckpointID == nil {
		t.Fatal(states, err)
	}
	if err = c.RetryContinuation(ctx, uuid.New(), req.ID, uuid.MustParse(cmd.ID), nil, false); err == nil {
		t.Fatal("another owner retried")
	}
	wrong := uuid.New()
	if err = c.RetryContinuation(ctx, owner, req.ID, uuid.MustParse(cmd.ID), &wrong, false); err == nil {
		t.Fatal("unoffered checkpoint accepted")
	}
	if err = c.RetryContinuation(ctx, owner, req.ID, uuid.MustParse(cmd.ID), states[0].CheckpointID, false); err != nil {
		t.Fatal(err)
	}
	if state, _ := continuationState(t, c, cmd.ID); state != "waiting" {
		t.Fatal(state)
	}
}

func TestContinuationStreamReplaysReceiptAndIgnoresOtherRoundEnd(t *testing.T) {
	c, req, owner := continuationFixture(t)
	first := acceptedContinuation(t, c, req, "previous queued input")
	messageID := uuid.NewString()
	if err := c.TaskManager().Continue(context.Background(), taskflow.TaskReq{Task: &taskflow.Task{ID: req.ID, Text: "next queued input", ClientMessageID: messageID}}); err != nil {
		t.Fatal(err)
	}
	if err := c.RetryContinuation(context.Background(), owner, req.ID, uuid.MustParse(first.ID), nil, true); err != nil {
		t.Fatal(err)
	}
	var second string
	if err := c.ledger.db.QueryRow(`SELECT id FROM runtime_commands WHERE task_id=$1 AND turn=3`, req.ID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if err := c.RetryContinuation(context.Background(), owner, req.ID, uuid.MustParse(second), nil, true); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		events := []string{}
		err := c.TaskInputLive(ctx, req.ID.String(), messageID, func(chunk *taskflow.TaskChunk) error {
			events = append(events, chunk.Event)
			if chunk.Event == "user-input" && !strings.Contains(string(chunk.Data), messageID) {
				t.Fatal("another input leaked into stream")
			}
			return nil
		})
		cancel()
		if err != nil || strings.Join(events, ",") != "user-input,task-ended" {
			t.Fatalf("lost or mixed receipts on attempt %d: %v %v", attempt, events, err)
		}
	}
}

type unreachableSandbox struct {
	rpc.UnimplementedSandboxServiceHandler
}

func (unreachableSandbox) GetSandbox(context.Context, *connect.Request[v2.GetSandboxRequest]) (*connect.Response[v2.GetSandboxResponse], error) {
	return nil, connect.NewError(connect.CodeUnavailable, errors.New("network unavailable"))
}
func TestContinuationTransportFailureCannotRebuildAndEventuallyBlocks(t *testing.T) {
	c, req, _ := continuationFixture(t)
	mux := http.NewServeMux()
	p, h := rpc.NewSandboxServiceHandler(unreachableSandbox{})
	mux.Handle(p, h)
	c.engines["node"] = testEngine(t, mux)
	c.SetContinuationRestore(func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (string, error) {
		t.Fatal("timeout must not rebuild a potentially live environment")
		return "", nil
	})
	cmd := acceptedContinuation(t, c, req, "do not duplicate")
	if _, err := c.ledger.db.Exec(`UPDATE runtime_commands SET preparation_started_at=now()-interval '6 minutes' WHERE id=$1`, cmd.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.prepareContinuation(context.Background(), &cmd); err != nil {
		t.Fatal(err)
	}
	if state, _ := continuationState(t, c, cmd.ID); state != "blocked" {
		t.Fatal(state)
	}
}
func TestContinuationCancelPersistsTerminalReceiptAndDoesNotSubmit(t *testing.T) {
	c, req, owner := continuationFixture(t)
	cmd := acceptedContinuation(t, c, req, "cancel this")
	if err := c.RetryContinuation(context.Background(), owner, req.ID, uuid.MustParse(cmd.ID), nil, true); err != nil {
		t.Fatal(err)
	}
	var terminal bool
	if err := c.ledger.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM runtime_events WHERE command_id=$1 AND source_key='task-ended')`, cmd.ID).Scan(&terminal); err != nil || !terminal {
		t.Fatal("cancel did not close stream", err)
	}
	if err := c.prepareContinuation(context.Background(), &cmd); !errors.Is(err, errLeaseLost) {
		t.Fatal("stale worker retained canceled lease", err)
	}
}
func TestRecoveryContextContainsOriginalAndRecentRequestsWithoutConfigSecrets(t *testing.T) {
	c, req, _ := continuationFixture(t)
	cmd := acceptedContinuation(t, c, req, "new instruction")
	var task taskflow.CreateTaskReq
	if err := json.Unmarshal(cmd.Payload, &task); err != nil {
		t.Fatal(err)
	}
	task.LLM.ApiKey = "DO-NOT-EXPOSE"
	task.Recovered = true
	if err := c.freezeRecoveryContext(context.Background(), &cmd, &task); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(task.RecoveryContext, req.Text) || strings.Contains(task.RecoveryContext, task.LLM.ApiKey) || strings.Contains(task.RecoveryContext, "new instruction") {
		t.Fatal("recovery context boundary incorrect")
	}
	if strings.Contains(task.RecoveryContext, "/workspace/input") {
		t.Fatal("recovery invented an attachment directory without file evidence")
	}
}
