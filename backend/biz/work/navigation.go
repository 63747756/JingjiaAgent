package work

import (
	"context"
	"database/sql"
	"errors"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/GoYoko/web"
	"github.com/google/uuid"
	"time"
)

type NavigationPin struct {
	Kind     string    `json:"kind"`
	TargetID uuid.UUID `json:"target_id"`
	PinnedAt time.Time `json:"pinned_at"`
}

func nullableProjectID(id sql.NullString) any {
	if !id.Valid {
		return nil
	}
	return id.String
}

type PinRequest struct {
	Kind     string    `json:"kind"`
	TargetID uuid.UUID `json:"target_id"`
	Pinned   bool      `json:"pinned"`
}

func (h *Handler) navigationPins(c *web.Context) error {
	uid := middleware.GetUser(c).ID
	rows, err := h.service.store.db.QueryContext(c.Request().Context(), `SELECT kind,target_id,pinned_at FROM navigation_pins WHERE user_id=$1 ORDER BY pinned_at DESC,target_id`, uid)
	if err != nil {
		return err
	}
	defer rows.Close()
	pins := []NavigationPin{}
	for rows.Next() {
		var p NavigationPin
		if err = rows.Scan(&p.Kind, &p.TargetID, &p.PinnedAt); err != nil {
			return err
		}
		pins = append(pins, p)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return c.Success(map[string]any{"items": pins})
}

func (s *Service) setNavigationPin(ctx context.Context, u *domain.User, r PinRequest) error {
	if r.TargetID == uuid.Nil {
		return errcode.ErrBadRequest
	}
	switch r.Kind {
	case "project":
		if _, err := s.projects.Get(ctx, u.ID, r.TargetID); err != nil {
			return errcode.ErrForbidden
		}
	case "task":
		if err := s.guard.CheckWorkTaskAccess(ctx, u.ID, r.TargetID, false); err != nil {
			return err
		}
		if _, err := s.taskRepo.Info(ctx, u, r.TargetID, false); err != nil {
			return err
		}
	default:
		return errcode.ErrBadRequest
	}
	if !r.Pinned {
		_, err := s.store.db.ExecContext(ctx, `DELETE FROM navigation_pins WHERE user_id=$1 AND kind=$2 AND target_id=$3`, u.ID, r.Kind, r.TargetID)
		return err
	}
	_, err := s.store.db.ExecContext(ctx, `INSERT INTO navigation_pins(user_id,kind,target_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, u.ID, r.Kind, r.TargetID)
	return err
}
func (h *Handler) setNavigationPin(c *web.Context, r PinRequest) error {
	if err := h.service.setNavigationPin(c.Request().Context(), middleware.GetUser(c), r); err != nil {
		return err
	}
	return c.Success(nil)
}

func (h *Handler) navigationTask(c *web.Context, r domain.IDReq[uuid.UUID]) error {
	ctx, u := c.Request().Context(), middleware.GetUser(c)
	if err := h.service.guard.CheckWorkTaskAccess(ctx, u.ID, r.ID, false); err != nil {
		return err
	}
	if _, err := h.service.taskRepo.Info(ctx, u, r.ID, false); err != nil {
		return err
	}
	var title, content, status string
	var pid sql.NullString
	var running bool
	err := h.service.store.db.QueryRowContext(ctx, `SELECT COALESCE(t.title,''),COALESCE(t.content,''),t.status,pt.project_id::text,
 CASE WHEN EXISTS(SELECT 1 FROM runtime_task_intents i WHERE i.task_id=t.id)
 THEN EXISTS(SELECT 1 FROM runtime_commands c WHERE c.task_id=t.id AND c.state NOT IN ('complete','failed','canceled'))
 ELSE t.status IN ('pending','processing') END
 FROM tasks t JOIN project_tasks pt ON pt.task_id=t.id WHERE t.id=$1 AND t.user_id=$2 AND t.deleted_at IS NULL`, r.ID, u.ID).Scan(&title, &content, &status, &pid, &running)
	if errors.Is(err, sql.ErrNoRows) {
		return errcode.ErrNotFound
	}
	if err != nil {
		return err
	}
	if pid.Valid {
		id, _ := uuid.Parse(pid.String)
		if _, err = h.service.projects.Get(ctx, u.ID, id); err != nil {
			return errcode.ErrForbidden
		}
	}
	return c.Success(map[string]any{"id": r.ID, "title": title, "content": content, "status": status, "project_id": nullableProjectID(pid), "running": running})
}
