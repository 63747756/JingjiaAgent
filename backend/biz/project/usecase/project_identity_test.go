package usecase

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/samber/do"

	gitrepo "github.com/chaitin/MonkeyCode/backend/biz/git/repo"
	gituc "github.com/chaitin/MonkeyCode/backend/biz/git/usecase"
	projectrepo "github.com/chaitin/MonkeyCode/backend/biz/project/repo"
	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db/enttest"
	"github.com/chaitin/MonkeyCode/backend/db/projectcollaborator"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/errcode"
)

func TestProjectTokenKeepsCollaboratorAccessButRejectsArbitraryIdentities(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:project-token-access?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	owner, member, outsider := uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{owner, member, outsider} {
		if _, err := client.User.Create().SetID(id).SetName(id.String()).SetRole(consts.UserRoleIndividual).SetStatus(consts.UserStatusActive).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := client.GitIdentity.Create().SetID(uuid.New()).SetUserID(owner).SetPlatform(consts.GitPlatformGitLab).SetAccessToken("project-owner-pat").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, err := client.Project.Create().SetID(uuid.New()).SetUserID(owner).SetName("shared fixture").SetGitIdentityID(identity.ID).SetPlatform(consts.GitPlatformGitLab).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ProjectCollaborator.Create().SetID(uuid.New()).SetProjectID(p.ID).SetUserID(member).SetRole(consts.ProjectCollaboratorRoleReadWrite).Save(ctx); err != nil {
		t.Fatal(err)
	}
	i := do.New()
	do.ProvideValue(i, client)
	do.ProvideValue(i, &config.Config{})
	do.ProvideValue(i, slog.New(slog.NewTextHandler(io.Discard, nil)))
	gr, err := gitrepo.NewGitIdentityRepo(i)
	if err != nil {
		t.Fatal(err)
	}
	do.ProvideValue[domain.GitIdentityRepo](i, gr)
	pr, err := projectrepo.NewProjectRepo(i)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := gituc.NewTokenProvider(i)
	if err != nil {
		t.Fatal(err)
	}
	for _, useProvider := range []bool{false, true} {
		u := &ProjectUsecase{repo: pr, gitidentityRepo: gr}
		if useProvider {
			u.tokenProvider = tp
		}
		for _, uid := range []uuid.UUID{owner, member} {
			token, err := u.GetRepoToken(ctx, uid, p.ID, identity.ID, consts.GitPlatformGitLab)
			if err != nil || token != "project-owner-pat" {
				t.Fatalf("authorized project token: matched=%v err=%v", token == "project-owner-pat", err)
			}
		}
		for _, query := range [][3]uuid.UUID{{member, uuid.Nil, identity.ID}, {outsider, p.ID, identity.ID}, {member, p.ID, uuid.New()}, {uuid.Nil, uuid.Nil, identity.ID}} {
			token, err := u.GetRepoToken(ctx, query[0], query[1], query[2], consts.GitPlatformGitLab)
			if !errors.Is(err, errcode.ErrNotFound) || token != "" {
				t.Fatal("unauthorized direct/project token was returned")
			}
		}
	}
	// Existing malformed bindings also fail before a Git client/network call.
	loaded, err := pr.Get(ctx, member, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Edges.GitIdentity.UserID = outsider
	u := &ProjectUsecase{}
	if _, _, err := u.getClient(ctx, loaded); !errors.Is(err, errcode.ErrNotFound) {
		t.Fatal("foreign historical binding reached a platform client")
	}
	if token, err := u.getRepoToken(loaded); !errors.Is(err, errcode.ErrNotFound) || token != "" {
		t.Fatal("foreign historical binding returned a stored token")
	}
	if _, err := client.ProjectCollaborator.Delete().Where(projectcollaborator.ProjectID(p.ID), projectcollaborator.UserID(member)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	for _, useProvider := range []bool{false, true} {
		u := &ProjectUsecase{repo: pr, gitidentityRepo: gr}
		if useProvider {
			u.tokenProvider = tp
		}
		if token, err := u.GetRepoToken(ctx, member, p.ID, identity.ID, consts.GitPlatformGitLab); !errors.Is(err, errcode.ErrNotFound) || token != "" {
			t.Fatal("removed collaborator still received a cached/stored project credential")
		}
	}
}
