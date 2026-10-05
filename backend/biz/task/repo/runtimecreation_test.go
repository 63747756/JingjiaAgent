package repo

import (
	"context"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/enttest"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"testing"
)

func TestRejectedCreationCleanupRequiresOwnerAndUnadmittedVM(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:rejected-creation-scope?mode=memory&cache=shared&_fk=1")
	defer client.Close()
	owner := uuid.New()
	client.User.Create().SetID(owner).SetName("fixture").SetRole(consts.UserRoleIndividual).SetStatus(consts.UserStatusActive).SaveX(ctx)
	client.Host.Create().SetID("host").SetUserID(owner).SaveX(ctx)
	vm := client.VirtualMachine.Create().SetID("prepared").SetUserID(owner).SetHostID("host").SetName("fixture").SaveX(ctx)
	r := &TaskRepo{db: client}
	if err := r.RejectPreparedRuntimeCreation(ctx, uuid.New(), vm.ID); err == nil {
		t.Fatal("cross-owner cleanup accepted")
	}
	if err := client.VirtualMachine.UpdateOneID(vm.ID).SetEnvironmentID("admitted").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.RejectPreparedRuntimeCreation(ctx, owner, vm.ID); err == nil {
		t.Fatal("admitted environment compensated")
	}
	current, err := client.VirtualMachine.Get(ctx, vm.ID)
	if err != nil || current.IsRecycled || current.EnvironmentID != "admitted" {
		t.Fatal("rejected cleanup changed admitted VM")
	}
	if err := client.VirtualMachine.UpdateOneID(vm.ID).SetEnvironmentID("").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.RejectPreparedRuntimeCreation(ctx, owner, vm.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.VirtualMachine.Get(ctx, vm.ID); !db.IsNotFound(err) {
		t.Fatal("unadmitted VM was not removed from product queries")
	}
}
