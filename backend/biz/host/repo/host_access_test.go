package repo

import (
	"context"
	"errors"
	"testing"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db/enttest"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

func TestVMInfoMasksInaccessibleAndMissingIDs(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:vm-info-access?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	owner, outsider := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{owner, outsider} {
		if _, err := client.User.Create().SetID(id).SetName("fixture").SetRole(consts.UserRoleIndividual).SetStatus(consts.UserStatusActive).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Host.Create().SetID("fixture-host").SetUserID(owner).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.VirtualMachine.Create().SetID("fixture-vm").SetHostID("fixture-host").SetUserID(owner).SetName("private VM").Save(ctx); err != nil {
		t.Fatal(err)
	}
	repo := &HostRepo{db: client}
	if vm, err := repo.GetVirtualMachineWithUser(ctx, owner, "fixture-vm"); err != nil || vm.ID != "fixture-vm" {
		t.Fatalf("owner VM access failed: %v", err)
	}
	for _, id := range []string{"fixture-vm", "missing-vm"} {
		if vm, err := repo.GetVirtualMachineWithUser(ctx, outsider, id); vm != nil || !errors.Is(err, errcode.ErrNotFound) {
			t.Fatalf("inaccessible/missing VM response differs: %v", err)
		}
	}
}
