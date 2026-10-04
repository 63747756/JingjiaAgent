package usecase

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func switchRecoveryFixture() (*TaskUsecase, *switchModelTaskRepo, *switchModelModelRepo, *switchModelTaskManager, uuid.UUID, uuid.UUID) {
	owner, taskID, modelID := uuid.New(), uuid.New(), uuid.New()
	repo := newSwitchModelTaskRepo(owner, taskID, modelID, consts.TaskStatusProcessing)
	models := &switchModelModelRepo{model: &db.Model{ID: modelID, Provider: "OpenAI", BaseURL: "https://model.example/v1", Model: "fixture", InterfaceType: string(consts.InterfaceTypeOpenAIResponse)}, runtimeKey: "fixture-key"}
	manager := &switchModelTaskManager{resp: &taskflow.RestartTaskResp{Success: true, BusinessStateCommitted: true, RequestId: "restart", Message: "restarted", SessionID: "session"}}
	uc := &TaskUsecase{repo: repo, modelRepo: models, taskflow: &switchModelTaskflow{taskMgr: manager, vm: &switchModelVM{}}, logger: slog.Default()}
	return uc, repo, models, manager, owner, taskID
}

func TestSwitchModelDurableTimeoutDoesNotFinishAudit(t *testing.T) {
	uc, repo, models, manager, owner, taskID := switchRecoveryFixture()
	manager.err = &taskflow.RestartPendingError{Err: context.DeadlineExceeded}
	manager.resp = nil
	_, err := uc.SwitchModel(context.Background(), &domain.User{ID: owner}, taskID, domain.SwitchTaskModelReq{RequestID: "restart", ModelID: models.model.ID})
	if !errors.Is(err, context.DeadlineExceeded) || !taskflow.IsRestartPending(err) {
		t.Fatalf("pending error lost: %v", err)
	}
	if repo.created == nil || repo.finishedID != uuid.Nil || repo.updatedTaskID != uuid.Nil {
		t.Fatalf("HTTP timeout finalized durable mutation: %+v", repo)
	}
	mutation := manager.restartReq.BusinessMutation
	if mutation == nil || mutation.OwnerID != owner || mutation.ModelSwitch == nil || mutation.ModelSwitch.ID != repo.created.ID || mutation.ModelSwitch.ModelID != models.model.ID {
		t.Fatalf("restart missing authorized audit metadata: %+v", mutation)
	}
}

func TestSwitchModelDurableSuccessDoesNotReapplyBusinessMutation(t *testing.T) {
	uc, repo, models, _, owner, taskID := switchRecoveryFixture()
	resp, err := uc.SwitchModel(context.Background(), &domain.User{ID: owner}, taskID, domain.SwitchTaskModelReq{RequestID: "restart", ModelID: models.model.ID})
	if err != nil || resp == nil || !resp.Success {
		t.Fatalf("restart failed: %+v, %v", resp, err)
	}
	if repo.finishedID != uuid.Nil || repo.updatedTaskID != uuid.Nil {
		t.Fatal("HTTP path reapplied worker-owned business result")
	}
}

func TestSwitchAgentResourcesCarriesDurableSelectionAndSkipsHTTPWrite(t *testing.T) {
	uc, repo, _, manager, owner, taskID := switchRecoveryFixture()
	// Empty resources exercise the explicit-clear path without external assets.
	resp, err := uc.SwitchAgentResources(context.Background(), &domain.User{ID: owner}, taskID, domain.SwitchAgentResourcesReq{RequestID: "restart"})
	if err != nil || resp == nil || !resp.Success {
		t.Fatalf("resource restart failed: %+v, %v", resp, err)
	}
	mutation := manager.restartReq.BusinessMutation
	if mutation == nil || mutation.OwnerID != owner || mutation.ResourceSelection == nil || mutation.ResourceSelection.SkillIDs == nil || mutation.ResourceSelection.PluginIDs == nil {
		t.Fatalf("clear selection not durable: %+v", mutation)
	}
	if repo.resourceUpdates != 0 {
		t.Fatal("HTTP path rewrote durable selection")
	}
}

type resumableTaskManager struct {
	*switchModelTaskManager
	found     bool
	resumeErr error
	resumeReq taskflow.RestartTaskReq
}

func (m *resumableTaskManager) ResumeRestart(_ context.Context, req taskflow.RestartTaskReq) (*taskflow.RestartTaskResp, bool, error) {
	m.resumeReq = req
	return m.resp, m.found, m.resumeErr
}

func TestSwitchRetriesResumeBeforeChangingCredentialsOrPreparing(t *testing.T) {
	for _, kind := range []string{"model", "resources"} {
		for _, pending := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "-complete", true: "-pending"}[pending], func(t *testing.T) {
				uc, repo, models, manager, owner, taskID := switchRecoveryFixture()
				resumer := &resumableTaskManager{switchModelTaskManager: manager, found: true}
				if pending {
					resumer.resumeErr = &taskflow.RestartPendingError{Err: context.DeadlineExceeded}
				}
				uc.taskflow.(*switchModelTaskflow).taskMgr = resumer
				var err error
				if kind == "model" {
					_, err = uc.SwitchModel(context.Background(), &domain.User{ID: owner}, taskID, domain.SwitchTaskModelReq{RequestID: "restart", ModelID: models.model.ID})
				} else {
					_, err = uc.SwitchAgentResources(context.Background(), &domain.User{ID: owner}, taskID, domain.SwitchAgentResourcesReq{RequestID: "restart", SkillIDs: []string{"original-skill"}})
				}
				if pending != taskflow.IsRestartPending(err) {
					t.Fatalf("wrong replay result: %v", err)
				}
				if !pending && err != nil {
					t.Fatal(err)
				}
				if models.runtimeVMID != "" || manager.restartCalls != 0 || repo.created != nil || repo.finishedID != uuid.Nil || repo.resourceUpdates != 0 {
					t.Fatal("replayed request mutated credentials, runtime or business state")
				}
				if resumer.resumeReq.BusinessMutation == nil || resumer.resumeReq.ExecutionConfig != nil {
					t.Fatal("resume must use stable metadata without rebuilt execution config")
				}
			})
		}
	}
}

func TestSwitchModelAdmissionFailureStillFinishesAudit(t *testing.T) {
	uc, repo, models, manager, owner, taskID := switchRecoveryFixture()
	manager.err = errors.New("rejected before admission")
	_, err := uc.SwitchModel(context.Background(), &domain.User{ID: owner}, taskID, domain.SwitchTaskModelReq{RequestID: "restart", ModelID: models.model.ID})
	if err == nil || repo.finishedID == uuid.Nil || repo.finishedSuccess {
		t.Fatal("definite admission rejection not recorded")
	}
}

func TestSwitchReplayStillRequiresTaskOwner(t *testing.T) {
	for _, kind := range []string{"model", "resources"} {
		t.Run(kind, func(t *testing.T) {
			uc, _, models, manager, _, taskID := switchRecoveryFixture()
			resumer := &resumableTaskManager{switchModelTaskManager: manager, found: true}
			uc.taskflow.(*switchModelTaskflow).taskMgr = resumer
			outsider := &domain.User{ID: uuid.New()}
			var err error
			if kind == "model" {
				_, err = uc.SwitchModel(context.Background(), outsider, taskID, domain.SwitchTaskModelReq{RequestID: "restart", ModelID: models.model.ID})
			} else {
				_, err = uc.SwitchAgentResources(context.Background(), outsider, taskID, domain.SwitchAgentResourcesReq{RequestID: "restart"})
			}
			if err == nil || resumer.resumeReq.ID != uuid.Nil || models.runtimeVMID != "" {
				t.Fatal("replay bypassed task authorization")
			}
		})
	}
}
