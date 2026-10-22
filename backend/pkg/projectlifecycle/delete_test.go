package projectlifecycle

import (
	"context"
	"database/sql"
	"errors"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"net/url"
	"os"
	"testing"
	"time"
)

func fixture(t *testing.T) (*sql.DB, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("WORK_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "navigation_" + uuid.NewString()
	quoted := `"` + schema + `"`
	if _, err = admin.Exec(`CREATE SCHEMA ` + quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(`DROP SCHEMA ` + quoted + ` CASCADE`); admin.Close() })
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", quoted)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE users(id uuid PRIMARY KEY);CREATE TABLE teams(id uuid PRIMARY KEY,deleted_at timestamptz);
 CREATE TABLE team_members(team_id uuid,user_id uuid);
 CREATE TABLE projects(id uuid PRIMARY KEY,user_id uuid,team_id uuid,deleted_at timestamptz,updated_at timestamptz);
 CREATE TABLE project_collaborators(project_id uuid,user_id uuid,deleted_at timestamptz);
 CREATE TABLE tasks(id uuid PRIMARY KEY,user_id uuid,status text,deleted_at timestamptz);
 CREATE TABLE project_tasks(task_id uuid,project_id uuid);
 CREATE TABLE work_tasks(task_id uuid,owner_id uuid,project_id uuid,resource_manifest jsonb DEFAULT '[]');
 CREATE TABLE runtime_task_intents(task_id uuid);
 CREATE TABLE runtime_commands(task_id uuid,state text);
 CREATE TABLE runtime_restores(task_id uuid,state text);
 CREATE TABLE work_storage_jobs(task_id uuid,state text);
 CREATE TABLE work_resources(id uuid,owner_id uuid,project_id uuid,deleted_at timestamptz);
 CREATE TABLE work_resource_versions(id uuid,resource_id uuid);
 CREATE TABLE work_artifacts(task_id uuid,project_id uuid);
 CREATE TABLE work_artifact_publications(project_id uuid);
 CREATE TABLE project_issues(id uuid,project_id uuid,deleted_at timestamptz);
 CREATE TABLE project_issue_comments(issue_id uuid,deleted_at timestamptz);
 CREATE TABLE project_git_bots(project_id uuid);`)
	migration, err := os.ReadFile("../../migration/000043_navigation_actions.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(migration))
	owner, member, pid, tid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO users VALUES($1),($2);`, owner, member)
	exec(`INSERT INTO projects(id,user_id) VALUES($1,$2)`, pid, owner)
	exec(`INSERT INTO project_collaborators(project_id,user_id) VALUES($1,$2)`, pid, member)
	exec(`INSERT INTO tasks VALUES($1,$2,'finished',NULL)`, tid, member)
	exec(`INSERT INTO project_tasks VALUES($1,$2)`, tid, pid)
	exec(`INSERT INTO work_tasks(task_id,owner_id,project_id) VALUES($1,$2,$3)`, tid, member, pid)
	unification, err := os.ReadFile("../../migration/000046_unified_task_project.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(unification))
	return db, owner, member, pid, tid
}

func TestDeletePreservesPrivateTasksAndOnlyReferencedVersions(t *testing.T) {
	db, owner, member, pid, tid := fixture(t)
	ctx := context.Background()
	if err := Delete(ctx, db, member, pid); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatalf("member deletion allowed: %v", err)
	}
	rid, v1, v2 := uuid.New(), uuid.New(), uuid.New()
	for _, q := range []struct {
		s string
		a []any
	}{
		{`INSERT INTO work_resources VALUES($1,$2,$3,NULL)`, []any{rid, owner, pid}},
		{`INSERT INTO work_resource_versions VALUES($1,$3),($2,$3)`, []any{v1, v2, rid}},
		{`UPDATE work_tasks SET resource_manifest=jsonb_build_array(jsonb_build_object('resource_id',$2::text,'version_id',$3::text)) WHERE task_id=$1`, []any{tid, rid, v1}},
		{`INSERT INTO runtime_commands VALUES($1,'waiting')`, []any{tid}},
	} {
		if _, err := db.Exec(q.s, q.a...); err != nil {
			t.Fatal(err)
		}
	}
	if err := Delete(ctx, db, owner, pid); !errors.Is(err, errcode.ErrProjectBusy) {
		t.Fatalf("queued message did not block: %v", err)
	}
	db.Exec(`UPDATE runtime_commands SET state='complete'`)
	// A sleeping, resumable task still has the legacy processing status.
	// Only its actual active commands should block deleting the project.
	if _, err := db.Exec(`INSERT INTO runtime_task_intents(task_id) VALUES($1)`, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET status='processing' WHERE id=$1`, tid); err != nil {
		t.Fatal(err)
	}
	if err := Delete(ctx, db, owner, pid); err != nil {
		t.Fatal(err)
	}
	var valid bool
	if err := db.QueryRow(`SELECT pt.project_id IS NULL AND t.deleted_at IS NULL AND w.retained_resource_versions=jsonb_build_array($2::text) FROM work_tasks w JOIN tasks t ON t.id=w.task_id JOIN project_tasks pt ON pt.task_id=t.id WHERE t.id=$1`, tid, v1).Scan(&valid); err != nil || !valid {
		t.Fatalf("private task/version not retained: %v %v", valid, err)
	}
	if err := db.QueryRow(`SELECT project_id IS NULL AND owner_id=$2 FROM work_resources WHERE id=$1`, rid, owner).Scan(&valid); err != nil || !valid {
		t.Fatalf("source owner lost file: %v %v", valid, err)
	}
	if err := Delete(ctx, db, owner, pid); err != nil {
		t.Fatalf("retry not idempotent: %v", err)
	}
}

func TestProjectFenceRejectsCreationAfterConcurrentDeletion(t *testing.T) {
	db, owner, _, pid, _ := fixture(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := db.Exec(`INSERT INTO tasks VALUES($1,$2,'pending',NULL)`, id, owner); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`SELECT id FROM projects WHERE id=$1 FOR UPDATE`, pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := db.ExecContext(ctx, `INSERT INTO project_tasks VALUES($1,$2)`, id, pid); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("creation escaped project lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err = tx.Exec(`UPDATE projects SET deleted_at=now() WHERE id=$1`, pid); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("created task in deleted project")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("creation remained blocked")
	}
}
