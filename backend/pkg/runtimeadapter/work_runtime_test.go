package runtimeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

// These tests use real PostgreSQL statement execution and isolated schemas.
// They exercise durable admission contracts rather than SQL mock expectations.
func workRuntimeLedger(t *testing.T) (*Ledger, *Client, uuid.UUID) {
	t.Helper()
	l := testLedger(t)
	_, err := l.db.Exec(`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS deleted_at timestamptz;
CREATE TABLE users(id uuid PRIMARY KEY);
CREATE TABLE model_api_keys(id uuid PRIMARY KEY,user_id uuid,virtualmachine_id text,kind text);
CREATE TABLE projects(id uuid PRIMARY KEY);
ALTER TABLE tasks ADD COLUMN user_id uuid;
CREATE TABLE teams(id uuid PRIMARY KEY, task_concurrency_limit integer NOT NULL DEFAULT 3,task_total_concurrency_limit integer NOT NULL DEFAULT 7,created_at timestamptz DEFAULT now(),deleted_at timestamptz);
CREATE TABLE team_members(team_id uuid,user_id uuid);
CREATE TABLE work_execution_profiles(id uuid PRIMARY KEY,max_concurrent_tasks integer NOT NULL);
CREATE TABLE work_tasks(task_id uuid PRIMARY KEY REFERENCES tasks(id),owner_id uuid NOT NULL,profile_id uuid NOT NULL REFERENCES work_execution_profiles(id),team_id uuid,project_id uuid,delivery_status text NOT NULL DEFAULT 'pending',updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE task_virtualmachines(task_id uuid PRIMARY KEY REFERENCES tasks(id),virtualmachine_id text NOT NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"000004_host_vm.up.sql", "000034_work_storage.up.sql", "000037_work_runtime.up.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "migration", name))
		if err != nil {
			t.Fatal(err)
		}
		definition := string(migration)
		if name == "000004_host_vm.up.sql" {
			// Apply the real host/VM definitions, excluding unrelated legacy
			// team host tables that require the uuid-ossp extension namespace.
			definition = strings.Split(definition, "CREATE TABLE IF NOT EXISTS team_hosts")[0]
		}
		if _, err = l.db.Exec(definition); err != nil {
			t.Fatalf("migration %s: %v", name, err)
		}
	}
	if _, err = l.db.Exec(`ALTER TABLE work_run_slots DROP CONSTRAINT work_run_slots_task_id_fkey;
ALTER TABLE work_run_slots ADD FOREIGN KEY(task_id) REFERENCES tasks(id);
ALTER TABLE work_run_slots DROP COLUMN profile_id;
ALTER TABLE work_run_slots ADD COLUMN owner_id uuid,ADD COLUMN team_id uuid;
INSERT INTO teams(id) VALUES('11111111-1111-4111-8111-111111111111');`); err != nil {
		t.Fatal(err)
	}
	l.workEnabled = true
	profile := uuid.New()
	if _, err = l.db.Exec(`INSERT INTO work_execution_profiles(id,max_concurrent_tasks) VALUES($1,50)`, profile); err != nil {
		t.Fatal(err)
	}
	c := &Client{ledger: l, engines: map[string]*Engine{"node": {}}}
	return l, c, profile
}

func workRuntimeTask(t *testing.T, l *Ledger, profile uuid.UUID) taskflow.CreateTaskReq {
	t.Helper()
	tid, owner := uuid.New(), uuid.New()
	env := "agent_" + uuid.NewString()
	vm := taskflow.CreateVirtualMachineReq{ID: env, TaskID: tid, Cores: "1", Memory: 1 << 30, Envs: []string{"JINGJIAAGENT_WORK_MODE=1"}}
	ctx := context.Background()
	if err := l.SaveEnvironment(ctx, Environment{ID: env, OwnerID: owner.String(), NodeID: "node", Request: vm}); err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id) VALUES($1)`, []any{owner}},
		{`INSERT INTO tasks(id,status,user_id) VALUES($1,'pending',$2)`, []any{tid, owner}},
		{`INSERT INTO team_members(team_id,user_id) VALUES('11111111-1111-4111-8111-111111111111',$1)`, []any{owner}},
		{`INSERT INTO work_tasks(task_id,owner_id,profile_id) VALUES($1,$2,$3)`, []any{tid, owner, profile}},
		{`INSERT INTO virtualmachines(id,host_id,user_id,name,environment_id) VALUES($1,'node',$2,'work fixture',$1)`, []any{env, owner}},
		{`INSERT INTO task_virtualmachines(task_id,virtualmachine_id) VALUES($1,$2)`, []any{tid, env}},
	}
	for _, st := range statements {
		if _, err := l.db.ExecContext(ctx, st.query, st.args...); err != nil {
			t.Fatal(err)
		}
	}
	req := taskflow.CreateTaskReq{ID: tid, VMID: env, Text: "原始任务：生成一次报告，不得在恢复时重发", CodingAgent: taskflow.CodingAgentOpenCode}
	if err := l.Stage(ctx, req); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestWorkPrepareSlotsBoundConcurrencyAndTransferToFirstTurn(t *testing.T) {
	l, c, profile := workRuntimeLedger(t)
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		workRuntimeTask(t, l, profile)
	}
	commands := make([]Command, 12)
	for i := range commands {
		var err error
		commands[i], err = l.Claim(ctx)
		if err != nil || commands[i].Operation != "prepare" {
			t.Fatalf("claim preparation %d: %v, %+v", i, err, commands[i])
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, len(commands))
	for _, command := range commands {
		wg.Add(1)
		go func(cmd Command) {
			defer wg.Done()
			results <- c.acquireWorkSlot(ctx, cmd)
		}(command)
	}
	wg.Wait()
	close(results)
	admitted, queued := 0, 0
	for err := range results {
		if err == nil {
			admitted++
		} else if errors.Is(err, errWorkQueued) {
			queued++
		} else {
			t.Fatal(err)
		}
	}
	var count int
	if err := l.db.QueryRow(`SELECT count(*) FROM work_run_slots`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if admitted != 7 || queued != 5 || count != 7 {
		t.Fatalf("preparation overcommitted global slots: admitted=%d queued=%d slots=%d", admitted, queued, count)
	}
	var prepareID, tid, eid string
	if err := l.db.QueryRow(`SELECT c.id,c.task_id,c.environment_id FROM runtime_commands c JOIN work_run_slots s ON s.command_id=c.id LIMIT 1`).Scan(&prepareID, &tid, &eid); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`UPDATE runtime_commands SET state='complete',lease_token=NULL,lease_until=NULL WHERE id=$1`, prepareID); err != nil {
		t.Fatal(err)
	}
	req, err := l.Intent(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Enqueue(ctx, eid, tid, "task", 1, req); err != nil {
		t.Fatal(err)
	}
	// Avoid older queued preparations winning this targeted transfer claim.
	if _, err = l.db.Exec(`UPDATE runtime_commands SET available_at=now()+interval '1 minute' WHERE operation='prepare' AND state='pending'`); err != nil {
		t.Fatal(err)
	}
	turn, err := l.Claim(ctx)
	if err != nil || turn.TaskID != tid || turn.Operation != "task" {
		t.Fatalf("claim first task turn: %v, %+v", err, turn)
	}
	if err = c.acquireWorkSlot(ctx, turn); err != nil {
		t.Fatalf("first turn failed to inherit its completed prepare slot at full capacity: %v", err)
	}
	var transferred string
	if err = l.db.QueryRow(`SELECT command_id FROM work_run_slots WHERE task_id=$1`, tid).Scan(&transferred); err != nil {
		t.Fatal(err)
	}
	if err = l.db.QueryRow(`SELECT count(*) FROM work_run_slots`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if transferred != turn.ID || count != 7 {
		t.Fatalf("prepare slot did not transfer exactly once: command=%s want=%s slots=%d", transferred, turn.ID, count)
	}
	if err = l.Finish(ctx, turn, "complete", ""); err != nil {
		t.Fatal(err)
	}
	if err = l.db.QueryRow(`SELECT count(*) FROM work_run_slots`).Scan(&count); err != nil || count != 6 {
		t.Fatalf("terminal task did not release its slot: %d, %v", count, err)
	}
}

func TestWorkVerifiedCheckpointCapacityReacquiresWithFreshAdmission(t *testing.T) {
	l, c, profile := workRuntimeLedger(t)
	l.capacity = config.RuntimeCapacity{Enabled: true, MaxCPUMillis: 1000, MaxMemoryBytes: 1 << 30}
	capacityNode(t, l, "node", strings.Repeat("d", 64))
	req := workRuntimeTask(t, l, profile)
	ctx := context.Background()
	if err := l.ensureReservation(ctx, req.VMID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`UPDATE runtime_commands SET state='complete' WHERE task_id=$1`, req.ID); err != nil {
		t.Fatal(err)
	}
	if err := l.SetEnvironment(ctx, req.VMID, "work-project", "stopped-sandbox", "hibernated"); err != nil {
		t.Fatal(err)
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = ReleaseCheckpointReservation(ctx, tx, req.VMID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if count, _, _ := capacityUsage(t, l); count != 0 {
		t.Fatal("verified stopped checkpoint retained its active capacity")
	}
	if err = c.TaskManager().Continue(ctx, taskflow.TaskReq{Task: &taskflow.Task{ID: req.ID, Text: "继续修改报告", ClientMessageID: uuid.NewString()}}); err != nil {
		t.Fatal(err)
	}
	// Capacity is acquired after environment preparation promotes the input.
	if _, err = l.db.Exec(`UPDATE runtime_commands SET state='pending' WHERE task_id=$1 AND state='waiting'`, req.ID); err != nil {
		t.Fatal(err)
	}
	if err = l.ensureReservation(ctx, req.VMID); err != nil {
		t.Fatalf("legitimate continuation could not reacquire verified checkpoint capacity: %v", err)
	}
	if count, cpu, memory := capacityUsage(t, l); count != 1 || cpu != 1000 || memory != 1<<30 {
		t.Fatalf("reacquisition counted incorrectly: %d, %d, %d", count, cpu, memory)
	}
	if err = l.ensureReservation(ctx, req.VMID); err != nil {
		t.Fatal("reacquisition retry was not idempotent", err)
	}
	if err = l.SaveEnvironment(ctx, capacityEnv("competitor", "node")); !errors.Is(err, errcode.ErrRuntimeCapacityExhausted) {
		t.Fatalf("reacquisition failed to enforce pool capacity: %v", err)
	}
	// A stopped checkpoint does not retain a perpetual grant to capacity.
	tx, err = l.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = ReleaseCheckpointReservation(ctx, tx, req.VMID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = l.SaveEnvironment(ctx, capacityEnv("competitor", "node")); err != nil {
		t.Fatal(err)
	}
	if err = l.ensureReservation(ctx, req.VMID); !errors.Is(err, errcode.ErrRuntimeCapacityExhausted) {
		t.Fatalf("checkpoint reacquisition bypassed capacity now owned by another environment: %v", err)
	}
}

func TestWorkFailedRestoreAllowsFreshAttemptWithoutReissuingOriginalPrompt(t *testing.T) {
	l, c, profile := workRuntimeLedger(t)
	ctx := context.Background()
	req := workRuntimeTask(t, l, profile)
	if _, err := l.db.Exec(`UPDATE runtime_commands SET state='complete' WHERE task_id=$1`, req.ID); err != nil {
		t.Fatal(err)
	}
	if err := l.SetEnvironment(ctx, req.VMID, "old-runtime-project", "old-stopped-sandbox", "hibernated"); err != nil {
		t.Fatal(err)
	}
	e, err := l.Environment(ctx, req.VMID)
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.MustParse(e.OwnerID)
	checkpoint := uuid.New()
	digest := strings.Repeat("a", 64)
	key := "work-private/checkpoints/" + req.ID.String() + "/" + checkpoint.String() + ".zip"
	address := "https://internal.invalid/checkpoint.zip#jingjiaagent-checkpoint=" + checkpoint.String() + "&expected_sha256=" + digest
	if _, err = l.db.Exec(`INSERT INTO workspace_checkpoints(id,task_id,environment_id,state,archive_key,payload_sha256) VALUES($1,$2,$3,'committed',$4,$5)`, checkpoint, req.ID, req.VMID, key, digest); err != nil {
		t.Fatal(err)
	}
	first, err := c.RestoreCheckpoint(ctx, owner, req.ID, checkpoint, address, key, digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := c.RestoreCheckpoint(ctx, owner, req.ID, checkpoint, address, key, digest, nil); err != nil || retry != first {
		t.Fatalf("same checkpoint preparation was not idempotent: %s %v", retry, err)
	}
	if _, err = c.RestoreCheckpoint(ctx, owner, req.ID, uuid.New(), address, key, digest, nil); err == nil {
		t.Fatal("another checkpoint was silently replaced by the active restoration")
	}
	current, err := l.EnvironmentForTask(ctx, req.ID.String())
	if err != nil || current.ID != req.VMID {
		t.Fatalf("unverified restore prematurely switched task mapping: %s %v", current.ID, err)
	}
	failed, err := l.Claim(ctx)
	if err != nil || failed.Operation != "restore_prepare" || failed.EnvironmentID != first {
		t.Fatalf("claim restore attempt: %+v %v", failed, err)
	}
	var recovery taskflow.CreateTaskReq
	if err = json.Unmarshal(failed.Payload, &recovery); err != nil {
		t.Fatal(err)
	}
	if recovery.Text == req.Text || strings.Contains(recovery.Text, "原始任务") || recovery.VMID != first {
		t.Fatalf("restore reissued the original user request: %+v", recovery)
	}
	if err = c.acquireWorkSlot(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if err = l.Finish(ctx, failed, "failed", "fixture archive verification rejected"); err != nil {
		t.Fatal(err)
	}
	var restoreState string
	var slots int
	if err = l.db.QueryRow(`SELECT state FROM runtime_restores WHERE task_id=$1`, req.ID).Scan(&restoreState); err != nil || restoreState != "failed" {
		t.Fatalf("terminal failure retained a preparing restore fence: %s %v", restoreState, err)
	}
	if err = l.db.QueryRow(`SELECT count(*) FROM work_run_slots WHERE task_id=$1`, req.ID).Scan(&slots); err != nil || slots != 0 {
		t.Fatalf("failed restore retained its work slot: %d %v", slots, err)
	}
	second, err := c.RestoreCheckpoint(ctx, owner, req.ID, checkpoint, address, key, digest, nil)
	if err != nil || second == first || second == req.VMID {
		t.Fatalf("failed preparation did not permit a fresh attempt: %s %v", second, err)
	}
	verified, err := l.Claim(ctx)
	if err != nil || verified.Operation != "restore_prepare" || verified.EnvironmentID != second {
		t.Fatalf("claim retry: %+v %v", verified, err)
	}
	if err = json.Unmarshal(verified.Payload, &recovery); err != nil || recovery.Text == req.Text {
		t.Fatal("restore retry revived the original request", err)
	}
	if err = l.SetEnvironment(ctx, second, "verified-runtime-project", "verified-restored-sandbox", "online"); err != nil {
		t.Fatal(err)
	}
	restored, err := l.Environment(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.db.Exec(`UPDATE tasks SET status='finished',completed_at=now() WHERE id=$1`, req.ID); err != nil {
		t.Fatal(err)
	}
	if err = c.commitRestore(ctx, verified, restored); err != nil {
		t.Fatalf("verified restore product/intent SQL commit: %v", err)
	}
	var interactive bool
	if err = l.db.QueryRow(`SELECT status='processing' AND completed_at IS NULL FROM tasks WHERE id=$1`, req.ID).Scan(&interactive); err != nil || !interactive {
		t.Fatalf("verified restoration did not permit a new user turn: %v %v", interactive, err)
	}
	current, err = l.EnvironmentForTask(ctx, req.ID.String())
	if err != nil || current.ID != second {
		t.Fatalf("verified retry did not switch task mapping: %s %v", current.ID, err)
	}
	var productEnvironment string
	if err = l.db.QueryRow(`SELECT virtualmachine_id FROM task_virtualmachines WHERE task_id=$1`, req.ID).Scan(&productEnvironment); err != nil || productEnvironment != second {
		t.Fatalf("product VM mapping did not commit with runtime mapping: %s %v", productEnvironment, err)
	}
	intent, err := l.Intent(ctx, req.ID.String())
	if err != nil || intent.Text == req.Text || intent.VMID != second {
		t.Fatalf("committed intent reused initial prompt or old environment: %+v %v", intent, err)
	}
}

func TestWorkOtherCapacityReleaseReasonsCannotReviveEnvironment(t *testing.T) {
	for _, reason := range []string{"recycled", "failed_before_submission", "administrative_release"} {
		t.Run(reason, func(t *testing.T) {
			l, _, profile := workRuntimeLedger(t)
			l.capacity = config.RuntimeCapacity{Enabled: true, MaxCPUMillis: 1000, MaxMemoryBytes: 1 << 30}
			capacityNode(t, l, "node", strings.Repeat("e", 64))
			req := workRuntimeTask(t, l, profile)
			ctx := context.Background()
			if err := l.ensureReservation(ctx, req.VMID); err != nil {
				t.Fatal(err)
			}
			if err := l.SetEnvironment(ctx, req.VMID, "project", "sandbox", "hibernated"); err != nil {
				t.Fatal(err)
			}
			tx, err := l.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err = releaseReservationTx(ctx, tx, req.VMID, reason); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err = l.ensureReservation(ctx, req.VMID); !errors.Is(err, errcode.ErrRuntimeCapacityUnavailable) {
				t.Fatalf("unsafe release reason revived an environment: %v", err)
			}
		})
	}
}
