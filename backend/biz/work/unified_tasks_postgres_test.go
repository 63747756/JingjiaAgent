package work

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/google/uuid"
)

func TestUnifiedTaskQueryOwnershipGroupingAndRevocation(t *testing.T) {
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
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TEMP TABLE tasks(id uuid,user_id uuid,created_at timestamptz,deleted_at timestamptz,last_active_at timestamptz DEFAULT now());
CREATE TEMP TABLE navigation_pins(user_id uuid,kind text,target_id uuid,pinned_at timestamptz);
CREATE TEMP TABLE project_tasks(task_id uuid,project_id uuid);
CREATE TEMP TABLE work_tasks(task_id uuid,team_id uuid);
CREATE TEMP TABLE projects(id uuid,user_id uuid,team_id uuid,deleted_at timestamptz);
CREATE TEMP TABLE project_collaborators(project_id uuid,user_id uuid,deleted_at timestamptz);
CREATE TEMP TABLE teams(id uuid,deleted_at timestamptz);
CREATE TEMP TABLE team_members(team_id uuid,user_id uuid);`)
	owner, foreign, team, project := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO teams VALUES($1,NULL)`, team)
	exec(`INSERT INTO team_members VALUES($1,$2)`, team, owner)
	exec(`INSERT INTO projects VALUES($1,$2,$3,NULL)`, project, foreign, team)
	exec(`INSERT INTO project_collaborators VALUES($1,$2,NULL)`, project, owner)
	legacy, independent, projectCode, projectWork, hidden := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{legacy, independent, projectCode, projectWork} {
		exec(`INSERT INTO tasks(id,user_id,created_at,deleted_at) VALUES($1,$2,now(),NULL)`, id, owner)
	}
	exec(`INSERT INTO tasks(id,user_id,created_at,deleted_at) VALUES($1,$2,now(),NULL)`, hidden, foreign)
	exec(`INSERT INTO project_tasks VALUES($1,NULL),($2,NULL),($3,$5),($4,$5),($6,NULL)`, legacy, independent, projectCode, projectWork, project, hidden)
	exec(`INSERT INTO work_tasks VALUES($1,$3),($2,$3)`, independent, projectWork, team)
	query := func(pid *uuid.UUID, unlinked bool) map[uuid.UUID]bool {
		t.Helper()
		rows, err := conn.QueryContext(ctx, unifiedTaskQuery, owner, pid, 50, 0, unlinked)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		found := map[uuid.UUID]bool{}
		for rows.Next() {
			var id uuid.UUID
			var work bool
			if err := rows.Scan(&id, &work); err != nil {
				t.Fatal(err)
			}
			found[id] = work
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return found
	}
	all := query(nil, false)
	if len(all) != 4 || all[legacy] || !all[independent] || all[projectCode] || !all[projectWork] {
		t.Fatalf("legacy and assistant tasks not combined: %v", all)
	}
	unlinked := query(nil, true)
	if len(unlinked) != 2 || !unlinked[independent] {
		t.Fatalf("project tasks leaked into independent group: %v", unlinked)
	}
	linked := query(&project, false)
	if len(linked) != 2 || !linked[projectWork] {
		t.Fatalf("project group lost historical coding tasks: %v", linked)
	}
	exec(`UPDATE project_collaborators SET deleted_at=now() WHERE project_id=$1`, project)
	// Pins must be applied before pagination and belong to the current user.
	exec(`UPDATE tasks SET last_active_at=now()-interval '2 days' WHERE id=$1`, legacy)
	exec(`INSERT INTO navigation_pins VALUES($1,'task',$2,now())`, foreign, legacy)
	first := func() uuid.UUID {
		t.Helper()
		var id uuid.UUID
		var work bool
		if err := conn.QueryRowContext(ctx, unifiedTaskQuery, owner, nil, 1, 0, true).Scan(&id, &work); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if first() != independent {
		t.Fatal("another user's pin changed ordering")
	}
	exec(`INSERT INTO navigation_pins VALUES($1,'task',$2,now())`, owner, legacy)
	if first() != legacy {
		t.Fatal("pin was applied after pagination")
	}
	exec(`DELETE FROM navigation_pins WHERE user_id=$1`, owner)
	if first() != independent {
		t.Fatal("unpin did not restore recent ordering")
	}
	if len(query(&project, false)) != 0 {
		t.Fatal("revoked project tasks remained visible")
	}
	exec(`DELETE FROM team_members WHERE user_id=$1`, owner)
	if remaining := query(nil, false); len(remaining) != 1 {
		t.Fatalf("revoked work tasks visible: %v", remaining)
	}
	exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, legacy)
	if len(query(nil, false)) != 0 {
		t.Fatal("deleted task remained visible")
	}
}
