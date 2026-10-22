package repo

import (
	"context"
	stdsql "database/sql"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/enttest"
	"github.com/63747756/jingjiaagent/backend/db/projectcollaborator"
	"github.com/63747756/jingjiaagent/backend/db/teammember"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

func workProjectFixture(t *testing.T) (*ProjectRepo, *db.Client, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	c := enttest.Open(t, "sqlite3", "file:work-project-"+uuid.NewString()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { c.Close() })
	return seedWorkProjectFixture(ctx, t, c)
}

func seedWorkProjectFixture(ctx context.Context, t *testing.T, c *db.Client) (*ProjectRepo, *db.Client, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	owner, member, team := uuid.New(), uuid.New(), uuid.New()
	c.Team.Create().SetID(team).SetName("fixture").SetMemberLimit(30).SaveX(ctx)
	for _, uid := range []uuid.UUID{owner, member} {
		c.User.Create().SetID(uid).SetName(uid.String()).SetRole(consts.UserRoleSubAccount).SetStatus(consts.UserStatusActive).SaveX(ctx)
		c.TeamMember.Create().SetID(uuid.New()).SetTeamID(team).SetUserID(uid).SetRole(consts.TeamMemberRoleUser).SaveX(ctx)
	}
	return &ProjectRepo{db: c, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, c, owner, member, team
}

func workProjectPostgresFixture(t *testing.T) (*ProjectRepo, *db.Client, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("WORK_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("WORK_POSTGRES_TEST_DSN is not set")
	}
	admin, err := stdsql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "project_settings_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP SCHEMA " + schema + " CASCADE"); admin.Close() })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	c := enttest.Open(t, "postgres", u.String())
	t.Cleanup(func() { c.Close() })
	return seedWorkProjectFixture(context.Background(), t, c)
}
func TestWorkBusinessProjectWithoutGitAndWithTeamRevocation(t *testing.T) {
	r, c, owner, member, team := workProjectFixture(t)
	ctx := context.Background()
	p, err := r.Create(ctx, owner, &domain.CreateProjectReq{Name: "业务项目", TeamID: &team, Rules: "保留原始数据"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ProjectKind != "business" || p.RepoURL != "" || p.GitIdentityID != uuid.Nil {
		t.Fatalf("business project acquired git binding: %v", p)
	}
	c.ProjectCollaborator.Create().SetID(uuid.New()).SetProjectID(p.ID).SetUserID(member).SetRole(consts.ProjectCollaboratorRoleReadOnly).SaveX(ctx)
	if _, err = r.Get(ctx, member, p.ID); err != nil {
		t.Fatal(err)
	}
	if r.HasReadWritePerm(ctx, member, p.ID) {
		t.Fatal("read-only collaborator got write access")
	}
	c.ProjectCollaborator.Update().Where(projectcollaborator.ProjectIDEQ(p.ID), projectcollaborator.UserIDEQ(member)).SetRole(consts.ProjectCollaboratorRoleReadWrite).SaveX(ctx)
	if !r.HasReadWritePerm(ctx, member, p.ID) {
		t.Fatal("writer rejected")
	}
	c.TeamMember.Delete().Where(teammember.TeamIDEQ(team), teammember.UserIDEQ(member)).ExecX(ctx)
	if _, err = r.Get(ctx, member, p.ID); !db.IsNotFound(err) {
		t.Fatalf("removed team member reads project: %v", err)
	}
	if r.HasReadWritePerm(ctx, member, p.ID) {
		t.Fatal("removed team member retained write access")
	}
	if _, err = r.Create(ctx, member, &domain.CreateProjectReq{Name: "foreign", TeamID: &team}); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatalf("create in removed team: %v", err)
	}
}
func TestWorkProjectDefaultsAndBusinessGitRejection(t *testing.T) {
	for _, tc := range []struct {
		name, url, kind, want string
		bad                   bool
	}{
		{"new", "", "", "business", false},
		{"legacy", "https://git.internal/repo.git", "", "code", false},
		{"explicit code", "", "code", "code", false},
		{"business git", "https://git.internal/repo.git", "business", "", true},
		{"invalid kind", "", "unknown", "", true},
	} {
		req := &domain.CreateProjectReq{Name: tc.name, RepoURL: tc.url, ProjectKind: tc.kind}
		err := normalizeProjectRequest(req)
		if tc.bad {
			if !errors.Is(err, errcode.ErrBadRequest) {
				t.Fatalf("%s: %v", tc.name, err)
			}
		} else if err != nil || req.ProjectKind != tc.want {
			t.Fatalf("%s: %s %v", tc.name, req.ProjectKind, err)
		}
	}
}

func TestProjectWriterCanSaveSettingsButCannotManageMembers(t *testing.T) {
	r, c, owner, writer, team := workProjectPostgresFixture(t)
	ctx := context.Background()
	p, err := r.Create(ctx, owner, &domain.CreateProjectReq{Name: "shared", TeamID: &team})
	if err != nil {
		t.Fatal(err)
	}
	c.ProjectCollaborator.Create().SetID(uuid.New()).SetProjectID(p.ID).SetUserID(writer).SetRole(consts.ProjectCollaboratorRoleReadWrite).SaveX(ctx)
	rules := "Include references"
	emptyAssistant := uuid.Nil
	req := &domain.UpdateProjectReq{ID: p.ID, Name: "updated", Rules: &rules, DefaultProfileID: &emptyAssistant}
	if _, err := r.Update(ctx, &domain.User{ID: writer}, req); err != nil {
		t.Fatalf("writer could not save ordinary settings: %v", err)
	}
	req.Name = "must not persist"
	req.Collaborator = []*domain.CreateCollaboratorItem{{UserID: writer, Permission: consts.ProjectCollaboratorRoleReadWrite}}
	if _, err := r.Update(ctx, &domain.User{ID: writer}, req); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatalf("writer modified membership: %v", err)
	}
	saved := c.Project.GetX(ctx, p.ID)
	if saved.Name != "updated" || saved.Rules != rules {
		t.Fatal("rejected membership update modified project settings")
	}
}
