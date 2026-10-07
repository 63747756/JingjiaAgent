package repo

import (
	"context"
	"testing"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db/enttest"
)

func TestPasswordResetLookupPreservesGeneralEmailBindingLookup(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:password-reset-binding-"+uuid.NewString()+"?mode=memory&cache=shared&_fk=1")
	defer client.Close()
	email := "shared@example.invalid"
	local := client.User.Create().SetID(uuid.New()).SetName("Local employee").SetEmail(email).
		SetAuthSource("local").SetRole(consts.UserRoleSubAccount).SetStatus(consts.UserStatusActive).SaveX(ctx)
	client.User.Create().SetID(uuid.New()).SetName("AD employee").SetEmail(email).
		SetAuthSource("ad").SetRole(consts.UserRoleSubAccount).SetStatus(consts.UserStatusActive).SaveX(ctx)
	client.User.Create().SetID(uuid.New()).SetName("Local team admin").SetEmail(email).
		SetAuthSource("local").SetRole(consts.UserRoleEnterprise).SetStatus(consts.UserStatusActive).SaveX(ctx)
	r := &userRepo{db: client}
	candidates, err := r.GetPasswordResetCandidates(ctx, "SHARED@EXAMPLE.INVALID")
	if err != nil || len(candidates) != 1 || candidates[0].ID != local.ID {
		t.Fatalf("recovery candidates = %v, error = %v", candidates, err)
	}
	all, err := r.GetUserByEmail(ctx, []string{email})
	if err != nil || len(all) != 3 {
		t.Fatalf("general binding lookup changed: count = %d, error = %v", len(all), err)
	}
	all, err = r.GetUserByEmail(ctx, []string{"SHARED@EXAMPLE.INVALID"})
	if err != nil || len(all) != 0 {
		t.Fatal("general binding lookup no longer preserves its exact-email behavior")
	}
}
