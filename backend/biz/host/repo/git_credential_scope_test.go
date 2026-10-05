package repo

import (
	"context"
	"testing"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db/enttest"
)

func TestGitCredentialLookupCarriesActualTaskUserForAccessChecks(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:host-git-credential-task-user?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	owner, member, modelID, imageID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, uid := range []uuid.UUID{owner, member} {
		if _, err := client.User.Create().SetID(uid).SetName(uid.String()).SetRole(consts.UserRoleIndividual).SetStatus(consts.UserStatusActive).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	gi, err := client.GitIdentity.Create().SetID(uuid.New()).SetUserID(owner).SetPlatform(consts.GitPlatformGitLab).SetAccessToken("fixture").SetUsername("git-owner").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Model.Create().SetID(modelID).SetUserID(owner).SetProvider("test").SetModel("test").SetBaseURL("https://example.invalid").SetAPIKey("fixture").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Image.Create().SetID(imageID).SetUserID(owner).SetName("fixture").Save(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := client.Project.Create().SetID(uuid.New()).SetUserID(owner).SetName("fixture").SetGitIdentityID(gi.ID).SetPlatform(consts.GitPlatformGitLab).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := &HostRepo{db: client}
	for _, shared := range []bool{false, true} {
		taskUser := owner
		if shared {
			taskUser = member
		}
		task, err := client.Task.Create().SetID(uuid.New()).SetUserID(taskUser).SetKind(consts.TaskTypeDevelop).SetContent("fixture").SetStatus(consts.TaskStatusProcessing).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		create := client.ProjectTask.Create().SetID(uuid.New()).SetTaskID(task.ID).SetImageID(imageID).SetModelID(modelID).SetGitIdentityID(gi.ID).SetCliName(consts.CliNameOpencode)
		if shared {
			create.SetProjectID(p.ID)
		}
		if _, err := create.Save(ctx); err != nil {
			t.Fatal(err)
		}
		info, err := r.GetGitCredentialByTask(ctx, task.ID.String())
		if err != nil || info.UserID != taskUser || info.GitIdentityID != gi.ID || info.GitUsername != "git-owner" {
			t.Fatalf("credential lookup lost actual task principal: %v %v", info, err)
		}
		if shared && info.ProjectID != p.ID {
			t.Fatal("shared task lost its project scope")
		}
	}
}
