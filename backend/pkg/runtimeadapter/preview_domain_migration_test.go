package runtimeadapter

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func runPreviewDomainMigration(t *testing.T, l *Ledger, direction string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "migration", "000054_domain_previews."+direction+".sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(string(b)); err != nil {
		t.Fatal(err)
	}
}

func TestDomainPreviewMigrationRetainsPoliciesAndRemovesPortPool(t *testing.T) {
	l := testLedger(t)
	runPreviewDomainMigration(t, l, "down")
	ctx := context.Background()
	env := "agent_" + uuid.NewString()
	if err := l.SaveEnvironment(ctx, Environment{ID: env, OwnerID: uuid.NewString(), NodeID: "node", Request: taskflow.CreateVirtualMachineReq{ID: env}}); err != nil {
		t.Fatal(err)
	}
	task := uuid.New()
	if _, err := l.db.Exec(`INSERT INTO tasks(id,status) VALUES($1,'running')`, task); err != nil {
		t.Fatal(err)
	}
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	if _, err := l.db.Exec(`INSERT INTO runtime_port_forwards(id,environment_id,task_id,port,whitelist,public_port,active) VALUES
($1,$4,$5,8000,'["192.0.2.31"]',31000,true),
($2,$4,$5,8001,'[]',31001,true),
($3,$4,$5,8002,'["192.0.2.32"]',31002,false)`, ids[0], ids[1], ids[2], env, task); err != nil {
		t.Fatal(err)
	}
	runPreviewDomainMigration(t, l, "up")
	for index, id := range ids {
		var valid bool
		whitelist := []string{`["192.0.2.31"]`, `[]`, `["192.0.2.32"]`}[index]
		if err := l.db.QueryRow(`SELECT task_id=$2 AND environment_id=$3 AND port=$4 AND whitelist=$5::jsonb
AND active=$6 AND access_mode='ip_allowlist' FROM runtime_port_forwards WHERE id=$1`, id, task, env, 8000+index, whitelist, index != 2).Scan(&valid); err != nil || !valid {
			t.Fatal("migration changed route identity or access policy", index, valid, err)
		}
	}
	var removed bool
	if err := l.db.QueryRow(`SELECT NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema()
AND table_name='runtime_port_forwards' AND column_name IN ('public_port','released_at'))
AND NOT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='runtime_preview_client_ips')`).Scan(&removed); err != nil || !removed {
		t.Fatal("external port or observed-IP storage remains", removed, err)
	}
	newID := uuid.New()
	if _, err := l.db.Exec(`INSERT INTO runtime_port_forwards(id,environment_id,task_id,port) VALUES($1,$2,$3,8003)`, newID, env, task); err != nil {
		t.Fatal(err)
	}
	var public bool
	if err := l.db.QueryRow(`SELECT access_mode='public' AND whitelist='[]'::jsonb AND active FROM runtime_port_forwards WHERE id=$1`, newID).Scan(&public); err != nil || !public {
		t.Fatal("new route does not default to public", public, err)
	}
	if _, err := l.db.Exec(`UPDATE runtime_port_forwards SET access_mode='unsupported' WHERE id=$1`, newID); err == nil {
		t.Fatal("invalid access policy accepted")
	}
	if _, err := l.db.Exec(`INSERT INTO runtime_port_forwards(id,environment_id,task_id,port) VALUES($1,$2,$3,8003)`, uuid.New(), env, task); err == nil {
		t.Fatal("duplicate task/port accepted")
	}
}

func TestDomainPreviewDeletionRevokesWithoutReassigningRoutes(t *testing.T) {
	for _, deletion := range []string{"UPDATE tasks SET deleted_at=now() WHERE id=$1", "DELETE FROM tasks WHERE id=$1"} {
		t.Run(deletion, func(t *testing.T) {
			l := testLedger(t)
			env, task, id := "agent_"+uuid.NewString(), uuid.New(), uuid.New()
			if err := l.SaveEnvironment(context.Background(), Environment{ID: env, OwnerID: uuid.NewString(), NodeID: "node", State: "online", Request: taskflow.CreateVirtualMachineReq{ID: env}}); err != nil {
				t.Fatal(err)
			}
			if _, err := l.db.Exec(`INSERT INTO tasks(id,status) VALUES($1,'running')`, task); err != nil {
				t.Fatal(err)
			}
			if _, err := l.db.Exec(`INSERT INTO runtime_port_forwards(id,environment_id,task_id,port) VALUES($1,$2,$3,8000)`, id, env, task); err != nil {
				t.Fatal(err)
			}
			if _, err := l.db.Exec(`UPDATE runtime_environments SET state='deleted' WHERE id=$1`, env); err != nil {
				t.Fatal(err)
			}
			var preserved bool
			if err := l.db.QueryRow(`SELECT active AND revision=2 AND access_mode='public' FROM runtime_port_forwards WHERE id=$1`, id).Scan(&preserved); err != nil || !preserved {
				t.Fatal("workspace deletion changed saved access policy", preserved, err)
			}
			if _, err := l.db.Exec(deletion, task); err != nil {
				t.Fatal(err)
			}
			var revoked bool
			if err := l.db.QueryRow(`SELECT NOT active AND revision=3 AND task_id=$2 AND environment_id=$3
AND access_mode='public' FROM runtime_port_forwards WHERE id=$1`, id, task, env).Scan(&revoked); err != nil || !revoked {
				t.Fatal("task deletion did not revoke and retain route identity", revoked, err)
			}
		})
	}
}
