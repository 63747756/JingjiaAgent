package usecase

import (
	"context"
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

type selectionManager struct {
	*switchModelTaskManager
	req taskflow.NextTurnModelReq
}

func (s *selectionManager) SelectNextTurnModel(_ context.Context, req taskflow.NextTurnModelReq) error {
	s.req = req
	return nil
}

func TestSelectModelOnlySavesNextTurnWithoutRuntimeObservation(t *testing.T) {
	for _, cli := range []consts.CliName{consts.CliNameOpencode, consts.CliNameCodex, consts.CliNameClaude} {
		t.Run(string(cli), func(t *testing.T) {
			uc, repo, models, previous, owner, tid := switchRecoveryFixture()
			repo.task.Kind = consts.TaskTypeWork
			repo.task.Edges.ProjectTasks[0].CliName = cli
			if cli == consts.CliNameClaude {
				models.model.InterfaceType = "anthropic"
			}
			manager := &selectionManager{switchModelTaskManager: previous}
			// A nil VM client deliberately fails if this path starts observing a VM.
			uc.taskflow = &switchModelTaskflow{taskMgr: manager}
			response, err := uc.SelectModel(context.Background(), &domain.User{ID: owner}, domain.SelectTaskModelReq{ID: tid, RequestID: "select", ModelID: models.model.ID})
			if err != nil || response == nil || !response.Success {
				t.Fatalf("selection failed: %+v %v", response, err)
			}
			if manager.req.OwnerID != owner || manager.req.ModelID != models.model.ID || manager.req.Config.LLM.Model != models.model.Model || manager.req.Config.AgentResources != nil || previous.restartCalls != 0 || repo.created != nil {
				t.Fatal("selection touched restart/resources or used wrong owner")
			}
		})
	}
}

func TestSelectModelRejectsBeforeSavingOrIssuingCredentials(t *testing.T) {
	for _, mode := range []string{"owner", "access", "protocol", "offline"} {
		t.Run(mode, func(t *testing.T) {
			uc, repo, models, previous, owner, tid := switchRecoveryFixture()
			manager := &selectionManager{switchModelTaskManager: previous}
			uc.taskflow = &switchModelTaskflow{taskMgr: manager}
			uc.cfg = &config.Config{}
			userID := owner
			if mode == "owner" {
				userID = uuid.New()
			}
			uc.workAccess = &workModelAccess{denyWrite: mode == "access"}
			if mode == "protocol" {
				repo.task.Edges.ProjectTasks[0].CliName = consts.CliNameClaude
			}
			if mode == "offline" {
				uc.cfg.Work.Enabled, uc.cfg.Work.Offline = true, true
			}
			_, err := uc.SelectModel(context.Background(), &domain.User{ID: userID}, domain.SelectTaskModelReq{ID: tid, RequestID: "select", ModelID: models.model.ID})
			if err == nil || manager.req.TaskID != uuid.Nil || models.runtimeVMID != "" {
				t.Fatalf("rejected selection mutated state: %v", err)
			}
		})
	}
}
