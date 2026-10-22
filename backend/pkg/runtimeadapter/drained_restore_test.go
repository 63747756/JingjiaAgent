package runtimeadapter

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/runtimenode"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func TestDrainingNodeRestoresExistingTaskWithCapacityChecks(t *testing.T) {
	l, c, profile := workRuntimeLedger(t)
	ctx := context.Background()
	req := workRuntimeTask(t, l, profile)
	if _, err := l.db.Exec(`UPDATE runtime_commands SET state='complete' WHERE task_id=$1`, req.ID); err != nil {
		t.Fatal(err)
	}
	if err := l.SetEnvironment(ctx, req.VMID, "old-project", "old-sandbox", "hibernated"); err != nil {
		t.Fatal(err)
	}
	env, err := l.Environment(ctx, req.VMID)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migration/000049_dynamic_runtime_nodes.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	c.nodeStore, err = runtimenode.New(l.db, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	l.dynamicNodes = true
	if err = c.nodeStore.Create(ctx, runtimenode.Record{
		Config:   config.RuntimeNode{ID: "node", URL: "http://127.0.0.1:12345", OwnerID: env.OwnerID},
		Material: runtimenode.Material{Token: "test-node"}, Imported: true, Accepting: true, Phase: "ready",
	}); err != nil {
		t.Fatal(err)
	}
	l.capacity = config.RuntimeCapacity{Enabled: true, MaxCPUMillis: 2000, MaxMemoryBytes: 2 << 30}
	capacityNode(t, l, "node", strings.Repeat("a", 64))
	stopped, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = ReleaseCheckpointReservation(ctx, stopped, env.ID); err != nil {
		stopped.Rollback()
		t.Fatal(err)
	}
	if err = stopped.Commit(); err != nil {
		t.Fatal(err)
	}
	other := capacityEnv("occupied", "node")
	other.Request.Cores, other.Request.Memory = "2", 2<<30
	if err = l.SaveEnvironment(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err = c.nodeStore.SetAccepting(ctx, "node", false); err != nil {
		t.Fatal(err)
	}
	if err = l.SaveEnvironment(ctx, capacityEnv("new-task", "node")); err == nil {
		t.Fatal("draining node accepted new task")
	}
	checkpoint := uuid.New()
	digest := strings.Repeat("b", 64)
	key := "work-private/checkpoints/" + checkpoint.String() + ".zip"
	if _, err = l.db.Exec(`INSERT INTO workspace_checkpoints(id,task_id,environment_id,state,archive_key,payload_sha256) VALUES($1,$2,$3,'committed',$4,$5)`, checkpoint, req.ID, req.VMID, key, digest); err != nil {
		t.Fatal(err)
	}
	id, err := c.RestoreCheckpoint(ctx, uuid.MustParse(env.OwnerID), req.ID, checkpoint, "https://internal.invalid/checkpoint.zip", key, digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := l.Environment(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := l.Claim(ctx)
	if err != nil || cmd.Operation != "restore_prepare" {
		t.Fatalf("claim: %+v %v", cmd, err)
	}
	if err = c.scheduleEnvironment(ctx, cmd, restored); !errors.Is(err, errWorkQueued) {
		t.Fatalf("restore bypassed occupied capacity: %v", err)
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = releaseReservationTx(ctx, tx, other.ID, "test stopped workspace"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = l.db.Exec(`UPDATE runtime_commands SET available_at=now() WHERE id=$1`, cmd.ID); err != nil {
		t.Fatal(err)
	}
	cmd, err = l.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.scheduleEnvironment(ctx, cmd, restored); err != nil {
		t.Fatalf("drain blocked existing task restoration: %v", err)
	}
	// A forged restore_prepare without the durable task/restore mapping must
	// not receive the exception, even though it uses the same host.
	fake := Environment{ID: "unrelated", OwnerID: env.OwnerID, NodeID: "node", Request: taskflow.CreateVirtualMachineReq{TaskID: uuid.New()}}
	if ok, err := restoringExistingTask(ctx, l.db, cmd, fake); err != nil || ok {
		t.Fatalf("unrelated environment treated as restore: %v %v", ok, err)
	}
	if _, err = l.db.Exec(`UPDATE runtime_node_configs SET phase='verifying' WHERE node_id='node'`); err != nil {
		t.Fatal(err)
	}
	if err = c.reservePlacement(ctx, cmd, restored, "node", false); err == nil {
		t.Fatal("restore bypassed node readiness")
	}
	if _, err = l.db.Exec(`UPDATE runtime_node_configs SET phase='ready',revoked_at=now() WHERE node_id='node'`); err != nil {
		t.Fatal(err)
	}
	if err = c.reservePlacement(ctx, cmd, restored, "node", false); err == nil {
		t.Fatal("restore bypassed node revocation")
	}
}
