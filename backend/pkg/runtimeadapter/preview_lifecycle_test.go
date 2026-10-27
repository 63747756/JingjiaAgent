package runtimeadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func storedPreview(t *testing.T, l *Ledger, id string) portForward {
	t.Helper()
	f, err := scanForward(l.db.QueryRow("SELECT "+forwardColumns+" FROM runtime_port_forwards WHERE id=$1", id))
	if err != nil {
		t.Fatal(err)
	}
	return f
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
	c.preview = config.RuntimePreview{Mode: "domain", BaseURL: "http://preview.example.net"}
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
	for _, state := range []string{"public", "custom whitelist", "deny all", "closed"} {
		t.Run(state, func(t *testing.T) {
			c, req, owner := workPreviewFixture(t)
			ctx := context.Background()
			ips := []string{"192.0.2.31", "192.0.2.32"}
			raw, err := c.OpenTaskPreview(ctx, owner, req.ID, 8080, false)
			if err != nil {
				t.Fatal(err)
			}
			info := decodePreviewInfo(t, raw)
			id := *info.ForwardID
			if state != "public" {
				if _, err = c.UpdateTaskPreviewWhitelist(ctx, owner, req.ID, 8080, "ip_allowlist", &ips, ""); err != nil {
					t.Fatal(err)
				}
			}
			if state == "deny all" {
				empty := []string{}
				if _, err = c.UpdateTaskPreviewWhitelist(ctx, owner, req.ID, 8080, "ip_allowlist", &empty, ""); err != nil {
					t.Fatal(err)
				}
			}
			if state == "closed" {
				if err = c.CloseTaskPreview(ctx, owner, req.ID, id); err != nil {
					t.Fatal(err)
				}
			}
			before := storedPreview(t, c.ledger, id)
			cmd, restored := preparePreviewRestore(t, c, req, owner)
			stopped := storedPreview(t, c.ledger, id)
			if stopped.Active != before.Active || stopped.AccessMode != before.AccessMode || stopped.EnvironmentID != req.VMID {
				t.Fatal("environment deletion changed the saved route policy", stopped)
			}
			if err = c.commitRestore(ctx, cmd, restored); err != nil {
				t.Fatal(err)
			}
			after := storedPreview(t, c.ledger, id)
			if after.EnvironmentID != restored.ID || after.TaskID.String != req.ID.String() || after.AccessMode != before.AccessMode || after.Active != before.Active || !reflect.DeepEqual(after.Whitelist, before.Whitelist) || after.Revision <= stopped.Revision || c.previewURL(after).String() != *info.AccessURL {
				t.Fatal("verified restore changed preview identity/access settings", before, after)
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
				if got, requestErr := c.RequestEnvironmentPreview(ctx, restored.ID, owner.String(), 8080); requestErr != nil || got.Success || got.AccessMode != "ip_allowlist" {
					t.Fatal("AI request changed restored deny-all policy", got, requestErr)
				}
				denied := storedPreview(t, c.ledger, id)
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
						got, err := c.RequestEnvironmentPreview(ctx, restored.ID, owner.String(), 8080)
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
			if _, err = c.RequestEnvironmentPreview(ctx, req.VMID, owner.String(), 8080); err == nil {
				t.Fatal("old generation was allowed to allocate")
			}
			if state == "custom whitelist" {
				if _, err = c.ledger.db.Exec(`UPDATE runtime_port_forwards SET environment_id=$2 WHERE id=$1`, id, req.VMID); err != nil {
					t.Fatal(err)
				}
				stale := storedPreview(t, c.ledger, id)
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

func TestPreviewTaskDeletionRevokesRoute(t *testing.T) {
	for _, deletion := range []string{"UPDATE tasks SET deleted_at=now() WHERE id=$1", "DELETE FROM tasks WHERE id=$1"} {
		t.Run(strings.Fields(deletion)[0], func(t *testing.T) {
			c, owner, task, env := previewTaskFixture(t)
			ctx := context.Background()
			info, err := c.RequestEnvironmentPreview(ctx, env, owner.String(), 8080)
			if err != nil {
				t.Fatal(err)
			}
			id := *info.ForwardID
			if err = c.ledger.SetEnvironment(ctx, env, "project", "", "deleted"); err != nil {
				t.Fatal(err)
			}
			reserved := storedPreview(t, c.ledger, id)
			if !reserved.Active {
				t.Fatal("workspace recycle released task reservation")
			}
			if _, err = c.ledger.db.Exec(deletion, task); err != nil {
				t.Fatal(err)
			}
			closed := storedPreview(t, c.ledger, id)
			if closed.Active || closed.ID != reserved.ID || closed.AccessMode != reserved.AccessMode || closed.Revision <= reserved.Revision || !reflect.DeepEqual(closed.Whitelist, reserved.Whitelist) {
				t.Fatal("task deletion did not revoke and retain route identity", closed)
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
				_, err := c.RequestEnvironmentPreview(ctx, env, owner.String(), 8080)
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
	info, err := c.RequestEnvironmentPreview(ctx, req.VMID, owner.String(), 8080)
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
	f := storedPreview(t, c.ledger, *info.ForwardID)
	if f.EnvironmentID != req.VMID || f.Active {
		t.Fatal("failed restore changed or reopened a deleted task mapping", f)
	}
	current, err := c.ledger.EnvironmentForTask(ctx, req.ID.String())
	if err != nil || current.ID != req.VMID {
		t.Fatal("failed restore partially changed task environment", current.ID, err)
	}
}
