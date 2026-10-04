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
	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/db/enttest"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/errcode"
)

func TestUserTokenAuthorizesBeforeCacheAndAfterDeletion(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:git-user-token-access?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	owner, other := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{owner, other} {
		if _, err := client.User.Create().SetID(id).SetName(id.String()).SetRole(consts.UserRoleIndividual).SetStatus(consts.UserStatusActive).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := client.GitIdentity.Create().SetID(uuid.New()).SetUserID(owner).SetPlatform(consts.GitPlatformGitLab).SetAccessToken("fixture-pat").SetUsername("fixture-owner").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := do.New()
	do.ProvideValue(i, client)
	do.ProvideValue(i, &config.Config{})
	do.ProvideValue(i, slog.New(slog.NewTextHandler(io.Discard, nil)))
	repo, err := gitrepo.NewGitIdentityRepo(i)
	if err != nil {
		t.Fatal(err)
	}
	do.ProvideValue[domain.GitIdentityRepo](i, repo)
	p, err := NewTokenProvider(i)
	if err != nil {
		t.Fatal(err)
	}
	got, token, err := p.GetTokenForUser(ctx, owner, identity.ID)
	if err != nil || got.ID != identity.ID || token != "fixture-pat" {
		t.Fatalf("owner token: identity=%v token matched=%v err=%v", got, token == "fixture-pat", err)
	}
	if _, cached := p.tokenCache.Get(identity.ID.String()); !cached {
		t.Fatal("positive request did not exercise the token cache")
	}
	for _, userID := range []uuid.UUID{other, uuid.Nil} {
		got, token, err = p.GetTokenForUser(ctx, userID, identity.ID)
		if !errors.Is(err, errcode.ErrNotFound) || got != nil || token != "" {
			t.Fatal("foreign/anonymous request reached the cached credential")
		}
	}
	if err = client.GitIdentity.DeleteOneID(identity.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	got, token, err = p.GetTokenForUser(ctx, owner, identity.ID)
	if !errors.Is(err, errcode.ErrNotFound) || got != nil || token != "" {
		t.Fatal("deleted identity still returned its cached credential")
	}
}

func TestProjectIdentityRejectsForeignOrMissingBindings(t *testing.T) {
	owner, identityID := uuid.New(), uuid.New()
	p := &db.Project{UserID: owner, GitIdentityID: identityID}
	p.Edges.GitIdentity = &db.GitIdentity{ID: identityID, UserID: owner}
	if _, err := ProjectIdentity(p); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*db.Project){
		func(p *db.Project) { p.Edges.GitIdentity.UserID = uuid.New() },
		func(p *db.Project) { p.Edges.GitIdentity.ID = uuid.New() },
		func(p *db.Project) { p.Edges.GitIdentity = nil },
		func(p *db.Project) { p.UserID = uuid.Nil },
	} {
		candidate := *p
		identity := *p.Edges.GitIdentity
		candidate.Edges.GitIdentity = &identity
		mutate(&candidate)
		if _, err := ProjectIdentity(&candidate); !errors.Is(err, errcode.ErrNotFound) {
			t.Fatal("invalid project binding granted credential access")
		}
	}
}
