package workartifact

import (
	"context"
	"errors"
	"fmt"

	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/63747756/jingjiaagent/backend/pkg/runtimeadapter"
	"github.com/GoYoko/web"
	"github.com/google/uuid"
)

func (s *Service) restoreContinuation(ctx context.Context, uid, tid, confirmed uuid.UUID) (string, error) {
	checkpoints, err := s.Checkpoints(ctx, uid, tid)
	if err != nil {
		return "", err
	}
	var chosen *Checkpoint
	for i := range checkpoints {
		if checkpoints[i].State == "committed" && (confirmed == uuid.Nil || checkpoints[i].ID == confirmed) {
			chosen = &checkpoints[i]
			break
		}
	}
	if chosen == nil {
		return "", &runtimeadapter.RecoveryChoiceError{Message: "工作环境已丢失，暂无可用的保存节点。对话和已保存的成果仍可查看，请联系管理员。"}
	}
	if confirmed == uuid.Nil {
		var newer bool
		err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_commands c CROSS JOIN workspace_checkpoints cp LEFT JOIN work_storage_jobs j ON j.id=cp.id WHERE c.task_id=$1 AND cp.id=$2 AND c.operation='task' AND c.submission_started AND CASE WHEN cp.manifest->>'schema'='jingjiaagent.workspace.checkpoint.v3' AND j.request ? 'captured_turn' THEN c.turn>(j.request->>'captured_turn')::integer ELSE c.created_at>COALESCE((SELECT created_at FROM runtime_commands WHERE id=cp.command_id),cp.committed_at,cp.created_at) END)`, tid, chosen.ID).Scan(&newer)
		if err != nil {
			return "", err
		}
		if newer {
			return "", &runtimeadapter.RecoveryChoiceError{CheckpointID: chosen.ID, Message: fmt.Sprintf("最新工作未完整保存。可恢复到 %s 的文件，之后的文件修改可能缺失；历史对话会保留。", chosen.CreatedAt)}
		}
	}
	result, err := s.Restore(ctx, uid, tid, chosen.ID)
	if errors.Is(err, errCheckpointIntegrity) {
		// Never silently downgrade to another checkpoint after a failed verification.
		// Expose the next candidate only as an explicit, authorized recovery choice.
		for _, cp := range checkpoints {
			if cp.State == "committed" && cp.CreatedAt.Before(chosen.CreatedAt) {
				return "", &runtimeadapter.RecoveryChoiceError{CheckpointID: cp.ID, Message: fmt.Sprintf("当前保存节点无法恢复。可尝试 %s 的较早节点，之后的文件修改可能缺失；也可重试当前节点。", cp.CreatedAt.Format("2006-01-02 15:04:05 MST"))}
			}
		}
	}
	return result, err
}

func (h *Handler) continuations(c *web.Context) error {
	tid, err := param(c)
	if err != nil {
		return err
	}
	uid := middleware.GetUser(c).ID
	if _, err = h.s.taskPermission(c.Request().Context(), uid, tid); err != nil {
		return err
	}
	if h.s.runtime == nil {
		return errcode.ErrBadRequest
	}
	items, err := h.s.runtime.Continuations(c.Request().Context(), uid, tid)
	if err != nil {
		return err
	}
	return c.Success(map[string]any{"items": items})
}
func (h *Handler) retryContinuation(c *web.Context) error {
	tid, err := param(c)
	if err != nil {
		return err
	}
	uid := middleware.GetUser(c).ID
	if _, err = h.s.taskPermission(c.Request().Context(), uid, tid); err != nil {
		return err
	}
	if h.s.guard != nil {
		if err = h.s.guard.CheckWorkTaskAccess(c.Request().Context(), uid, tid, true); err != nil {
			return err
		}
	}
	if h.s.runtime == nil {
		return errcode.ErrBadRequest
	}
	var req struct {
		ID           uuid.UUID  `json:"id"`
		CheckpointID *uuid.UUID `json:"checkpoint_id"`
		Cancel       bool       `json:"cancel"`
	}
	if err = body(c, &req); err != nil || req.ID == uuid.Nil {
		return errcode.ErrBadRequest
	}
	if req.CheckpointID != nil && *req.CheckpointID == uuid.Nil {
		return errcode.ErrBadRequest.Wrap(errors.New("请选择有效的恢复节点"))
	}
	if err = h.s.runtime.RetryContinuation(c.Request().Context(), uid, tid, req.ID, req.CheckpointID, req.Cancel); err != nil {
		return errcode.ErrBadRequest.Wrap(err)
	}
	return c.Success(map[string]any{"accepted": true})
}
