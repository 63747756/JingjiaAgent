package runtimeadapter

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/google/uuid"
)

func storedPreview(t *testing.T, l *Ledger, id string) (portForward, sql.NullTime) {
	t.Helper()
	f, err := scanForward(l.db.QueryRow("SELECT "+forwardColumns+" FROM runtime_port_forwards WHERE id=$1", id))
	if err != nil {
		t.Fatal(err)
	}
	var released sql.NullTime
	if err = l.db.QueryRow("SELECT released_at FROM runtime_port_forwards WHERE id=$1", id).Scan(&released); err != nil {
		t.Fatal(err)
	}
	return f, released
}

func workPreviewFixture(t *testing.T) (*Client, taskflow.CreateTaskReq, uuid.UUID) {
	t.Helper()
	l, c, profile := workRuntimeLedger(t)
	req := workRuntimeTask(t, l, profile)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	t.Cleanup(app.Close)
	mux := http.NewServeMux()
	installPreviewTunnel(t, mux, app.URL)
	path, handler := rpc.NewExecServiceHandler(&previewExecFixture{})
	mux.Handle(path, handler)
	c.engines["node"] = testEngine(t, mux)
	c.preview = config.RuntimePreview{Mode: "port", BaseURL: "http://192.0.2.10", PortRange: "31000-31099"}
	if _, err := l.db.Exec(`UPDATE runtime_commands SET state='complete' WHERE task_id=$1`, req.ID); err != nil {
		t.Fatal(err)
	}
	if err := l.SetEnvironment(context.Background(), req.VMID, "project", "old-sandbox", "online"); err != nil {
		t.Fatal(err)
	}
	e, err := l.Environment(context.Background(), req.VMID)
	if err != nil {
		t.Fatal(err)
	}
	return c, req, uuid.MustParse(e.OwnerID)
}

func preparePreviewRestore(t *testing.T, c *Client, req taskflow.CreateTaskReq, owner uuid.UUID) (Command, Environment) {
	t.Helper()
	ctx := context.Background()
	checkpoint, digest := uuid.New(), strings.Repeat("a", 64)
	key := "work-private/checkpoints/" + req.ID.String() + "/" + checkpoint.String() + ".zip"
	address := "https://internal.invalid/checkpoint.zip#jingjiaagent-checkpoint=" + checkpoint.String() + "&expected_sha256=" + digest
	if _, err := c.ledger.db.Exec(`INSERT INTO workspace_checkpoints(id,task_id,environment_id,state,archive_key,payload_sha256) VALUES($1,$2,$3,'committed',$4,$5)`, checkpoint, req.ID, req.VMID, key, digest); err != nil {
		t.Fatal(err)
	}
	if err := c.ledger.SetEnvironment(ctx, req.VMID, "project", "", "deleted"); err != nil {
		t.Fatal(err)
	}
	newID, err := c.RestoreCheckpoint(ctx, owner, req.ID, checkpoint, address, key, digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := c.ledger.Claim(ctx)
	if err != nil || cmd.Operation != "restore_prepare" || cmd.EnvironmentID != newID {
		t.Fatalf("restore command: %+v %v", cmd, err)
	}
	if err = c.ledger.SetEnvironment(ctx, newID, "project", "restored-sandbox", "online"); err != nil {
		t.Fatal(err)
	}
	e, err := c.ledger.Environment(ctx, newID)
	if err != nil {
		t.Fatal(err)
	}
	return cmd, e
}

func TestPreviewTaskReservationSurvivesWorkspaceRestore(t *testing.T) {
	for _, state := range []string{"custom whitelist", "deny all", "closed"} {
		t.Run(state, func(t *testing.T) {
			c, req, owner := workPreviewFixture(t)
			ctx := context.Background()
			ips := []string{"192.0.2.31", "192.0.2.32"}
			raw, err := c.OpenTaskPreview(ctx, owner, req.ID, 8080, &ips)
			if err != nil {
				t.Fatal(err)
			}
			info := decodePreviewInfo(t, raw)
			id := *info.ForwardID
			if state == "deny all" {
				empty := []string{}
				if _, err = c.UpdateTaskPreviewWhitelist(ctx, owner, req.ID, 8080, &empty, false); err != nil {
					t.Fatal(err)
				}
			}
			if state == "closed" {
				if err = c.CloseTaskPreview(ctx, owner, req.ID, id); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := storedPreview(t, c.ledger, id)
			cmd, restored := preparePreviewRestore(t, c, req, owner)
			stopped, released := storedPreview(t, c.ledger, id)
			if released.Valid || stopped.Active != before.Active || stopped.PublicPort != before.PublicPort || stopped.EnvironmentID != req.VMID {
				t.Fatal("environment deletion released or changed the task reservation", stopped, released)
			}
			if err = c.commitRestore(ctx, cmd, restored); err != nil {
				t.Fatal(err)
			}
			after, released := storedPreview(t, c.ledger, id)
			if released.Valid || after.EnvironmentID != restored.ID || after.TaskID.String != req.ID.String() || after.PublicPort != before.PublicPort || after.Active != before.Active || !reflect.DeepEqual(after.Whitelist, before.Whitelist) || after.Revision <= stopped.Revision || c.previewURL(after).String() != *info.AccessURL {
				t.Fatal("verified restore changed preview identity/access settings", before, after, released)
			}
			// The list and management path use the replacement environment while
			// still exposing the same forward ID and saved whitelist.
			list, err := c.ListTaskPreviews(ctx, owner, req.ID, "restored-list")
			if err != nil || len(list.Ports) != 1 || *list.Ports[0].ForwardID != id || !reflect.DeepEqual(list.Ports[0].WhitelistIPs, before.Whitelist) {
				t.Fatalf("restored preview list: %+v %v", list, err)
			}
			if state == "closed" && (list.Ports[0].Status != "closed" || list.Ports[0].Success) {
				t.Fatal("restore reopened an employee-closed preview")
			}
			if state == "deny all" {
				if list.Ports[0].Success {
					t.Fatal("deny-all preview advertised access")
				}
				if _, err = c.RequestEnvironmentPreview(ctx, restored.ID, owner.String(), 8080, []string{"192.0.2.99"}); err == nil {
					t.Fatal("AI request replaced restored deny-all with default employee IP")
				}
				denied, _ := storedPreview(t, c.ledger, id)
				if len(denied.Whitelist) != 0 {
					t.Fatal("failed request mutated deny-all")
				}
			}
			if state == "custom whitelist" {
				var wg sync.WaitGroup
				for i := 0; i < 8; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						got, err := c.RequestEnvironmentPreview(ctx, restored.ID, owner.String(), 8080, []string{"192.0.2.99"})
						if err != nil || *got.ForwardID != id || *got.AccessURL != *info.AccessURL || !reflect.DeepEqual(got.WhitelistIPs, ips) {
							t.Errorf("concurrent restored request forked reservation: %+v %v", got, err)
						}
					}()
				}
				wg.Wait()
				var count int
				if err = c.ledger.db.QueryRow(`SELECT count(*) FROM runtime_port_forwards WHERE task_id=$1 AND port=8080`, req.ID).Scan(&count); err != nil || count != 1 {
					t.Fatal("concurrent allocation forked task mapping", count, err)
				}
			}
			// A still-running stale generation cannot allocate or serve previews.
			if _, err = c.ledger.db.Exec(`UPDATE runtime_environments SET state='online',sandbox_id='stale-sandbox' WHERE id=$1`, req.VMID); err != nil {
				t.Fatal(err)
			}
			if _, err = c.RequestEnvironmentPreview(ctx, req.VMID, owner.String(), 8080, ips); err == nil {
				t.Fatal("old generation was allowed to allocate")
			}
			if state == "custom whitelist" {
				if _, err = c.ledger.db.Exec(`UPDATE runtime_port_forwards SET environment_id=$2 WHERE id=$1`, id, req.VMID); err != nil {
					t.Fatal(err)
				}
				stale, _ := storedPreview(t, c.ledger, id)
				s := NewPreviewService(c, func(context.Context, string, string) error { return nil })
				defer s.cancel()
				r := httptest.NewRequest("GET", *info.AccessURL, nil)
				r.RemoteAddr = "192.0.2.31:1234"
				if _, _, err = s.allowed(ctx, stale, r); err == nil {
					t.Fatal("gateway served an obsolete environment despite task rebinding")
				}
			}
		})
	}
}

func TestPreviewTaskDeletionStartsCooldown(t *testing.T) {
	for _, deletion := range []string{"UPDATE tasks SET deleted_at=now() WHERE id=$1", "DELETE FROM tasks WHERE id=$1"} {
		t.Run(strings.Fields(deletion)[0], func(t *testing.T) {
			c, owner, task, env := previewTaskFixture(t)
			ctx := context.Background()
			info, err := c.RequestEnvironmentPreview(ctx, env, owner.String(), 8080, []string{"192.0.2.31"})
			if err != nil {
				t.Fatal(err)
			}
			id := *info.ForwardID
			if err = c.ledger.SetEnvironment(ctx, env, "project", "", "deleted"); err != nil {
				t.Fatal(err)
			}
			reserved, released := storedPreview(t, c.ledger, id)
			if released.Valid || !reserved.Active {
				t.Fatal("workspace recycle released task reservation")
			}
			if _, err = c.ledger.db.Exec(deletion, task); err != nil {
				t.Fatal(err)
			}
			closed, released := storedPreview(t, c.ledger, id)
			if !released.Valid || closed.Active || closed.PublicPort != reserved.PublicPort || closed.Revision <= reserved.Revision || !reflect.DeepEqual(closed.Whitelist, reserved.Whitelist) {
				t.Fatal("task deletion did not retain a closed cooldown reservation", closed, released)
			}
			if _, err = c.forward(ctx, id); err == nil {
				t.Fatal("deleted task still resolves to a public route")
			}
		})
	}
}

func waitPreviewRowLock(t *testing.T, l *Ledger, query string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		if err := l.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query=$1)`, query).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("request did not wait for the concurrent deletion row lock")
}

func TestPreviewAllocationCannotRaceTaskDeletion(t *testing.T) {
	for _, deletion := range []string{"UPDATE tasks SET deleted_at=now() WHERE id=$1", "DELETE FROM tasks WHERE id=$1", "UPDATE runtime_task_intents SET deletion_requested=true WHERE task_id=$1"} {
		t.Run(deletion, func(t *testing.T) {
			c, owner, task, env := previewTaskFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx, err := c.ledger.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.ExecContext(ctx, deletion, task); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := c.RequestEnvironmentPreview(ctx, env, owner.String(), 8080, []string{"192.0.2.31"})
				done <- err
			}()
			query := `SELECT deleted_at FROM tasks WHERE id=$1 FOR SHARE`
			if strings.Contains(deletion, "runtime_task_intents") {
				query = `SELECT environment_id,deletion_requested FROM runtime_task_intents WHERE task_id=$1 FOR SHARE`
			}
			waitPreviewRowLock(t, c.ledger, query)
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err == nil {
				t.Fatal("allocation resurrected a deleted/deleting task")
			}
			var count int
			if err = c.ledger.db.QueryRow("SELECT count(*) FROM runtime_port_forwards").Scan(&count); err != nil || count != 0 {
				t.Fatal("rejected allocation left a mapping", count, err)
			}
		})
	}
}

func TestPreviewRestoreCannotRaceTaskDeletion(t *testing.T) {
	c, req, owner := workPreviewFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := c.RequestEnvironmentPreview(ctx, req.VMID, owner.String(), 8080, []string{"192.0.2.31"})
	if err != nil {
		t.Fatal(err)
	}
	cmd, restored := preparePreviewRestore(t, c, req, owner)
	tx, err := c.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET deleted_at=now() WHERE id=$1`, req.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.commitRestore(ctx, cmd, restored) }()
	waitPreviewRowLock(t, c.ledger, `SELECT deleted_at FROM tasks WHERE id=$1 FOR SHARE`)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil {
		t.Fatal("restore committed after the task deletion trigger")
	}
	f, released := storedPreview(t, c.ledger, *info.ForwardID)
	if f.EnvironmentID != req.VMID || f.Active || !released.Valid {
		t.Fatal("failed restore changed or reopened a deleted task mapping", f, released)
	}
	current, err := c.ledger.EnvironmentForTask(ctx, req.ID.String())
	if err != nil || current.ID != req.VMID {
		t.Fatal("failed restore partially changed task environment", current.ID, err)
	}
}

func runPreviewTaskMigration(t *testing.T, l *Ledger, direction string) error {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "migration", "000053_task_preview_reservations."+direction+".sql"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = l.db.Exec(string(b))
	return err
}

func TestPreviewTaskMigrationPreservesCurrentSettingsAndHistory(t *testing.T) {
	l := testLedger(t)
	if err := runPreviewTaskMigration(t, l, "down"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	task := uuid.New()
	old, current := "agent_"+uuid.NewString(), "agent_"+uuid.NewString()
	for _, env := range []string{old, current} {
		if err := l.SaveEnvironment(ctx, Environment{ID: env, OwnerID: uuid.NewString(), NodeID: "node", Request: taskflow.CreateVirtualMachineReq{ID: env}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.db.Exec(`INSERT INTO tasks(id,status) VALUES($1,'running')`, task); err != nil {
		t.Fatal(err)
	}
	if err := l.Stage(ctx, taskflow.CreateTaskReq{ID: task, VMID: old}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`UPDATE runtime_task_intents SET environment_id=$2 WHERE task_id=$1`, task, current); err != nil {
		t.Fatal(err)
	}
	// The current mapping wins even if an old generation's row was created
	// later. A second port exists only in the deleted generation.
	oldID, currentID, orphanID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := l.db.Exec(`INSERT INTO runtime_port_forwards(id,environment_id,port,whitelist,public_port,active,released_at,created_at) VALUES
($1,$4,8080,'["192.0.2.40"]',31000,false,now(),now()),
($2,$5,8080,'["192.0.2.31"]',31001,false,NULL,now()-interval '1 hour'),
($3,$4,8081,'[]',31002,false,now(),now())`, oldID, currentID, orphanID, old, current); err != nil {
		t.Fatal(err)
	}
	if err := runPreviewTaskMigration(t, l, "up"); err != nil {
		t.Fatal(err)
	}
	winner, release := storedPreview(t, l, currentID)
	if winner.TaskID.String != task.String() || winner.EnvironmentID != current || winner.PublicPort.Int32 != 31001 || winner.Active || release.Valid || !reflect.DeepEqual(winner.Whitelist, []string{"192.0.2.31"}) {
		t.Fatal("upgrade did not preserve current closed mapping", winner, release)
	}
	loser, release := storedPreview(t, l, oldID)
	if loser.TaskID.Valid || !release.Valid || loser.Active || loser.PublicPort.Int32 != 31000 || !reflect.DeepEqual(loser.Whitelist, []string{"192.0.2.40"}) {
		t.Fatal("upgrade discarded superseded settings or left an active duplicate", loser, release)
	}
	orphan, release := storedPreview(t, l, orphanID)
	if orphan.TaskID.String != task.String() || orphan.EnvironmentID != current || release.Valid || orphan.Active || len(orphan.Whitelist) != 0 || orphan.PublicPort.Int32 != 31002 {
		t.Fatal("upgrade did not reserve prior generation's deny-all mapping", orphan, release)
	}
	if _, err := l.db.Exec(`INSERT INTO runtime_port_forwards(id,environment_id,task_id,port,whitelist) VALUES($1,$2,$3,8081,'[]')`, uuid.New(), old, task); err == nil {
		t.Fatal("database accepted duplicate task/port")
	}
}

func TestPreviewTaskMigrationDoesNotReclaimReusedPortsOrDeletedTasks(t *testing.T) {
	l := testLedger(t)
	if err := runPreviewTaskMigration(t, l, "down"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for index, lifecycle := range []string{"live", "soft deleted", "hard deleted"} {
		task, env, id := uuid.New(), "agent_"+uuid.NewString(), uuid.NewString()
		if err := l.SaveEnvironment(ctx, Environment{ID: env, OwnerID: uuid.NewString(), NodeID: "node", Request: taskflow.CreateVirtualMachineReq{ID: env}}); err != nil {
			t.Fatal(err)
		}
		if _, err := l.db.Exec(`INSERT INTO tasks(id,status) VALUES($1,'finished')`, task); err != nil {
			t.Fatal(err)
		}
		if err := l.Stage(ctx, taskflow.CreateTaskReq{ID: task, VMID: env}); err != nil {
			t.Fatal(err)
		}
		if lifecycle == "soft deleted" {
			if _, err := l.db.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, task); err != nil {
				t.Fatal(err)
			}
		}
		if lifecycle == "hard deleted" {
			if _, err := l.db.Exec(`DELETE FROM tasks WHERE id=$1`, task); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := l.db.Exec(`INSERT INTO runtime_port_forwards(id,environment_id,port,whitelist,public_port,active,released_at) VALUES($1,$2,$3,'["192.0.2.31"]',NULL,false,now()-interval '25 hours')`, id, env, 8080+index); err != nil {
			t.Fatal(err)
		}
	}
	// A different environment legitimately owns a port already reclaimed by 51.
	other := "agent_" + uuid.NewString()
	if err := l.SaveEnvironment(ctx, Environment{ID: other, OwnerID: uuid.NewString(), NodeID: "node", Request: taskflow.CreateVirtualMachineReq{ID: other}}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`INSERT INTO runtime_port_forwards(id,environment_id,port,whitelist,public_port) VALUES($1,$2,8080,'["192.0.2.40"]',31000)`, uuid.New(), other); err != nil {
		t.Fatal(err)
	}
	if err := runPreviewTaskMigration(t, l, "up"); err != nil {
		t.Fatal(err)
	}
	var valid bool
	if err := l.db.QueryRow(`SELECT count(*)=1 FROM runtime_port_forwards WHERE task_id IS NOT NULL AND released_at IS NULL AND public_port IS NULL AND NOT active`).Scan(&valid); err != nil || !valid {
		t.Fatal("live task's reclaimed address was not kept unassigned/reserved", valid, err)
	}
	if err := l.db.QueryRow(`SELECT count(*)=2 FROM runtime_port_forwards WHERE task_id IS NOT NULL AND released_at IS NOT NULL AND NOT active`).Scan(&valid); err != nil || !valid {
		t.Fatal("deleted task mapping revived during upgrade", valid, err)
	}
	if err := l.db.QueryRow(`SELECT active AND public_port=31000 AND task_id IS NULL FROM runtime_port_forwards WHERE environment_id=$1`, other).Scan(&valid); err != nil || !valid {
		t.Fatal("upgrade stole another environment's public port", valid, err)
	}
}

func TestPreviewTaskMigrationRejectsAmbiguousRuntimeOwnership(t *testing.T) {
	c, _, task, env := previewTaskFixture(t)
	l := c.ledger
	if err := runPreviewTaskMigration(t, l, "down"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`INSERT INTO runtime_port_forwards(id,environment_id,port,whitelist) VALUES($1,$2,8080,'["192.0.2.31"]')`, uuid.New(), env); err != nil {
		t.Fatal(err)
	}
	// The intent and original command belong to task, but another task's durable
	// command incorrectly points at the same environment. The migration fails
	// atomically instead of publishing one user's service under another task.
	if _, err := l.db.Exec(`INSERT INTO runtime_commands(id,environment_id,task_id,operation,payload,payload_hash) SELECT $1,environment_id,$2,'task',payload,payload_hash FROM runtime_task_intents WHERE task_id=$3`, uuid.New(), uuid.New(), task); err != nil {
		t.Fatal(err)
	}
	if err := runPreviewTaskMigration(t, l, "up"); err == nil || !strings.Contains(err.Error(), "multiple tasks") {
		t.Fatal("ambiguous preview ownership was silently migrated", err)
	}
	var changed bool
	if err := l.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='runtime_port_forwards' AND column_name='task_id')`).Scan(&changed); err != nil || changed {
		t.Fatal("failed migration was not atomic", changed, err)
	}
}
