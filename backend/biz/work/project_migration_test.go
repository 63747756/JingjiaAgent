package work

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/google/uuid"
)

func TestUnifiedProjectMigrationPreservesAssociationsAndRejectsConflicts(t *testing.T) {
	dsn := os.Getenv("WORK_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	for _, scenario := range []string{"consistent", "mismatch", "missing base", "duplicate base"} {
		t.Run(scenario, func(t *testing.T) {
			conn, err := sql.Open("postgres", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetMaxOpenConns(1)
			schema := `"project_migration_` + uuid.NewString() + `"`
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := conn.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec("CREATE SCHEMA " + schema)
			defer conn.Exec("DROP SCHEMA " + schema + " CASCADE")
			exec("SET search_path TO " + schema)
			exec(`CREATE TABLE users(id uuid PRIMARY KEY);
CREATE TABLE projects(id uuid PRIMARY KEY,deleted_at timestamptz);
CREATE TABLE tasks(id uuid PRIMARY KEY,status text);
CREATE TABLE project_tasks(task_id uuid,project_id uuid);
CREATE TABLE work_tasks(task_id uuid PRIMARY KEY,project_id uuid,created_at timestamptz DEFAULT now(),skill_manifest jsonb DEFAULT '[{"version":"v1"}]',resource_manifest jsonb DEFAULT '[{"version_id":"fixed"}]');
CREATE TABLE runtime_task_intents(task_id uuid);`)
			load := func(file string) string {
				t.Helper()
				b, err := os.ReadFile("../../migration/" + file)
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			exec(load("000043_navigation_actions.up.sql"))
			pid, tid, independent := uuid.New(), uuid.New(), uuid.New()
			exec(`INSERT INTO projects(id) VALUES($1)`, pid)
			exec(`INSERT INTO tasks VALUES($1,'finished'),($2,'finished')`, tid, independent)
			exec(`INSERT INTO project_tasks VALUES($1,$2),($3,NULL)`, tid, pid, independent)
			exec(`INSERT INTO work_tasks(task_id,project_id) VALUES($1,$2),($3,NULL)`, tid, pid, independent)
			switch scenario {
			case "mismatch":
				exec(`UPDATE work_tasks SET project_id=NULL WHERE task_id=$1`, tid)
			case "missing base":
				exec(`DELETE FROM project_tasks WHERE task_id=$1`, tid)
			case "duplicate base":
				exec(`INSERT INTO project_tasks VALUES($1,$2)`, tid, pid)
			}
			tx, err := conn.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(load("000046_unified_task_project.up.sql"))
			if scenario != "consistent" {
				tx.Rollback()
				if err == nil {
					t.Fatal("ambiguous association was silently migrated")
				}
				var count int
				if err = conn.QueryRow(`SELECT count(project_id) FROM work_tasks`).Scan(&count); err != nil {
					t.Fatal("failed migration changed the original schema", err)
				}
				return
			}
			if err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var valid bool
			if err = conn.QueryRow(`SELECT NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='work_tasks' AND column_name='project_id') AND EXISTS(SELECT 1 FROM project_tasks WHERE task_id=$1 AND project_id=$2) AND EXISTS(SELECT 1 FROM project_tasks WHERE task_id=$3 AND project_id IS NULL) AND NOT EXISTS(SELECT 1 FROM work_tasks WHERE skill_manifest<>'[{"version":"v1"}]'::jsonb OR resource_manifest<>'[{"version_id":"fixed"}]'::jsonb)`, tid, pid, independent).Scan(&valid); err != nil || !valid {
				t.Fatalf("migration changed task data: %v", err)
			}
			if _, err = conn.Exec(`INSERT INTO project_tasks VALUES($1,NULL)`, tid); err == nil {
				t.Fatal("duplicate task association accepted")
			}
			if _, err = conn.Exec(`UPDATE project_tasks SET project_id=$2 WHERE task_id=$1`, independent, uuid.Nil); err == nil {
				t.Fatal("zero project accepted")
			}
			exec(load("000046_unified_task_project.down.sql"))
			if err = conn.QueryRow(`SELECT bool_and(w.project_id IS NOT DISTINCT FROM pt.project_id) FROM work_tasks w JOIN project_tasks pt ON pt.task_id=w.task_id`).Scan(&valid); err != nil || !valid {
				t.Fatal("rollback failed to restore associations", err)
			}
		})
	}
}
