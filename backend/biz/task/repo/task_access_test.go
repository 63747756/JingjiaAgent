package repo

import (
	"context"
	"errors"
	"testing"

	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db/enttest"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/errcode"
	"github.com/google/uuid"
)

func TestTaskInfoMasksInaccessibleAndMissingIDs(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:task-info-access?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	owner, outsider, taskID := uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{owner, outsider} {
		if _, err := client.User.Create().SetID(id).SetName("fixture").SetRole(consts.UserRoleIndividual).SetStatus(consts.UserStatusActive).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Task.Create().SetID(taskID).SetUserID(owner).SetKind(consts.TaskTypeDevelop).SetContent("private task").SetStatus(consts.TaskStatusProcessing).Save(ctx); err != nil {
		t.Fatal(err)
	}
	repo := &TaskRepo{db: client}
	for _, id := range []uuid.UUID{taskID, uuid.New()} {
		if task, err := repo.Info(ctx, &domain.User{ID: outsider}, id, false); task != nil || !errors.Is(err, errcode.ErrNotFound) {
			t.Fatalf("inaccessible/missing task response differs: %v", err)
		}
	}
	for _, access := range []struct {
		User       uuid.UUID
		Privileged bool
	}{{owner, false}, {outsider, true}} {
		if task, err := repo.Info(ctx, &domain.User{ID: access.User}, taskID, access.Privileged); err != nil || task.ID != taskID {
			t.Fatalf("authorized task access failed: %v", err)
		}
	}
}
