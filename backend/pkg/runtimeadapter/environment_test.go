package runtimeadapter

import (
	"context"
	"testing"

	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func TestEnvironmentCreationIsAtomicAndRetriesKeepNodeAndSandbox(t *testing.T) {
	l := testLedger(t)
	c := &Client{ledger: l, backend: "agent_compose", nodes: map[string]config.RuntimeNode{"node": {ID: "node"}}}
	ctx := context.Background()
	r := taskflow.CreateVirtualMachineReq{ID: "agent_" + uuid.NewString(), UserID: uuid.NewString(), HostID: "node", Cores: "1", Memory: 2 << 30}
	created, err := c.VirtualMachiner().Create(ctx, &r)
	if err != nil || created.ID != r.ID {
		t.Fatalf("environment admission failed: %v", err)
	}
	if err = l.SetEnvironment(ctx, r.ID, "project-one", "sandbox-one", "online"); err != nil {
		t.Fatal(err)
	}
	c.backend = "taskflow" // Rollback changes the route only for new IDs.
	repeated, err := c.VirtualMachiner().Create(ctx, &r)
	if err != nil || repeated.ID != r.ID || repeated.Status != "online" {
		t.Fatalf("environment retry changed identity/state: %v", err)
	}
	var sandbox string
	if err = l.db.QueryRowContext(ctx, `SELECT sandbox_id FROM runtime_environments WHERE id=$1`, r.ID).Scan(&sandbox); err != nil || sandbox != "sandbox-one" {
		t.Fatal("retry changed the pinned Sandbox")
	}
	var count int
	if err = l.db.QueryRowContext(ctx, `SELECT count(*) FROM runtime_commands WHERE environment_id=$1`, r.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry duplicated environment preparation: %d, %v", count, err)
	}
	conflicting := r
	conflicting.UserID = uuid.NewString()
	if _, err = c.VirtualMachiner().Create(ctx, &conflicting); err == nil {
		t.Fatal("another owner reused the environment admission ID")
	}
	conflicting = r
	conflicting.Memory *= 2
	if _, err = c.VirtualMachiner().Create(ctx, &conflicting); err == nil {
		t.Fatal("environment retry silently changed resource limits")
	}
	c.backend = "agent_compose"
	if _, err = l.db.ExecContext(ctx, `CREATE FUNCTION reject_prepare() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test enqueue failure'; END; $$;
CREATE TRIGGER reject_prepare BEFORE INSERT ON runtime_commands FOR EACH ROW EXECUTE FUNCTION reject_prepare()`); err != nil {
		t.Fatal(err)
	}
	failure := r
	failure.ID = "agent_" + uuid.NewString()
	if _, err = c.VirtualMachiner().Create(ctx, &failure); err == nil {
		t.Fatal("failed preparation was acknowledged")
	}
	if err = l.db.QueryRowContext(ctx, `SELECT count(*) FROM runtime_environments WHERE id=$1`, failure.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("enqueue failure left an orphaned environment: %d, %v", count, err)
	}
	if _, err = l.db.ExecContext(ctx, `DROP TRIGGER reject_prepare ON runtime_commands`); err != nil {
		t.Fatal(err)
	}
	if _, err = l.db.ExecContext(ctx, `UPDATE runtime_environments SET state='deleted' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.VirtualMachiner().Create(ctx, &r); err == nil {
		t.Fatal("retry recreated a recycled environment")
	}
}

func TestRecycledEnvironmentDoesNotBreakHostOnlineList(t *testing.T) {
	l := testLedger(t)
	c := &Client{ledger: l, engines: map[string]*Engine{"node": {}}, backend: "agent_compose"}
	ctx := context.Background()
	for _, id := range []string{"agent-recycled", "agent-pending"} {
		if err := l.SaveEnvironment(ctx, Environment{ID: id, OwnerID: uuid.NewString(), NodeID: "node", Request: taskflow.CreateVirtualMachineReq{}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.SetEnvironment(ctx, "agent-recycled", "project", "sandbox", "deleted"); err != nil {
		t.Fatal(err)
	}
	states, err := c.VirtualMachiner().IsOnline(ctx, &taskflow.IsOnlineReq[string]{IDs: []string{"agent-recycled", "agent-pending"}})
	if err != nil || len(states.OnlineMap) != 2 || states.OnlineMap["agent-recycled"] || states.OnlineMap["agent-pending"] {
		t.Fatalf("mixed online list failed: %v %v", states, err)
	}
	if _, err = c.VirtualMachiner().Info(ctx, taskflow.VirtualMachineInfoReq{ID: "agent-recycled"}); err == nil {
		t.Fatal("direct tombstone access was re-enabled")
	}
}
