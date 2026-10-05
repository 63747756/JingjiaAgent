package repo

import (
	"context"
	"testing"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db/enttest"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/google/uuid"
)

func TestCreateModelSwitchRetryPreservesAuditAndRejectsConflicts(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:model-switch-idempotency?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	owner, taskID, modelID := uuid.New(), uuid.New(), uuid.New()
	if _, err := client.User.Create().SetID(owner).SetName("fixture").SetRole(consts.UserRoleIndividual).SetStatus(consts.UserStatusActive).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Model.Create().SetID(modelID).SetUserID(owner).SetProvider("OpenAI").SetModel("fixture").SetAPIKey("fixture-key").SetBaseURL("https://model.example").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Task.Create().SetID(taskID).SetUserID(owner).SetKind(consts.TaskTypeDevelop).SetContent("fixture").SetStatus(consts.TaskStatusProcessing).Save(ctx); err != nil {
		t.Fatal(err)
	}
	repo := &TaskRepo{db: client}
	item := &domain.TaskModelSwitch{ID: uuid.New(), TaskID: taskID, UserID: owner, ToModelID: modelID, RequestID: "same-request", LoadSession: true}
	if err := repo.CreateModelSwitch(ctx, item); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishModelSwitch(ctx, item.ID, true, "restarted", "original-session"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateModelSwitch(ctx, item); err != nil {
		t.Fatalf("identical retry rejected: %v", err)
	}
	// A late HTTP timeout cannot overwrite worker success.
	if err := repo.FinishModelSwitch(ctx, item.ID, false, "HTTP timeout", ""); err != nil {
		t.Fatal(err)
	}
	saved, err := client.TaskModelSwitch.Get(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Success == nil || !*saved.Success || saved.SessionID != "original-session" || saved.Message != "restarted" {
		t.Fatalf("retry rewrote terminal audit: %+v", saved)
	}
	conflicting := *item
	conflicting.LoadSession = false
	if err := repo.CreateModelSwitch(ctx, &conflicting); err == nil {
		t.Fatal("same ID accepted conflicting session reset")
	}
	if count, err := client.TaskModelSwitch.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("retry duplicated audit: %d, %v", count, err)
	}
}
