package runtimeadapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

func TestSchedulingPersonalAndTeamLimitsApplyToLegacyTasksAndLivePolicy(t *testing.T) {
	l, c, profile := workRuntimeLedger(t)
	ctx := context.Background()
	first := workRuntimeTask(t, l, profile)
	second := workRuntimeTask(t, l, profile)
	if _, err := l.db.Exec(`UPDATE tasks SET user_id=(SELECT user_id FROM tasks WHERE id=$1) WHERE id=$2`, first.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`UPDATE teams SET task_concurrency_limit=1,task_total_concurrency_limit=9`); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`DELETE FROM work_tasks WHERE task_id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	a, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.acquireWorkSlot(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err = c.acquireWorkSlot(ctx, b); !errors.Is(err, errWorkQueued) {
		t.Fatalf("same employee bypassed limit: %v", err)
	}
	var reason string
	if err = l.db.QueryRow(`SELECT queue_reason FROM runtime_commands WHERE id=$1`, b.ID).Scan(&reason); err != nil || !strings.Contains(reason, "个人") {
		t.Fatalf("queue reason %s %v", reason, err)
	}
	// Ordinary waiting is not an execution failure, even after five minutes.
	if _, err = l.db.Exec(`UPDATE runtime_commands SET available_at=now(),preparation_started_at=now()-interval '20 minutes' WHERE id=$1`, b.ID); err != nil {
		t.Fatal(err)
	}
	if err = c.Step(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	var slots int
	l.db.QueryRow(`SELECT state FROM runtime_commands WHERE id=$1`, b.ID).Scan(&state)
	l.db.QueryRow(`SELECT count(*) FROM work_run_slots`).Scan(&slots)
	if state != "pending" || slots != 1 {
		t.Fatalf("queued request failed or held slot: %s %d", state, slots)
	}
	if _, err = l.db.Exec(`UPDATE runtime_commands SET available_at=now() WHERE id=$1`, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = l.db.Exec(`UPDATE teams SET task_concurrency_limit=2`); err != nil {
		t.Fatal(err)
	}
	b, err = l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.acquireWorkSlot(ctx, b); err != nil {
		t.Fatalf("live policy was not applied to legacy request: %v", err)
	}
	if _, err = l.db.Exec(`UPDATE teams SET task_total_concurrency_limit=1`); err != nil {
		t.Fatal(err)
	}
	// Lowering a limit must not invalidate an already held slot.
	if err = c.acquireWorkSlot(ctx, a); err != nil {
		t.Fatalf("active task interrupted by lower limit: %v", err)
	}
	third := workRuntimeTask(t, l, profile)
	_ = third
	d, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.acquireWorkSlot(ctx, d); !errors.Is(err, errWorkQueued) {
		t.Fatalf("new admission bypassed lowered limit: %v", err)
	}
}

func TestAutomaticPlacementUsesCompatibleCapacityAndPinsHost(t *testing.T) {
	l, c, profile := workRuntimeLedger(t)
	ctx := context.Background()
	l.capacity = config.RuntimeCapacity{Enabled: true, MaxCPUMillis: 2000, MaxMemoryBytes: 2 << 30}
	capacityNode(t, l, "node", strings.Repeat("a", 64))
	capacityNode(t, l, "node-b", strings.Repeat("b", 64))
	imageReady := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/jingjiaagent/image" || r.URL.Query().Get("name") != "fixture:1" {
			t.Error("incorrect image readiness request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"compatible":true}`))
	}))
	defer imageReady.Close()
	c.engines = map[string]*Engine{"node": {nodeURL: imageReady.URL, nodeHTTP: imageReady.Client()}, "node-b": {nodeURL: imageReady.URL, nodeHTTP: imageReady.Client()}}
	req := workRuntimeTask(t, l, profile)
	e, err := l.Environment(ctx, req.VMID)
	if err != nil {
		t.Fatal(err)
	}
	e.Request.AutoHostIDs = []string{"node", "node-b"}
	e.Request.ImageURL = "fixture:1"
	sealed, err := l.seal(e.ID, mustJSON(e.Request))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.db.Exec(`UPDATE runtime_environments SET payload=$2 WHERE id=$1`, e.ID, sealed); err != nil {
		t.Fatal(err)
	}
	// Fill the placeholder node. Enrolling/refreshing a node must not reserve a
	// queued task's placeholder resources, otherwise automatic selection deadlocks.
	other := capacityEnv("other", "node")
	other.Request.Cores = "2"
	other.Request.Memory = 2 << 30
	if err = l.SaveEnvironment(ctx, other); err != nil {
		t.Fatal(err)
	}
	cmd, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.acquireWorkSlot(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if err = c.scheduleEnvironment(ctx, cmd, e); err != nil {
		t.Fatal(err)
	}
	pinned, err := l.Environment(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.NodeID != "node-b" || pinned.Request.HostID != "node-b" || len(pinned.Request.AutoHostIDs) != 0 {
		t.Fatalf("not durably pinned: %+v", pinned.Request)
	}
	var vmhost string
	l.db.QueryRow(`SELECT host_id FROM virtualmachines WHERE id=$1`, e.ID).Scan(&vmhost)
	if vmhost != "node-b" {
		t.Fatal("product VM host differs from runtime host")
	}
	// A retry neither takes a second reservation nor moves to a different host.
	if err = c.scheduleEnvironment(ctx, cmd, pinned); err != nil {
		t.Fatal(err)
	}
	count, _, _ := capacityUsage(t, l)
	if count != 2 {
		t.Fatalf("duplicate resource reservation: %d", count)
	}
	// An incompatible image is rejected before the first remote mutation.
	incompatible := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"compatible":false}`)) }))
	defer incompatible.Close()
	c.engines["node"] = &Engine{nodeURL: incompatible.URL, nodeHTTP: incompatible.Client()}
	next := workRuntimeTask(t, l, profile)
	ne, _ := l.Environment(ctx, next.VMID)
	ne.Request.AutoHostIDs = []string{"node"}
	ne.Request.ImageURL = "wrong:1"
	nextCmd, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var rejected *schedulingError
	if err = c.scheduleEnvironment(ctx, nextCmd, ne); !errors.As(err, &rejected) {
		t.Fatalf("incompatible image not rejected: %v", err)
	}
}

func TestCapacityWaitReleasesExecutionSlot(t *testing.T) {
	l, c, profile := workRuntimeLedger(t)
	ctx := context.Background()
	l.capacity = config.RuntimeCapacity{Enabled: true, MaxCPUMillis: 1000, MaxMemoryBytes: 1 << 30}
	capacityNode(t, l, "node", strings.Repeat("e", 64))
	if err := l.SaveEnvironment(ctx, capacityEnv("full", "node")); err != nil {
		t.Fatal(err)
	}
	req := workRuntimeTask(t, l, profile)
	e, _ := l.Environment(ctx, req.VMID)
	cmd, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.acquireWorkSlot(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if err = c.scheduleEnvironment(ctx, cmd, e); !errors.Is(err, errWorkQueued) {
		t.Fatalf("full node did not queue: %v", err)
	}
	var count int
	l.db.QueryRow(`SELECT count(*) FROM work_run_slots`).Scan(&count)
	if count != 0 {
		t.Fatal("capacity waiter retained execution slot")
	}
	count, _, _ = capacityUsage(t, l)
	if count != 1 {
		t.Fatal("queued task reserved host resources")
	}
	// An individually oversized task is a configuration error, not an endless wait.
	e.Request = taskflow.CreateVirtualMachineReq{Cores: "3", Memory: 1 << 30}
	var oversized *schedulingError
	if err = c.scheduleEnvironment(ctx, cmd, e); !errors.As(err, &oversized) {
		t.Fatalf("oversized task should fail clearly: %v", err)
	}
}

func TestAutomaticPlacementWaitsForOfflineCompatibleHost(t *testing.T) {
	l, c, profile := workRuntimeLedger(t)
	ctx := context.Background()
	l.capacity = config.RuntimeCapacity{Enabled: true, MaxCPUMillis: 2000, MaxMemoryBytes: 2 << 30}
	capacityNode(t, l, "node", strings.Repeat("a", 64))
	capacityNode(t, l, "node-b", strings.Repeat("b", 64))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/unavailable/") {
			_, _ = w.Write([]byte(`{"compatible":false}`))
		} else {
			_, _ = w.Write([]byte(`{"compatible":true}`))
		}
	}))
	defer server.Close()
	c.engines = map[string]*Engine{"node": {nodeURL: server.URL + "/unavailable", nodeHTTP: server.Client()}, "node-b": {nodeURL: server.URL, nodeHTTP: server.Client()}}
	req := workRuntimeTask(t, l, profile)
	e, _ := l.Environment(ctx, req.VMID)
	e.Request.AutoHostIDs = []string{"node", "node-b"}
	e.Request.ImageURL = "fixture:1"
	c.registry = &nodeRegistryFixture{actor: e.OwnerID}
	c.nodeVerified = map[string]time.Time{"node": time.Now()}
	cmd, err := l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.scheduleEnvironment(ctx, cmd, e); !errors.Is(err, errWorkQueued) {
		t.Fatalf("offline eligible host must wait, not fail: %v", err)
	}
	c.nodeVerified["node-b"] = time.Now()
	if _, err = l.db.Exec(`UPDATE runtime_commands SET available_at=now() WHERE id=$1`, cmd.ID); err != nil {
		t.Fatal(err)
	}
	cmd, err = l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.scheduleEnvironment(ctx, cmd, e); err != nil {
		t.Fatalf("recovered node was not scheduled: %v", err)
	}
	saved, _ := l.Environment(ctx, e.ID)
	if saved.NodeID != "node-b" {
		t.Fatal("placed on an incompatible node")
	}
}
