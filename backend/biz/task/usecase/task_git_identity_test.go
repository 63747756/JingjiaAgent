package usecase

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/redis/go-redis/v9"
	"github.com/samber/do"

	gitrepo "github.com/chaitin/MonkeyCode/backend/biz/git/repo"
	gituc "github.com/chaitin/MonkeyCode/backend/biz/git/usecase"
	projectrepo "github.com/chaitin/MonkeyCode/backend/biz/project/repo"
	taskrepo "github.com/chaitin/MonkeyCode/backend/biz/task/repo"
	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/db/enttest"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/errcode"
	"github.com/chaitin/MonkeyCode/backend/pkg/lifecycle"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
)

type taskGitCaptureVM struct {
	*taskPreinsertVMCreateStub
	git taskflow.Git
}

func (v *taskGitCaptureVM) Create(ctx context.Context, req *taskflow.CreateVirtualMachineReq) (*taskflow.VirtualMachine, error) {
	v.git = req.Git
	return v.taskPreinsertVMCreateStub.Create(ctx, req)
}

func TestTaskCreationScopesGitIdentityAndRetainsProjectCollaborator(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:task-git-identity-access?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	owner, member, outsider := uuid.New(), uuid.New(), uuid.New()
	for _, uid := range []uuid.UUID{owner, member, outsider} {
		if _, err := client.User.Create().SetID(uid).SetName(uid.String()).SetRole(consts.UserRoleIndividual).SetStatus(consts.UserStatusActive).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Host.Create().SetID("git-test-host").SetUserID(member).Save(ctx); err != nil {
		t.Fatal(err)
	}
	modelID, imageID := uuid.New(), uuid.New()
	if _, err := client.Model.Create().SetID(modelID).SetUserID(member).SetProvider("OpenAI").SetAPIKey("model-test-secret").SetBaseURL("https://model.example").SetModel("test-model").SetInterfaceType("openai_chat").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Image.Create().SetID(imageID).SetUserID(member).SetName("test-image").Save(ctx); err != nil {
		t.Fatal(err)
	}
	identities := make(map[uuid.UUID]*db.GitIdentity)
	for _, uid := range []uuid.UUID{owner, member} {
		gi, err := client.GitIdentity.Create().SetID(uuid.New()).SetUserID(uid).SetPlatform(consts.GitPlatformGitLab).SetUsername("fixture-user").SetEmail("git@example.invalid").SetAccessToken("git-pat-" + uid.String()).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		identities[uid] = gi
	}
	p, err := client.Project.Create().SetID(uuid.New()).SetUserID(owner).SetName("shared source").SetRepoURL("https://git.example/shared.git").SetGitIdentityID(identities[owner].ID).SetPlatform(consts.GitPlatformGitLab).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ProjectCollaborator.Create().SetID(uuid.New()).SetProjectID(p.ID).SetUserID(member).SetRole(consts.ProjectCollaboratorRoleReadWrite).Save(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.LLMProxy.BaseURL = "https://model-proxy.example"
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	i := do.New()
	do.ProvideValue(i, cfg)
	do.ProvideValue(i, client)
	do.ProvideValue(i, logger)
	gr, err := gitrepo.NewGitIdentityRepo(i)
	if err != nil {
		t.Fatal(err)
	}
	do.ProvideValue[domain.GitIdentityRepo](i, gr)
	tp, err := gituc.NewTokenProvider(i)
	if err != nil {
		t.Fatal(err)
	}
	pr, err := projectrepo.NewProjectRepo(i)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := taskrepo.NewTaskRepo(i)
	if err != nil {
		t.Fatal(err)
	}
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	vm := &taskGitCaptureVM{taskPreinsertVMCreateStub: &taskPreinsertVMCreateStub{db: client}}
	u := &TaskUsecase{cfg: cfg, logger: logger, repo: tr, projectRepo: pr, tokenProvider: tp,
		taskflow: &taskPreinsertTaskflowStub{vm: vm}, redis: rdb, dbClient: client,
		taskLifecycle:         lifecycle.NewManager[uuid.UUID, consts.TaskStatus, lifecycle.TaskMetadata](rdb, lifecycle.WithTransitions[uuid.UUID, consts.TaskStatus, lifecycle.TaskMetadata](lifecycle.TaskTransitions())),
		vmLifecycle:           lifecycle.NewManager[string, lifecycle.VMState, lifecycle.VMMetadata](rdb, lifecycle.WithTransitions[string, lifecycle.VMState, lifecycle.VMMetadata](lifecycle.VMTransitions())),
		taskActivityRefresher: noopTaskActivityRefresher{}, idleRefresher: noopVMIdleRefresher{}}
	req := domain.CreateTaskReq{Content: "git access test", HostID: "git-test-host", ModelID: modelID.String(), ImageID: imageID,
		CliName: consts.CliNameOpencode, Type: consts.TaskTypeDevelop, Resource: &domain.VMResource{Core: 1, Memory: 1 << 30},
		RepoReq: domain.TaskRepoReq{RepoURL: "https://git.example/personal.git", Branch: "main"}}
	// Same identity is deliberately requested directly and via an authorized project.
	req.GitIdentityID = identities[owner].ID
	if _, err := u.Create(ctx, &domain.User{ID: member}, req); !errors.Is(err, errcode.ErrNotFound) {
		t.Fatalf("foreign direct identity: %v", err)
	}
	if count, _ := client.Task.Query().Count(ctx); count != 0 {
		t.Fatal("denied credential created a business task")
	}
	if count, _ := client.VirtualMachine.Query().Count(ctx); count != 0 || vm.seenID != "" {
		t.Fatal("denied credential created a VM or reached the runtime")
	}
	req.Extra.ProjectID = p.ID
	if _, err := u.Create(ctx, &domain.User{ID: outsider}, req); !db.IsNotFound(err) {
		t.Fatalf("non-collaborator project: %v", err)
	}
	req.GitIdentityID = uuid.New() // The project, not this arbitrary ID, selects its credential.
	created, err := u.Create(ctx, &domain.User{ID: member}, req)
	if err != nil || created == nil {
		t.Fatalf("collaborator creation: %v", err)
	}
	if vm.git.Token != identities[owner].AccessToken || vm.git.URL != p.RepoURL || vm.git.Username != "fixture-user" {
		t.Fatal("shared project lost its authorized Git configuration")
	}
	// A malformed historical binding cannot bypass direct ownership validation.
	if _, err := client.Project.UpdateOneID(p.ID).SetGitIdentityID(identities[member].ID).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Create(ctx, &domain.User{ID: member}, req); !errors.Is(err, errcode.ErrNotFound) {
		t.Fatalf("foreign historical project binding: %v", err)
	}
	req.Extra.ProjectID = uuid.Nil
	req.GitIdentityID = identities[member].ID
	if _, err := u.Create(ctx, &domain.User{ID: member}, req); err != nil {
		t.Fatalf("personal identity creation: %v", err)
	}
	if vm.git.Token != identities[member].AccessToken {
		t.Fatal("personal identity was not used")
	}
	for _, gi := range identities {
		if bytes.Contains(logs.Bytes(), []byte(gi.AccessToken)) {
			t.Fatal("task creation log retained a Git token")
		}
	}
}
