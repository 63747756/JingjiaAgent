package repo

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db/enttest"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
)

func TestProjectCreateRejectsForeignIdentityWithoutPartialRows(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:project-identity-creation?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	owner, other := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{owner, other} {
		if _, err := client.User.Create().SetID(id).SetName(id.String()).SetRole(consts.UserRoleIndividual).SetStatus(consts.UserStatusActive).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := client.GitIdentity.Create().SetID(uuid.New()).SetUserID(owner).SetPlatform(consts.GitPlatformGitLab).SetAccessToken("fixture-pat").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := &ProjectRepo{db: client, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := &domain.CreateProjectReq{Name: "identity test", GitIdentityID: identity.ID, Platform: consts.GitPlatformGitLab, RepoURL: "https://git.example/fixture.git"}
	if _, err = r.Create(ctx, other, req); !errors.Is(err, errcode.ErrNotFound) {
		t.Fatalf("foreign creation: %v", err)
	}
	if count, err := client.Project.Query().Count(ctx); err != nil || count != 0 {
		t.Fatalf("rejected creation left projects: %d %v", count, err)
	}
	if count, err := client.ProjectCollaborator.Query().Count(ctx); err != nil || count != 0 {
		t.Fatalf("rejected creation left collaborators: %d %v", count, err)
	}
	for n := 0; n < 2; n++ {
		p, err := r.Create(ctx, owner, req)
		if err != nil || p.ID == uuid.Nil || p.GitIdentityID != identity.ID {
			t.Fatalf("owned project creation: %v %v", p, err)
		}
		collaborators, err := p.QueryCollaborators().All(ctx)
		if err != nil || len(collaborators) != 1 || collaborators[0].ID == uuid.Nil || collaborators[0].UserID != owner {
			t.Fatalf("owner collaborator missing: %v", err)
		}
	}
}
