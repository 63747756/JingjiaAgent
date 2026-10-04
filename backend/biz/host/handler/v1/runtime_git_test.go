package v1

import (
	"context"
	"net/url"
	"testing"

	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db/enttest"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

func TestRuntimeGitCredentialRequiresBoundLiveTaskAndRepository(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:runtime-git-scope?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	owner, foreign := uuid.New(), uuid.New()
	for _, uid := range []uuid.UUID{owner, foreign} {
		client.User.Create().SetID(uid).SetName("fixture").SetRole(consts.UserRoleIndividual).SetStatus(consts.UserStatusActive).SaveX(ctx)
	}
	client.Host.Create().SetID("node").SetUserID(owner).SaveX(ctx)
	vm := client.VirtualMachine.Create().SetID("agent_" + uuid.NewString()).SetUserID(owner).SetHostID("node").SetName("fixture").SetEnvironmentID("bound").SaveX(ctx)
	m := client.Model.Create().SetID(uuid.New()).SetUserID(owner).SetProvider("fixture").SetModel("fixture").SetBaseURL("https://example.invalid").SetAPIKey("fixture").SaveX(ctx)
	i := client.Image.Create().SetID(uuid.New()).SetUserID(owner).SetName("fixture").SaveX(ctx)
	task := client.Task.Create().SetID(uuid.New()).SetUserID(owner).SetKind(consts.TaskTypeDevelop).SetContent("fixture").SetStatus(consts.TaskStatusProcessing).SaveX(ctx)
	client.TaskVirtualMachine.Create().SetID(uuid.New()).SetTaskID(task.ID).SetVirtualmachineID(vm.ID).SaveX(ctx)
	client.ProjectTask.Create().SetID(uuid.New()).SetTaskID(task.ID).SetImageID(i.ID).SetModelID(m.ID).SetCliName(consts.CliNameOpencode).SetRepoURL("https://git.example.invalid/owner/repo.git").SaveX(ctx)
	key := client.ModelApiKey.Create().SetID(uuid.New()).SetUserID(owner).SetModelID(m.ID).SetVirtualmachineID(vm.ID).SetAPIKey("fixture-task-key").SaveX(ctx)
	client.ModelApiKey.Create().SetID(uuid.New()).SetUserID(foreign).SetModelID(m.ID).SetVirtualmachineID(vm.ID).SetAPIKey("fixture-foreign-key").SaveX(ctx)
	h := &InternalHostHandler{runtimeDB: client}
	req := taskflow.GitCredentialRequest{TaskID: task.ID.String(), VMID: vm.ID, Protocol: "https", Host: "git.example.invalid", Path: "owner/repo.git"}
	if err := h.runtimeGitScope(ctx, key.APIKey, req); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"foreign-key", "foreign-task", "foreign-vm", "foreign-host", "foreign-repo", "protocol-downgrade"} {
		t.Run(mode, func(t *testing.T) {
			probe, token := req, key.APIKey
			switch mode {
			case "foreign-key":
				token = "fixture-foreign-key"
			case "foreign-task":
				probe.TaskID = uuid.NewString()
			case "foreign-vm":
				probe.VMID = "agent_" + uuid.NewString()
			case "foreign-host":
				probe.Host = "other.example.invalid"
			case "foreign-repo":
				probe.Path = "owner/other.git"
			case "protocol-downgrade":
				probe.Protocol = "http"
			}
			if h.runtimeGitScope(ctx, token, probe) == nil {
				t.Fatal("foreign credential scope accepted")
			}
		})
	}
	client.VirtualMachine.UpdateOneID(vm.ID).SetIsRecycled(true).ExecX(ctx)
	if h.runtimeGitScope(ctx, key.APIKey, req) == nil {
		t.Fatal("recycled VM credential accepted")
	}
	client.VirtualMachine.UpdateOneID(vm.ID).SetIsRecycled(false).ExecX(ctx)
	client.ModelApiKey.DeleteOneID(key.ID).ExecX(ctx)
	if h.runtimeGitScope(ctx, key.APIKey, req) == nil {
		t.Fatal("deleted key accepted")
	}
}

func TestGitCredentialTargetRejectsOtherRepositoryAndURLComponents(t *testing.T) {
	u, _ := url.Parse("https://git.example.invalid/team/repo.git")
	for _, path := range []string{"team/repo", "team/repo.git"} {
		if !gitCredentialTarget(u, taskflow.GitCredentialRequest{Protocol: "https", Host: "git.example.invalid", Path: path}) {
			t.Fatal("equivalent Git target rejected")
		}
	}
	for _, path := range []string{"team/other.git", "team/repo.git?leak=1", "team/repo.git#fragment", "team/../repo.git"} {
		if gitCredentialTarget(u, taskflow.GitCredentialRequest{Protocol: "https", Host: "git.example.invalid", Path: path}) {
			t.Fatal("ambiguous Git target accepted")
		}
	}
}
