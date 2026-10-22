package usecase

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

// fillAgentResourceBaseline 把落库的基线填进 domain.Task.Extra（cvt 不映射嵌套字段）。
// tk.Extra 为 nil 时初始化；只赋 SkillIDs/PluginIDs 两个字段，不覆盖 Extra 其他字段。
func fillAgentResourceBaseline(tk *domain.Task, skillIDs, pluginIDs []string) {
	if tk.Extra == nil {
		tk.Extra = &domain.TaskExtraConfig{}
	}
	tk.Extra.SkillIDs = skillIDs
	tk.Extra.PluginIDs = pluginIDs
}

// normalizeAgentResources 确保 AgentResources 非 nil；nil 入参转为空结构体（表达"清空"语义）。
// 该函数为纯函数，便于单独测试。
func normalizeAgentResources(ar *taskflow.AgentResources) *taskflow.AgentResources {
	if ar == nil {
		return &taskflow.AgentResources{}
	}
	return ar
}

// SwitchAgentResources 切换运行中任务的 skill/plugin 列表
func (a *TaskUsecase) SwitchAgentResources(ctx context.Context, user *domain.User, taskID uuid.UUID, req domain.SwitchAgentResourcesReq) (*domain.SwitchAgentResourcesResp, error) {
	t, owner, err := a.Info(ctx, user, taskID)
	if err != nil {
		return nil, restartObservationError(err)
	}
	if !owner && !a.isPrivileged(ctx, user.ID) {
		return nil, errcode.ErrForbidden
	}
	// Observe controls accepted before this endpoint was retired. Never admit
	// a new resource mutation, even when a stale client supplies a request ID.
	if resumer, ok := a.taskflow.TaskManager().(taskflow.RestartResumer); ok && req.RequestID != "" {
		mutation := &taskflow.RestartBusinessMutation{OwnerID: t.UserID, ResourceSelection: &taskflow.RestartResourceSelection{SkillIDs: req.SkillIDs, PluginIDs: req.PluginIDs}}
		resp, found, err := resumer.ResumeRestart(ctx, taskflow.RestartTaskReq{ID: taskID, RequestId: req.RequestID, LoadSession: true, BusinessMutation: mutation})
		if err != nil {
			return nil, err
		}
		if found && resp != nil {
			return &domain.SwitchAgentResourcesResp{RequestID: resp.RequestId, Success: resp.Success, Message: resp.Message, SessionID: resp.SessionID}, nil
		}
	}
	return nil, errcode.ErrBadRequest.Wrap(fmt.Errorf("请在我的技能中维护，技能变更将在下一轮消息生效"))
}

// Before checking the ledger, a failed task read cannot establish whether an
// original request is still running. Only authoritative access/deletion errors
// may clear the caller's saved request ID.
func restartObservationError(err error) error {
	for _, terminal := range []error{errcode.ErrNotFound, errcode.ErrForbidden, errcode.ErrUnauthorized, errcode.ErrPermision} {
		if errors.Is(err, terminal) {
			return err
		}
	}
	return &taskflow.RestartPendingError{Err: err}
}
