package work

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

func TestWorkIssueAdmissionPostgresRejectsChangedRequirement(t *testing.T) {
	dsn := os.Getenv("WORK_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("WORK_POSTGRES_TEST_DSN is not set")
	}
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	client := db.NewClient(db.Driver(entsql.OpenDB(dialect.Postgres, conn)))
	t.Cleanup(func() { client.Close() })
	ctx := context.Background()
	// Connection-local tables isolate this test from all application data.
	if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE project_issues(id uuid PRIMARY KEY,project_id uuid,deleted_at timestamptz)`); err != nil {
		t.Fatal(err)
	}
	project, issue := uuid.New(), uuid.New()
	for _, scenario := range []string{"unchanged", "moved", "deleted", "removed"} {
		t.Run(scenario, func(t *testing.T) {
			if _, err := conn.ExecContext(ctx, `INSERT INTO project_issues VALUES($1,$2,NULL) ON CONFLICT(id) DO UPDATE SET project_id=$2,deleted_at=NULL`, issue, project); err != nil {
				t.Fatal(err)
			}
			if err := validateIssue(ctx, client.ProjectIssue.Query(), project, issue, false); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "moved":
				_, err = conn.ExecContext(ctx, `UPDATE project_issues SET project_id=$2 WHERE id=$1`, issue, uuid.New())
			case "deleted":
				_, err = conn.ExecContext(ctx, `UPDATE project_issues SET deleted_at=now() WHERE id=$1`, issue)
			case "removed":
				_, err = conn.ExecContext(ctx, `DELETE FROM project_issues WHERE id=$1`, issue)
			}
			if err != nil {
				t.Fatal(err)
			}
			tx, err := client.Tx(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = validateIssue(ctx, tx.ProjectIssue.Query(), project, issue, true)
			if scenario == "unchanged" {
				if err != nil {
					t.Fatalf("valid requirement rejected during admission: %v", err)
				}
			} else if !errors.Is(err, errcode.ErrBadRequest) {
				t.Fatalf("changed requirement admitted: %v", err)
			}
		})
	}
}

func TestWorkIssueAdmissionPostgresLocksRequirementUntilCommit(t *testing.T) {
	dsn := os.Getenv("WORK_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("WORK_POSTGRES_TEST_DSN is not set")
	}
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	client := db.NewClient(db.Driver(entsql.OpenDB(dialect.Postgres, conn)))
	t.Cleanup(func() { client.Close() })
	ctx := context.Background()
	// A unique test schema lets a second connection test row locking without
	// reading or modifying any application's persistent tables.
	schema := "work_issue_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := conn.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
	})
	if _, err := conn.ExecContext(ctx, `SET search_path TO `+schema+`; CREATE TABLE project_issues(id uuid PRIMARY KEY,project_id uuid,deleted_at timestamptz)`); err != nil {
		t.Fatal(err)
	}
	project, issue := uuid.New(), uuid.New()
	if _, err := conn.ExecContext(ctx, `INSERT INTO project_issues VALUES($1,$2,NULL)`, issue, project); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	if _, err := writer.ExecContext(ctx, `SET lock_timeout='100ms'`); err != nil {
		t.Fatal(err)
	}
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := validateIssue(ctx, tx.ProjectIssue.Query(), project, issue, true); err != nil {
		t.Fatal(err)
	}
	_, err = writer.ExecContext(ctx, `UPDATE `+schema+`.project_issues SET deleted_at=now() WHERE id=$1`, issue)
	var postgresError *pq.Error
	if !errors.As(err, &postgresError) || postgresError.Code != "55P03" {
		t.Fatalf("requirement deletion was not blocked until admission commit: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `UPDATE `+schema+`.project_issues SET deleted_at=now() WHERE id=$1`, issue); err != nil {
		t.Fatalf("requirement remained locked after admission commit: %v", err)
	}
	if err := validateIssue(ctx, client.ProjectIssue.Query(), project, issue, false); !errors.Is(err, errcode.ErrBadRequest) {
		t.Fatalf("subsequent creation accepted deleted requirement: %v", err)
	}
}
