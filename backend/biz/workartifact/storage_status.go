package workartifact

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/63747756/jingjiaagent/backend/pkg/vmrecycle"
	"github.com/GoYoko/web"
	"github.com/google/uuid"
)

func storageFailureMessage(err error) string {
	text := err.Error()
	for _, reason := range []string{"超过 2 GiB", "个条目的保存限制", "特殊文件", "清单过大", "运行节点尚不支持", "运行节点没有返回完整工作空间", "哈希不匹配", "校验失败", "超过工作平台大小限制", "保存进程多次中断", "在保存期间发生变化", "成果目录", "成果总大小超过", "成果单文件超过"} {
		if strings.Contains(text, reason) {
			// Runtime diagnostics contain file paths, never file contents. Do not
			// reflect transport errors or signed object URLs to the browser.
			if !strings.Contains(text, "://") && len(text) < 1500 {
				return text + "；原工作空间保留。"
			}
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "工作空间保存超时，原环境保留；请检查存储和运行节点后重试。"
	}
	return "工作空间保存或后续处理失败，原环境保留；请检查对象存储、运行节点及文档转换服务后重试。"
}

type StorageStatus struct {
	ID       uuid.UUID `json:"id"`
	Kind     string    `json:"kind"`
	Error    string    `json:"error"`
	Attempts int       `json:"attempts"`
	Paused   bool      `json:"paused"`
}

func (s *Service) StorageStatus(ctx context.Context, uid, tid uuid.UUID) ([]StorageStatus, error) {
	if err := s.enabled(); err != nil {
		return nil, err
	}
	if _, err := s.taskPermission(ctx, uid, tid); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT j.id,j.kind,j.error,j.attempts FROM work_storage_jobs j JOIN runtime_task_intents i ON i.task_id=j.task_id AND i.environment_id=j.environment_id JOIN runtime_environments e ON e.id=i.environment_id WHERE j.task_id=$1 AND j.state='failed' AND j.error<>'' AND e.state<>'deleted' AND NOT EXISTS(SELECT 1 FROM work_storage_jobs newer WHERE newer.task_id=j.task_id AND newer.kind=j.kind AND COALESCE(newer.request->>'scope','')=COALESCE(j.request->>'scope','') AND newer.created_at>j.created_at AND newer.state='complete') ORDER BY j.updated_at DESC LIMIT 10`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []StorageStatus{}
	for rows.Next() {
		var item StorageStatus
		if err = rows.Scan(&item.ID, &item.Kind, &item.Error, &item.Attempts); err != nil {
			return nil, err
		}
		item.Paused = item.Attempts >= 3
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) RetryStorage(ctx context.Context, uid, tid, id uuid.UUID) error {
	if err := s.enabled(); err != nil {
		return err
	}
	if _, err := s.taskPermission(ctx, uid, tid); err != nil {
		return err
	}
	if s.guard != nil {
		if err := s.guard.CheckWorkTaskAccess(ctx, uid, tid, true); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var env string
	if err = tx.QueryRowContext(ctx, `SELECT environment_id FROM runtime_task_intents WHERE task_id=$1 FOR UPDATE`, tid).Scan(&env); err != nil {
		return err
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM runtime_environments WHERE id=$1 FOR UPDATE`, env).Scan(&state); err != nil {
		return err
	}
	if state == "deleted" {
		return errcode.ErrBadRequest.Wrap(errors.New("原工作空间已回收"))
	}
	var kind, scope string
	err = tx.QueryRowContext(ctx, `SELECT kind,COALESCE(request->>'scope','') FROM work_storage_jobs WHERE id=$1 AND task_id=$2 AND environment_id=$3 AND state='failed' FOR UPDATE`, id, tid, env).Scan(&kind, &scope)
	if errors.Is(err, sql.ErrNoRows) {
		return errcode.ErrNotFound
	}
	if err != nil {
		return err
	}
	// Reset only jobs from this owner-authorized current task/environment.
	// A recycle retry also retries its failed full snapshot, never an old VM.
	if _, err = tx.ExecContext(ctx, `UPDATE work_storage_jobs SET attempts=0,state=CASE WHEN kind IN ('checkpoint','artifacts') THEN 'pending' ELSE 'failed' END,error='',available_at=now(),updated_at=now() WHERE task_id=$1 AND environment_id=$2 AND state='failed' AND (id=$3 OR kind='recycle' OR ($4='recycle' AND request->>'scope'='workspace'))`, tid, env, id, kind); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if (scope == "workspace" || kind == "recycle") && s.recycleQueue != nil {
		var payload domain.VmIdleInfo
		if err = s.db.QueryRowContext(ctx, `SELECT id,environment_id,host_id,user_id FROM virtualmachines WHERE environment_id=$1 AND is_recycled=false LIMIT 1`, env).Scan(&payload.VmID, &payload.EnvID, &payload.HostID, &payload.UID); err != nil {
			return err
		}
		// The normal consumer rechecks current admin policy and recent activity.
		_, err = s.recycleQueue.Enqueue(ctx, vmrecycle.RecycleQueueKey, &payload, time.Now(), payload.VmID)
	}
	return err
}

func (h *Handler) storageStatus(c *web.Context) error {
	tid, err := param(c)
	if err != nil {
		return err
	}
	items, err := h.s.StorageStatus(c.Request().Context(), middleware.GetUser(c).ID, tid)
	if err != nil {
		return err
	}
	return c.Success(map[string]any{"items": items})
}
func (h *Handler) retryStorage(c *web.Context) error {
	tid, err := param(c)
	if err != nil {
		return err
	}
	var req struct {
		ID uuid.UUID `json:"id"`
	}
	if err = body(c, &req); err != nil || req.ID == uuid.Nil {
		return errcode.ErrBadRequest
	}
	if err = h.s.RetryStorage(c.Request().Context(), middleware.GetUser(c).ID, tid, req.ID); err != nil {
		return err
	}
	return c.Success(map[string]any{"accepted": true})
}
