package runtimeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/errcode"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func capacityNode(t *testing.T, l *Ledger, node, pool string) {
	t.Helper()
	s := validNodeSnapshot()
	s.InstanceID = uuid.NewString()
	s.Fingerprint = strings.Repeat("a", 64)
	s.CapacityID = pool
	if err := l.bindNode(context.Background(), node, s); err != nil {
		t.Fatal(err)
	}
	if err := l.saveNode(context.Background(), node, s); err != nil {
		t.Fatal(err)
	}
}
func capacityEnv(id, node string) Environment {
	return Environment{ID: id, NodeID: node, OwnerID: uuid.NewString(), Request: taskflow.CreateVirtualMachineReq{Cores: "1", Memory: 1 << 30}}
}
func capacityUsage(t *testing.T, l *Ledger) (int, int64, int64) {
	t.Helper()
	var count int
	var cpu, memory int64
	if err := l.db.QueryRow(`SELECT count(*),COALESCE(SUM(cpu_millis),0),COALESCE(SUM(memory_bytes),0) FROM runtime_reservations WHERE active`).Scan(&count, &cpu, &memory); err != nil {
		t.Fatal(err)
	}
	return count, cpu, memory
}
func TestCapacityResourcesMatchDockerLimits(t *testing.T) {
	for value, want := range map[string]int64{"": 2000, "1": 1000, "1.25": 1250, "0.001": 1, "2.000": 2000} {
		cpu, memory, err := requestedResources(taskflow.CreateVirtualMachineReq{Cores: value})
		if err != nil || cpu != want || memory != 8<<30 {
			t.Fatalf("resources %q: %d/%d %v", value, cpu, memory, err)
		}
	}
	for _, value := range []string{"0", "-1", "NaN", "Inf", "1e2", "1.0001", " 1", "65537", "0.000"} {
		if _, _, err := requestedResources(taskflow.CreateVirtualMachineReq{Cores: value}); !errors.Is(err, errcode.ErrRuntimeResources) {
			t.Fatalf("invalid CPU accepted %q", value)
		}
	}
	if _, _, err := requestedResources(taskflow.CreateVirtualMachineReq{Memory: 1 << 63}); !errors.Is(err, errcode.ErrRuntimeResources) {
		t.Fatal("memory overflow accepted")
	}
}
func TestCapacityConcurrentAliasesAndRestartCannotOvercommit(t *testing.T) {
	l := testLedger(t)
	l.capacity = config.RuntimeCapacity{Enabled: true, MaxCPUMillis: 2000, MaxMemoryBytes: 2 << 30}
	pool := strings.Repeat("b", 64)
	capacityNode(t, l, "node-a", pool)
	capacityNode(t, l, "node-b", pool)
	var group sync.WaitGroup
	results := make(chan error, 24)
	for i := 0; i < 24; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			results <- l.saveEnvironment(context.Background(), capacityEnv(fmt.Sprintf("candidate-%d", i), []string{"node-a", "node-b"}[i%2]), true)
		}(i)
	}
	group.Wait()
	close(results)
	admitted := 0
	for err := range results {
		if err == nil {
			admitted++
		} else if !errors.Is(err, errcode.ErrRuntimeCapacityExhausted) {
			t.Fatal(err)
		}
	}
	count, cpu, memory := capacityUsage(t, l)
	if admitted != 2 || count != 2 || cpu != 2000 || memory != 2<<30 {
		t.Fatalf("overcommit: %d %d %d %d", admitted, count, cpu, memory)
	}
	var envs, commands int
	l.db.QueryRow(`SELECT count(*) FROM runtime_environments`).Scan(&envs)
	l.db.QueryRow(`SELECT count(*) FROM runtime_commands`).Scan(&commands)
	if envs != 2 || commands != 2 {
		t.Fatal("rejected environment or command committed")
	}
	restarted, err := NewLedger(l.db, []byte(strings.Repeat(string(byte(17)), 32)))
	if err != nil {
		t.Fatal(err)
	}
	// The new process has capacity disabled; the persisted pool still enforces it.
	if err = restarted.SaveEnvironment(context.Background(), capacityEnv("restart-denied", "node-b")); !errors.Is(err, errcode.ErrRuntimeCapacityExhausted) {
		t.Fatalf("disabled replica bypassed policy: %v", err)
	}
	if err = restarted.SaveEnvironment(context.Background(), capacityEnv("unknown-node", "unbound")); !errors.Is(err, errcode.ErrRuntimeCapacityUnavailable) {
		t.Fatal("unknown node bypassed saved policy")
	}
}
func TestCapacityRetryRollbackAndLifecycleHold(t *testing.T) {
	ctx := context.Background()
	l := testLedger(t)
	l.capacity = config.RuntimeCapacity{Enabled: true, MaxCPUMillis: 1000, MaxMemoryBytes: 1 << 30}
	capacityNode(t, l, "node", strings.Repeat("c", 64))
	e := capacityEnv("owned", "node")
	if err := l.SaveEnvironment(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := l.SaveEnvironment(ctx, e); err != nil {
		t.Fatal("exact retry consumed capacity", err)
	}
	changed := e
	changed.Request.Memory = 2 << 30
	if err := l.SaveEnvironment(ctx, changed); err == nil {
		t.Fatal("payload conflict accepted")
	}
	for _, state := range []string{"online", "hibernated", "offline"} {
		if err := l.SetEnvironment(ctx, e.ID, "project", "sandbox", state); err != nil {
			t.Fatal(err)
		}
		if err := l.SaveEnvironment(ctx, capacityEnv("competitor", e.NodeID)); !errors.Is(err, errcode.ErrRuntimeCapacityExhausted) {
			t.Fatalf("state %s released capacity: %v", state, err)
		}
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := taskflow.CreateTaskReq{ID: uuid.New(), VMID: e.ID, CodingAgent: taskflow.CodingAgentOpenCode}
	if err = l.stageTx(ctx, tx, req); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	var commands int
	l.db.QueryRow(`SELECT count(*) FROM runtime_commands`).Scan(&commands)
	if commands != 0 {
		t.Fatal("rolled back staged task committed")
	}
	if count, _, _ := capacityUsage(t, l); count != 1 {
		t.Fatal("task staging changed environment reservation")
	}
	// Fencing alone, including a lost RPC response, must retain capacity.
	if _, err = l.beginRecycle(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if err = l.Stage(ctx, req); err == nil {
		t.Fatal("new task crossed recycle fence")
	}
	if count, _, _ := capacityUsage(t, l); count != 1 {
		t.Fatal("unconfirmed recycle released capacity")
	}
	if err = l.completeRecycle(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if err = l.completeRecycle(ctx, e.ID); err != nil {
		t.Fatal("recycle finalization not idempotent")
	}
	if count, _, _ := capacityUsage(t, l); count != 0 {
		t.Fatal("confirmed recycle did not release")
	}
	if err = l.SaveEnvironment(ctx, capacityEnv("replacement", e.NodeID)); err != nil {
		t.Fatal(err)
	}
}
func TestCapacityBackfillAndConservativePolicy(t *testing.T) {
	ctx := context.Background()
	l := testLedger(t)
	pool := strings.Repeat("d", 64)
	capacityNode(t, l, "old", pool)
	for i := 0; i < 3; i++ {
		if err := l.SaveEnvironment(ctx, capacityEnv(fmt.Sprintf("old-%d", i), "old")); err != nil {
			t.Fatal(err)
		}
	}
	l.capacity = config.RuntimeCapacity{Enabled: true, MaxCPUMillis: 1000, MaxMemoryBytes: 1 << 30}
	var s NodeSnapshot
	var b []byte
	l.db.QueryRow(`SELECT observation FROM runtime_nodes WHERE node_id='old'`).Scan(&b)
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if err := l.bindNode(ctx, "old", s); err != nil {
		t.Fatal(err)
	}
	if count, cpu, _ := capacityUsage(t, l); count != 3 || cpu != 3000 {
		t.Fatal("existing overcommitted environments were not counted")
	}
	if err := l.SaveEnvironment(ctx, capacityEnv("new", "old")); !errors.Is(err, errcode.ErrRuntimeCapacityExhausted) {
		t.Fatal("backfill allowed more overcommit")
	}
	// Heartbeats with a larger budget, or a disabled replica, cannot widen it.
	l.capacity = config.RuntimeCapacity{Enabled: true, MaxCPUMillis: 4000}
	if err := l.bindNode(ctx, "old", s); err != nil {
		t.Fatal(err)
	}
	l.capacity = config.RuntimeCapacity{}
	if err := l.bindNode(ctx, "old", s); err != nil {
		t.Fatal(err)
	}
	var limit int64
	var enforced bool
	l.db.QueryRow(`SELECT cpu_limit_millis,enforced FROM runtime_capacity_pools`).Scan(&limit, &enforced)
	if limit != 1000 || !enforced {
		t.Fatal("saved capacity policy was relaxed")
	}
	s.CapacityID = strings.Repeat("e", 64)
	if err := l.bindNode(ctx, "old", s); err == nil {
		t.Fatal("capacity identity replacement accepted")
	}
}
func TestCapacityFailureReleaseRequiresNoSubmission(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		t.Run(fmt.Sprint(submitted), func(t *testing.T) {
			ctx := context.Background()
			l := testLedger(t)
			l.capacity = config.RuntimeCapacity{Enabled: true}
			capacityNode(t, l, "node", strings.Repeat("f", 64))
			e := capacityEnv("failed", "node")
			if err := l.saveEnvironment(ctx, e, true); err != nil {
				t.Fatal(err)
			}
			cmd, err := l.Claim(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if submitted {
				if err = l.BeginSubmission(ctx, &cmd); err != nil {
					t.Fatal(err)
				}
			}
			if err = l.Finish(ctx, cmd, "failed", "test failure"); err != nil {
				t.Fatal(err)
			}
			count, _, _ := capacityUsage(t, l)
			if submitted && count != 1 || !submitted && count != 0 {
				t.Fatal("unsafe failure reservation release")
			}
			if submitted {
				if _, err = l.beginRecycle(ctx, e.ID); !errors.Is(err, errAdmissionUncertain) {
					t.Fatal("unmapped submitted Run treated as absent")
				}
			} else {
				if err = l.ensureReservation(ctx, e.ID); !errors.Is(err, errcode.ErrRuntimeCapacityUnavailable) {
					t.Fatal("released failed environment silently reacquired")
				}
			}
		})
	}
}
func TestCapacityMetadataRequiredWhenEnabled(t *testing.T) {
	l := testLedger(t)
	l.capacity = config.RuntimeCapacity{Enabled: true}
	if err := l.bindNode(context.Background(), "legacy", validNodeSnapshot()); !errors.Is(err, errcode.ErrRuntimeCapacityUnavailable) {
		t.Fatal("old daemon accepted for capacity admission")
	}
	if err := l.SaveEnvironment(context.Background(), capacityEnv("missing", "legacy")); !errors.Is(err, errcode.ErrRuntimeCapacityUnavailable) {
		t.Fatal("unverified capacity accepted")
	}
	var count int
	if err := l.db.QueryRow(`SELECT count(*) FROM runtime_environments`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected environment persisted")
	}
}

func TestCapacityActivationWaitsForLegacyAliasesThenBackfills(t *testing.T) {
	ctx := context.Background()
	l := testLedger(t)
	a, b := validNodeSnapshot(), validNodeSnapshot()
	for id, s := range map[string]NodeSnapshot{"legacy-a": a, "legacy-b": b} {
		if err := l.bindNode(ctx, id, s); err != nil {
			t.Fatal(err)
		}
		if err := l.saveNode(ctx, id, s); err != nil {
			t.Fatal(err)
		}
		if err := l.SaveEnvironment(ctx, capacityEnv(id+"-env", id)); err != nil {
			t.Fatal(err)
		}
	}
	l.capacity = config.RuntimeCapacity{Enabled: true, MaxCPUMillis: 1000, MaxMemoryBytes: 1 << 30}
	a.CapacityID = strings.Repeat("9", 64)
	b.CapacityID = a.CapacityID
	if err := l.bindNode(ctx, "legacy-a", a); !errors.Is(err, errcode.ErrRuntimeCapacityUnavailable) {
		t.Fatal("incomplete upgrade enabled policy", err)
	}
	var recorded string
	l.db.QueryRow(`SELECT capacity_id FROM runtime_nodes WHERE node_id='legacy-a'`).Scan(&recorded)
	if recorded != a.CapacityID {
		t.Fatal("rollout barrier lost verified capacity mapping")
	}
	if err := l.bindNode(ctx, "legacy-b", b); err != nil {
		t.Fatal(err)
	}
	if err := l.bindNode(ctx, "legacy-a", a); err != nil {
		t.Fatal(err)
	}
	if count, cpu, _ := capacityUsage(t, l); count != 2 || cpu != 2000 {
		t.Fatal("activation omitted existing alias environments")
	}
	if err := l.SaveEnvironment(ctx, capacityEnv("new", "legacy-a")); !errors.Is(err, errcode.ErrRuntimeCapacityExhausted) {
		t.Fatal("activation overcommitted shared engine")
	}
}
