package work

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

// The optional integration test uses connection-local temp tables. It never
// inserts into or changes the configured application's persistent tables.
func TestWorkPostgresFixedVersionAndRevocation(t *testing.T) {
	dsn := os.Getenv("WORK_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("WORK_POSTGRES_TEST_DSN is not set")
	}
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	ctx := context.Background()
	ddl := `CREATE TEMP TABLE tasks(id uuid,deleted_at timestamptz);
CREATE TEMP TABLE project_tasks(task_id uuid PRIMARY KEY,project_id uuid);
CREATE TEMP TABLE work_tasks(task_id uuid,owner_id uuid,team_id uuid,skill_manifest jsonb,resource_manifest jsonb,retained_resource_versions jsonb NOT NULL DEFAULT '[]');
CREATE TEMP TABLE team_members(team_id uuid,user_id uuid);
CREATE TEMP TABLE teams(id uuid,deleted_at timestamptz);
CREATE TEMP TABLE projects(id uuid,user_id uuid,team_id uuid,deleted_at timestamptz);
CREATE TEMP TABLE project_collaborators(project_id uuid,user_id uuid,role text,deleted_at timestamptz);
CREATE TEMP TABLE agent_skills(id uuid,scope_type text,scope_id text,is_deleted bool,enabled bool,publication_state text,active_version_id uuid);
CREATE TEMP TABLE agent_skill_versions(id uuid,resource_id uuid);
CREATE TEMP TABLE agent_skill_group_bindings(skill_id uuid,group_id uuid);
CREATE TEMP TABLE team_groups(id uuid,team_id uuid,deleted_at timestamptz);
CREATE TEMP TABLE team_group_members(group_id uuid,user_id uuid);
CREATE TEMP TABLE work_resources(id uuid,owner_id uuid,project_id uuid,deleted_at timestamptz);
CREATE TEMP TABLE work_resource_versions(id uuid,resource_id uuid);`
	if _, err = conn.ExecContext(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	uid, team, tid, sid, sv, rid, rv := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO tasks VALUES($1,NULL)`, tid)
	exec(`INSERT INTO project_tasks VALUES($1,NULL)`, tid)
	exec(`INSERT INTO teams VALUES($1,NULL)`, team)
	exec(`INSERT INTO team_members VALUES($1,$2)`, team, uid)
	exec(`INSERT INTO agent_skills VALUES($1,'user',$2,false,true,'draft',$3)`, sid, uid.String(), sv)
	exec(`INSERT INTO agent_skill_versions VALUES($1,$2)`, sv, sid)
	exec(`INSERT INTO work_resources VALUES($1,$2,NULL,NULL)`, rid, uid)
	exec(`INSERT INTO work_resource_versions VALUES($1,$2)`, rv, rid)
	skills, _ := json.Marshal([]map[string]any{{"skill_id": sid, "version_id": sv}})
	resources, _ := json.Marshal([]map[string]any{{"resource_id": rid, "version_id": rv}})
	exec(`INSERT INTO work_tasks(task_id,owner_id,team_id,skill_manifest,resource_manifest) VALUES($1,$2,$3,$4,$5)`, tid, uid, team, string(skills), string(resources))
	g := &AccessGuard{store: &Repository{db: conn}}
	if err = g.CheckWorkTaskAccess(ctx, uid, tid, true); err != nil {
		t.Fatalf("valid fixed version: %v", err)
	}
	project, projectOwner := uuid.New(), uuid.New()
	exec(`INSERT INTO projects VALUES($1,$2,$3,NULL)`, project, projectOwner, team)
	exec(`UPDATE project_tasks SET project_id=$2 WHERE task_id=$1`, tid, project)
	if err = g.CheckWorkTaskAccess(ctx, uid, tid, false); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatalf("canonical project membership not checked: %v", err)
	}
	exec(`INSERT INTO project_collaborators VALUES($1,$2,'read',NULL)`, project, uid)
	if err = g.CheckWorkTaskAccess(ctx, uid, tid, false); err != nil {
		t.Fatal(err)
	}
	if err = g.CheckWorkTaskAccess(ctx, uid, tid, true); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatalf("read-only collaborator can continue: %v", err)
	}
	exec(`UPDATE project_collaborators SET role='read_write' WHERE user_id=$1`, uid)
	if err = g.CheckWorkTaskAccess(ctx, uid, tid, true); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE project_collaborators SET deleted_at=now() WHERE user_id=$1`, uid)
	if err = g.CheckWorkTaskAccess(ctx, uid, tid, false); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatal("revoked collaborator can read task", err)
	}
	exec(`UPDATE project_tasks SET project_id=NULL WHERE task_id=$1`, tid)
	exec(`UPDATE agent_skills SET active_version_id=$2 WHERE id=$1`, sid, uuid.New())
	if err = g.CheckWorkTaskAccess(ctx, uid, tid, true); err != nil {
		t.Fatalf("new active version revoked valid older pin: %v", err)
	}
	if err = g.CheckWorkTaskAccess(ctx, uuid.New(), tid, false); !errors.Is(err, errcode.ErrNotFound) {
		t.Fatalf("foreign reader: %v", err)
	}
	exec(`UPDATE work_resources SET deleted_at=now() WHERE id=$1`, rid)
	if err = g.CheckWorkTaskAccess(ctx, uid, tid, true); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatalf("deleted source did not revoke continuation: %v", err)
	}
	exec(`UPDATE work_resources SET deleted_at=NULL WHERE id=$1`, rid)
	exec(`UPDATE agent_skills SET enabled=false WHERE id=$1`, sid)
	if err = g.CheckWorkTaskAccess(ctx, uid, tid, false); err != nil {
		t.Fatalf("disabled skill blocked history: %v", err)
	}
	exec(`UPDATE agent_skills SET enabled=true WHERE id=$1`, sid)
	exec(`DELETE FROM team_members WHERE user_id=$1`, uid)
	if err = g.CheckWorkTaskAccess(ctx, uid, tid, false); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatalf("removed team access: %v", err)
	}
	exec(`INSERT INTO team_members VALUES($1,$2)`, team, uid)
	exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, tid)
	if err = g.CheckWorkTaskAccess(ctx, uid, tid, false); !errors.Is(err, errcode.ErrNotFound) {
		t.Fatalf("deleted work task passed as legacy: %v", err)
	}
}
