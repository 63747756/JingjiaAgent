package usecase

import (
	"context"
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
)

type workModelAccess struct {
	denyWrite bool
	writes    int
}

func (g *workModelAccess) CheckWorkTaskAccess(_ context.Context, _, _ uuid.UUID, write bool) error {
	if write {
		g.writes++
		if g.denyWrite {
			return errcode.ErrForbidden
		}
	}
	return nil
}

func TestWorkModelSwitchKeepsResourcesAndUsesRuntimeSession(t *testing.T) {
	for _, cli := range []consts.CliName{consts.CliNameOpencode, consts.CliNameCodex, consts.CliNameClaude} {
		t.Run(string(cli), func(t *testing.T) {
			uc, repo, models, manager, owner, tid := switchRecoveryFixture()
			uc.cfg = &config.Config{}
			uc.cfg.Runtime.Backend = "agent_compose"
			repo.task.Kind = consts.TaskTypeWork
			repo.task.Edges.ProjectTasks[0].CliName = cli
			if cli == consts.CliNameClaude {
				models.model.InterfaceType = "anthropic"
			}
			guard := &workModelAccess{}
			uc.workAccess = guard
			response, err := uc.SwitchModel(context.Background(), &domain.User{ID: owner}, tid, domain.SwitchTaskModelReq{RequestID: "work-model", ModelID: models.model.ID, LoadSession: true})
			if err != nil || response == nil || !response.Success {
				t.Fatalf("switch failed: %+v %v", response, err)
			}
			request := manager.restartReq
			if !request.LoadSession || request.ExecutionConfig == nil || request.ExecutionConfig.AgentResources != nil || request.ExecutionConfig.McpServers != nil || guard.writes != 1 {
				t.Fatalf("resource/session boundary changed: %+v", request)
			}
			if request.ExecutionConfig.LLM.Model != models.model.Model || models.runtimeUserID != owner {
				t.Fatal("wrong owner or model")
			}
		})
	}
}

func TestWorkModelSwitchRejectsInvalidSelectionBeforeCreatingCredentials(t *testing.T) {
	for _, mode := range []string{"access", "protocol", "offline"} {
		t.Run(mode, func(t *testing.T) {
			uc, repo, models, manager, owner, tid := switchRecoveryFixture()
			uc.cfg = &config.Config{}
			uc.cfg.Runtime.Backend = "agent_compose"
			repo.task.Kind = consts.TaskTypeWork
			uc.workAccess = &workModelAccess{denyWrite: mode == "access"}
			if mode == "protocol" {
				repo.task.Edges.ProjectTasks[0].CliName = consts.CliNameClaude
			}
			if mode == "offline" {
				uc.cfg.Work.Enabled = true
				uc.cfg.Work.Offline = true
				uc.cfg.Work.AllowedModelHosts = []string{"127.0.0.1:8000"}
			}
			_, err := uc.SwitchModel(context.Background(), &domain.User{ID: owner}, tid, domain.SwitchTaskModelReq{RequestID: "work-model", ModelID: models.model.ID})
			if err == nil || models.runtimeVMID != "" || manager.restartCalls != 0 || repo.created != nil {
				t.Fatalf("rejected selection mutated state: %v", err)
			}
		})
	}
}
