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
	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db/enttest"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/errcode"
)

func TestIndependentEnvironmentRejectsForeignGitIdentityBeforePrepare(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:host-git-identity-access?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	owner, other := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{owner, other} {
		if _, err := client.User.Create().SetID(id).SetName(id.String()).SetRole(consts.UserRoleIndividual).SetStatus(consts.UserStatusActive).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	gi, err := client.GitIdentity.Create().SetID(uuid.New()).SetUserID(owner).SetPlatform(consts.GitPlatformGitLab).SetAccessToken("independent-fixture-pat").Save(ctx)
	if err != nil {
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
	tp, err := gituc.NewTokenProvider(i)
	if err != nil {
		t.Fatal(err)
	}
	// repo and VM client are intentionally absent: rejection must precede both.
	u := &HostUsecase{taskflow: &preinsertTaskflowStub{}, tokenProvider: tp}
	if _, err := u.CreateVM(ctx, &domain.User{ID: other}, &domain.CreateVMReq{HostID: "host-1", GitIdentityID: gi.ID}); !errors.Is(err, errcode.ErrNotFound) {
		t.Fatalf("foreign independent Git credential: %v", err)
	}
	if count, err := client.VirtualMachine.Query().Count(ctx); err != nil || count != 0 {
		t.Fatalf("denied credential left an environment: count=%d err=%v", count, err)
	}
}
