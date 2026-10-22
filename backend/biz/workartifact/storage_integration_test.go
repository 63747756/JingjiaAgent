package workartifact

import (
	"bytes"
	"context"
	"database/sql"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/asseturl"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testStorage(t *testing.T) (*Service, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("JINGJIAAGENT_RUNTIME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "work_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); admin.Close() })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.ExecContext(ctx, `CREATE TABLE users(id uuid PRIMARY KEY); CREATE TABLE teams(id uuid PRIMARY KEY,deleted_at timestamptz); CREATE TABLE team_members(team_id uuid,user_id uuid); CREATE TABLE projects(id uuid PRIMARY KEY,user_id uuid,team_id uuid,deleted_at timestamptz); CREATE TABLE project_collaborators(project_id uuid,user_id uuid,role text,deleted_at timestamptz); CREATE TABLE tasks(id uuid PRIMARY KEY,user_id uuid,deleted_at timestamptz); CREATE TABLE runtime_environments(id text PRIMARY KEY); CREATE TABLE project_tasks(task_id uuid PRIMARY KEY,project_id uuid); CREATE TABLE work_tasks(task_id uuid PRIMARY KEY,owner_id uuid,team_id uuid,resource_manifest jsonb DEFAULT '[]',skill_manifest jsonb DEFAULT '[]',delivery_status text DEFAULT 'pending',updated_at timestamptz DEFAULT now());`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile(filepath.Join("..", "..", "migration", "000034_work_storage.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	retention, err := os.ReadFile(filepath.Join("..", "..", "migration", "000050_simplify_work_storage.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(retention)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `ALTER TABLE work_tasks ADD COLUMN retained_resource_versions jsonb NOT NULL DEFAULT '[]'`); err != nil {
		t.Fatal(err)
	}
	owner, reader, team, project, task := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{owner, reader} {
		if _, err = db.ExecContext(ctx, `INSERT INTO users(id) VALUES($1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	statements := []struct {
		query string
		args  []any
	}{{`INSERT INTO teams(id) VALUES($1)`, []any{team}}, {`INSERT INTO team_members VALUES($1,$2),($1,$3)`, []any{team, owner, reader}}, {`INSERT INTO projects(id,user_id,team_id) VALUES($1,$2,$3)`, []any{project, owner, team}}, {`INSERT INTO project_collaborators VALUES($1,$2,'read',NULL)`, []any{project, reader}}, {`INSERT INTO tasks(id,user_id) VALUES($1,$2)`, []any{task, owner}}, {`INSERT INTO project_tasks VALUES($1,$2)`, []any{task, project}}, {`INSERT INTO work_tasks(task_id,owner_id,team_id) VALUES($1,$2,$3)`, []any{task, owner, team}}}
	for _, st := range statements {
		if _, err = db.ExecContext(ctx, st.query, st.args...); err != nil {
			t.Fatal(err)
		}
	}
	return &Service{db: db, store: &memoryStore{objects: map[string][]byte{}}, cfg: &config.Config{Work: config.Work{Enabled: true}, ObjectStorage: config.ObjectStorageConfig{TempPrefix: "tmp"}}}, owner, reader, project, task
}
func TestResourceVersionsRemainFixedAndRespectRevocation(t *testing.T) {
	s, owner, reader, project, _ := testStorage(t)
	ctx := context.Background()
	store := s.store.(*memoryStore)
	key := "tmp/" + owner.String() + "_source.csv"
	store.objects[key] = []byte("x\n1\n")
	r, err := s.RegisterResource(ctx, owner, RegisterResourceRequest{URL: asseturl.Build(key), Filename: "source.csv", ProjectID: &project})
	if err != nil {
		t.Fatal(err)
	}
	first := r.CurrentVersionID
	store.objects[key] = []byte("x\n99\n")
	r, err = s.AddResourceVersion(ctx, owner, r.ID, RegisterResourceRequest{URL: asseturl.Build(key), Filename: "source.csv"})
	if err != nil {
		t.Fatal(err)
	}
	if r.CurrentVersionID == first {
		t.Fatal("resource version was overwritten")
	}
	_, pins, err := s.ResolveInputs(ctx, reader, &project, []uuid.UUID{first})
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 1 || pins[0].VersionID != first || pins[0].SHA256 == r.Versions[0].SHA256 {
		t.Fatal("selected resource version drifted")
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE project_collaborators SET deleted_at=now() WHERE user_id=$1`, reader); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ResolveInputs(ctx, reader, &project, []uuid.UUID{first}); err == nil {
		t.Fatal("revoked member retained resource access")
	}
}
func TestPublishedArtifactFreezesVersionAndSurvivesEnvironmentRemoval(t *testing.T) {
	s, owner, reader, project, task := testStorage(t)
	ctx := context.Background()
	job := storageJob{TaskID: task}
	save := func(text string) {
		f, d, n, err := spool(bytes.NewBufferString(text), 100)
		if err != nil {
			t.Fatal(err)
		}
		defer removeSpool(f)
		if err = s.saveArtifact(ctx, owner, job, "output/report.md", f, d, n); err != nil {
			t.Fatal(err)
		}
		if err = s.convertTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	save("# v1\n已确认结果\n")
	aid := uuid.NewSHA1(task, []byte("output/report.md"))
	a, err := s.Artifact(ctx, owner, aid)
	if err != nil {
		t.Fatal(err)
	}
	first := a.CurrentVersionID
	if _, err = s.Artifact(ctx, reader, aid); err == nil {
		t.Fatal("private artifact visible before publishing")
	}
	if err = s.Publish(ctx, owner, aid, project, first); err != nil {
		t.Fatal(err)
	}
	save("# v2\n作者私有修改\n")
	shared, err := s.Artifact(ctx, reader, aid)
	if err != nil {
		t.Fatal(err)
	}
	if shared.CurrentVersionID != first || len(shared.Versions) != 1 || shared.CanEdit || shared.TaskID != uuid.Nil {
		t.Fatal("private draft or task metadata leaked")
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM runtime_environments`); err != nil {
		t.Fatal(err)
	}
	r, _, _, err := s.Download(ctx, reader, aid, first, false)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if _, err = s.db.ExecContext(ctx, `DELETE FROM team_members WHERE user_id=$1`, reader); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Artifact(ctx, reader, aid); err == nil {
		t.Fatal("removed team member retained access")
	}
}

func TestRetainedResourceOnlyExposesReferencedVersionToTaskOwner(t *testing.T) {
	s, owner, reader, project, task := testStorage(t)
	ctx := context.Background()
	store := s.store.(*memoryStore)
	key := "tmp/" + owner.String() + "_retained.txt"
	store.objects[key] = []byte("first")
	r, err := s.RegisterResource(ctx, owner, RegisterResourceRequest{URL: asseturl.Build(key), Filename: "retained.txt", ProjectID: &project})
	if err != nil {
		t.Fatal(err)
	}
	first := r.CurrentVersionID
	store.objects[key] = []byte("private second")
	r, err = s.AddResourceVersion(ctx, owner, r.ID, RegisterResourceRequest{URL: asseturl.Build(key), Filename: "retained.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// The retained task belongs to a different user from the original uploader.
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE tasks SET user_id=$2 WHERE id=$1`, []any{task, reader}},
		{`UPDATE project_tasks SET project_id=NULL WHERE task_id=$1`, []any{task}},
		{`UPDATE work_tasks SET owner_id=$2,retained_resource_versions=jsonb_build_array($3::text) WHERE task_id=$1`, []any{task, reader, first}},
		{`UPDATE work_resources SET project_id=NULL WHERE id=$1`, []any{r.ID}},
		{`UPDATE projects SET deleted_at=now() WHERE id=$1`, []any{project}},
	} {
		if _, err = s.db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Resource(ctx, reader, r.ID)
	if err != nil || len(got.Versions) != 1 || got.CurrentVersionID != first {
		t.Fatalf("wrong retained versions: %+v %v", got, err)
	}
	if _, _, err = s.ResolveInputs(ctx, reader, nil, []uuid.UUID{r.CurrentVersionID}); err == nil {
		t.Fatal("unreferenced version leaked")
	}
	if _, _, err = s.ResolveInputs(ctx, reader, nil, []uuid.UUID{first}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DELETE FROM team_members WHERE user_id=$1`, reader); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resource(ctx, reader, r.ID); err == nil {
		t.Fatal("revoked team member retained access")
	}
}
