package runtimeadapter

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/runtimenode"
)

// Real SQL/placement with mocked node HTTP: a queued request must discover a
// node enrolled after its creation, and a drain must not reset its connection.
func TestDynamicPlacementSeesNewNodesAndPreservesPinnedWorkspace(t *testing.T) {
	l, c, profile := workRuntimeLedger(t)
	ctx := context.Background()
	migration, e := os.ReadFile("../../migration/000049_dynamic_runtime_nodes.up.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = l.db.Exec(string(migration)); e != nil {
		t.Fatal(e)
	}
	req := workRuntimeTask(t, l, profile)
	env, e := l.Environment(ctx, req.VMID)
	if e != nil {
		t.Fatal(e)
	}
	l.capacity = config.RuntimeCapacity{Enabled: true, MaxCPUMillis: 2000, MaxMemoryBytes: 2 << 30}
	capacityNode(t, l, "node", strings.Repeat("a", 64))
	other := capacityEnv("other", "node")
	other.Request.Cores = "2"
	other.Request.Memory = 2 << 30
	if e = l.SaveEnvironment(ctx, other); e != nil {
		t.Fatal(e)
	}
	image := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"compatible":true}`))
	}))
	defer image.Close()
	c.nodes = map[string]config.RuntimeNode{}
	c.engines = map[string]*Engine{}
	c.nodeStore, e = runtimenode.New(l.db, bytes.Repeat([]byte{7}, 32))
	if e != nil {
		t.Fatal(e)
	}
	l.dynamicNodes = true
	record := runtimenode.Record{Config: config.RuntimeNode{ID: "node", URL: image.URL, OwnerID: env.OwnerID, GuestImage: "fixture:1"}, Material: runtimenode.Material{Token: "test-node-token"}, Imported: true, Accepting: true, Phase: "ready"}
	if e = c.nodeStore.Create(ctx, record); e != nil {
		t.Fatal(e)
	}
	env.Request.AutoHostIDs = []string{"node"}
	env.Request.ImageURL = "fixture:1"
	sealed, e := l.seal(env.ID, mustJSON(env.Request))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = l.db.Exec(`UPDATE runtime_environments SET payload=$2 WHERE id=$1`, env.ID, sealed); e != nil {
		t.Fatal(e)
	}
	cmd, e := l.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.acquireWorkSlot(ctx, cmd); e != nil {
		t.Fatal(e)
	}
	if e = c.scheduleEnvironment(ctx, cmd, env); !errors.Is(e, errWorkQueued) {
		t.Fatalf("expected capacity wait: %v", e)
	}
	// The stored request still only names node A; B joins without any config edit.
	record.Config.ID = "node-b"
	secondImage := httptest.NewServer(image.Config.Handler)
	defer secondImage.Close()
	record.Config.URL = secondImage.URL
	if e = c.nodeStore.Create(ctx, record); e != nil {
		t.Fatal(e)
	}
	capacityNode(t, l, "node-b", strings.Repeat("b", 64))
	if _, e = l.db.Exec(`UPDATE runtime_commands SET available_at=now() WHERE id=$1`, cmd.ID); e != nil {
		t.Fatal(e)
	}
	cmd, e = l.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.acquireWorkSlot(ctx, cmd); e != nil {
		t.Fatal(e)
	}
	if e = c.scheduleEnvironment(ctx, cmd, env); e != nil {
		t.Fatal(e)
	}
	pinned, e := l.Environment(ctx, env.ID)
	if e != nil || pinned.NodeID != "node-b" || len(pinned.Request.AutoHostIDs) != 0 {
		t.Fatalf("new node not pinned: %s %v", pinned.NodeID, e)
	}
	engine := c.engine("node-b")
	c.verifyNode("node-b", true)
	if e = c.nodeStore.SetAccepting(ctx, "node-b", false); e != nil {
		t.Fatal(e)
	}
	if e = c.RefreshNodes(ctx); e != nil {
		t.Fatal(e)
	}
	if engine != c.engine("node-b") || !c.nodeIsVerified("node-b") {
		t.Fatal("drain reset an active connection")
	}
	if accepting, e := c.acceptingNode(ctx, "node-b"); e != nil || accepting {
		t.Fatal("drained node still accepts new work", e)
	}
	// Once remotely started, continuation must use B even after it is drained.
	if _, e = l.db.Exec(`UPDATE runtime_environments SET project_id='existing-project',sandbox_id='existing-sandbox' WHERE id=$1`, env.ID); e != nil {
		t.Fatal(e)
	}
	pinned, e = l.Environment(ctx, env.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.scheduleEnvironment(ctx, cmd, pinned); e != nil {
		t.Fatalf("drain blocked existing workspace: %v", e)
	}
	// A fresh backend process reconstructs the registry exclusively from SQL.
	fresh := &Client{ledger: l, nodeStore: c.nodeStore, nodes: map[string]config.RuntimeNode{}, engines: map[string]*Engine{}}
	ids, e := fresh.RuntimeNodeIDs(ctx)
	if e != nil || len(ids) != 2 {
		t.Fatalf("restart lost dynamic nodes: %v %v", ids, e)
	}
}
