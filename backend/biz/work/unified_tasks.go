package work

import (
	"context"
	"errors"

	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/cvt"
	"github.com/google/uuid"
)

// The unified library includes existing coding tasks and new assistant tasks.
// Task ownership, current project membership and work grants still apply.
const unifiedTaskQuery = `SELECT t.id,(w.task_id IS NOT NULL)
 FROM tasks t JOIN project_tasks pt ON pt.task_id=t.id
 LEFT JOIN work_tasks w ON w.task_id=t.id
 LEFT JOIN navigation_pins pin ON pin.user_id=$1 AND pin.kind='task' AND pin.target_id=t.id
 WHERE t.user_id=$1 AND t.deleted_at IS NULL
 AND ($2::uuid IS NULL OR pt.project_id=$2)
 AND (NOT $5 OR pt.project_id IS NULL)
 AND (pt.project_id IS NULL OR EXISTS(SELECT 1 FROM projects p
  WHERE p.id=pt.project_id AND p.deleted_at IS NULL
  AND (p.user_id=$1 OR EXISTS(SELECT 1 FROM project_collaborators c
   WHERE c.project_id=p.id AND c.user_id=$1 AND c.deleted_at IS NULL))
  AND (p.team_id IS NULL OR EXISTS(SELECT 1 FROM team_members m JOIN teams tm ON tm.id=m.team_id
   WHERE m.team_id=p.team_id AND m.user_id=$1 AND tm.deleted_at IS NULL))))
 AND (w.task_id IS NULL OR EXISTS(SELECT 1 FROM team_members m JOIN teams tm ON tm.id=m.team_id
  WHERE m.user_id=$1 AND m.team_id=w.team_id AND tm.deleted_at IS NULL))
 ORDER BY pin.pinned_at DESC NULLS LAST,t.last_active_at DESC,t.id DESC LIMIT $3 OFFSET $4`

func (s *Service) UnifiedTasks(ctx context.Context, user *domain.User, req ListRequest) (UnifiedListResponse, error) {
	if req.Page < 1 {
		req.Page = 1
	}
	if req.Limit < 1 {
		req.Limit = 20
	}
	if req.Limit > 50 {
		req.Limit = 50
	}
	if req.ProjectID != nil && (*req.ProjectID == uuid.Nil || req.Unlinked) {
		return UnifiedListResponse{}, errcode.ErrBadRequest
	}
	out := UnifiedListResponse{Items: []UnifiedTaskView{}, Page: req.Page}
	rows, err := s.store.db.QueryContext(ctx, unifiedTaskQuery, user.ID, req.ProjectID, req.Limit+1, (req.Page-1)*req.Limit, req.Unlinked)
	if err != nil {
		return out, err
	}
	type reference struct {
		id   uuid.UUID
		work bool
	}
	refs := []reference{}
	for rows.Next() {
		var ref reference
		if err = rows.Scan(&ref.id, &ref.work); err != nil {
			rows.Close()
			return out, err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(refs) > req.Limit {
		refs, out.HasMore = refs[:req.Limit], true
	}
	for _, ref := range refs {
		var meta *Metadata
		if ref.work {
			if err = s.guard.CheckWorkTaskAccess(ctx, user.ID, ref.id, false); err != nil {
				if errors.Is(err, errcode.ErrForbidden) || errors.Is(err, errcode.ErrNotFound) {
					continue
				}
				return out, err
			}
			m, err := s.store.metadata(ctx, ref.id)
			if err != nil {
				return out, err
			}
			meta = &m
		}
		row, err := s.taskRepo.Info(ctx, user, ref.id, false)
		if err != nil {
			return out, err
		}
		task := cvt.From(row, &domain.Task{})
		out.Items = append(out.Items, UnifiedTaskView{ProjectTask: &domain.ProjectTask{
			ID: task.ID, ProjectID: task.ProjectID, Task: task, Model: task.Model, Image: task.Image, CliName: task.CliName,
			Extra: task.Extra, RepoURL: task.RepoURL, Branch: task.Branch, FullName: task.FullName,
		}, Work: meta})
	}
	return out, nil
}
