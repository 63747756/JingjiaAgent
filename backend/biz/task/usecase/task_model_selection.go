package usecase

import (
	"context"
	"errors"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/cvt"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func (a *TaskUsecase) SelectModel(ctx context.Context, user *domain.User, req domain.SelectTaskModelReq) (*domain.SwitchTaskModelResp, error) {
	if req.RequestID == "" || len(req.RequestID) > 256 || req.ModelID == uuid.Nil {
		return nil, errcode.ErrBadRequest
	}
	// Read business metadata only: saving a selection must not inspect/wake a VM.
	raw, err := a.repo.Info(ctx, user, req.ID, a.isPrivileged(ctx, user.ID))
	if err != nil {
		return nil, err
	}
	if raw.UserID != user.ID && !a.isPrivileged(ctx, user.ID) {
		return nil, errcode.ErrForbidden
	}
	if a.workAccess != nil {
		if err := a.workAccess.CheckWorkTaskAccess(ctx, user.ID, req.ID, true); err != nil {
			return nil, err
		}
	}
	task := cvt.From(raw, &domain.Task{})
	if (task.Status != consts.TaskStatusProcessing && task.Type != consts.TaskTypeWork) || task.VirtualMachine == nil {
		return nil, errcode.ErrBadRequest.Wrap(errors.New("task is not available for model selection"))
	}
	selector, ok := a.taskflow.TaskManager().(taskflow.NextTurnModelSelector)
	if !ok {
		return nil, errcode.ErrBadRequest.Wrap(errors.New("当前运行环境不支持下一轮模型选择"))
	}
	if a.modelHook != nil {
		if err := a.modelHook.ValidateAccess(ctx, task.UserID, req.ModelID.String()); err != nil {
			return nil, err
		}
	}
	model, err := a.modelRepo.Get(ctx, task.UserID, req.ModelID)
	if err != nil {
		return nil, err
	}
	if err := config.ValidateAgentModel(task.CliName, model.InterfaceType); err != nil {
		return nil, errcode.ErrBadRequest.Wrap(err)
	}
	if a.cfg != nil {
		if err := config.ValidateModelURL(model.BaseURL); err != nil {
			return nil, errcode.ErrForbidden.Wrap(err)
		}
	}
	key, err := a.modelRepo.CreateRuntimeAPIKey(ctx, task.UserID, req.ModelID, task.VirtualMachine.ID)
	if err != nil {
		return nil, err
	}
	if key != "" {
		model.APIKey = key // Keys are immutable per model; active turns keep theirs.
		if a.cfg != nil {
			model.BaseURL = a.cfg.LLMProxy.BaseURL + "/v1"
		}
	}
	_, configs, err := modelConfigFiles(task.CliName, model)
	if err != nil {
		return nil, err
	}
	envs := map[string]string{"OPENAI_API_KEY": model.APIKey, "OPEN_CODE_API_KEY": model.APIKey, "OPENCODE_DISABLE_DEFAULT_PLUGINS": "1", "OPENCODE_DISABLE_LSP_DOWNLOAD": "true", "JINGJIAAGENT_MODEL_PROVIDER_TYPE": model.InterfaceType}
	err = selector.SelectNextTurnModel(ctx, taskflow.NextTurnModelReq{
		TaskID: req.ID, OwnerID: task.UserID, ModelID: req.ModelID, RequestID: req.RequestID,
		Config: &taskflow.TaskExecutionConfig{LLM: &taskflow.LLM{ApiKey: model.APIKey, BaseURL: model.BaseURL, Model: model.Model, ApiType: model.InterfaceType}, Envs: envs, ConfigFiles: configs},
	})
	if err != nil {
		return nil, err
	}
	return &domain.SwitchTaskModelResp{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(req.ID.String()+":next-turn-model:"+req.RequestID)), RequestID: req.RequestID, Success: true, Message: "Saved for next turn", Model: cvt.From(model, &domain.ModelBrief{})}, nil
}
