package work

import (
	"context"
	"errors"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/GoYoko/web"
	"github.com/google/uuid"
)

type ModelOptionsRequest struct {
	ProfileID *uuid.UUID `query:"profile_id"`
	TaskID    *uuid.UUID `query:"task_id"`
}
type ModelOptionsResponse struct {
	Items          []*domain.ModelBrief `json:"items"`
	DefaultModelID uuid.UUID            `json:"default_model_id"`
}

func (h *Handler) ModelOptions(c *web.Context, req ModelOptionsRequest) error {
	out, err := h.service.ModelOptions(c.Request().Context(), middleware.GetUser(c), req)
	if err != nil {
		return err
	}
	return c.Success(out)
}

func (s *Service) ModelOptions(ctx context.Context, user *domain.User, req ModelOptionsRequest) (ModelOptionsResponse, error) {
	out := ModelOptionsResponse{Items: []*domain.ModelBrief{}}
	if err := s.enabled(); err != nil {
		return out, err
	}
	if (req.ProfileID == nil) == (req.TaskID == nil) {
		return out, errcode.ErrBadRequest
	}
	var cli consts.CliName
	if req.TaskID != nil {
		task, err := s.Get(ctx, user, *req.TaskID)
		if err != nil {
			return out, err
		}
		cli = task.CliName
		if task.Model != nil {
			out.DefaultModelID = task.Model.ID
		}
	} else {
		p, err := s.selectProfile(ctx, user.ID, CreateRequest{ProfileID: req.ProfileID})
		if err != nil {
			return out, err
		}
		cli, out.DefaultModelID = p.CliName, p.ModelID
	}
	cursor := domain.CursorReq{Limit: 100}
	for {
		models, page, err := s.models.List(ctx, user.ID, cursor)
		if err != nil {
			return out, err
		}
		for _, model := range models {
			if model == nil || model.IsHidden || config.ValidateAgentModel(cli, model.InterfaceType) != nil {
				continue
			}
			if s.cfg.Work.Offline && validateOfflineModel(model.BaseURL, s.cfg.Work.AllowedModelHosts) != nil {
				continue
			}
			out.Items = append(out.Items, (&domain.ModelBrief{}).From(model))
		}
		if page == nil || !page.HasNextPage {
			break
		}
		if page.Cursor == "" || page.Cursor == cursor.Cursor {
			return out, errors.New("模型列表分页异常，请重试")
		}
		cursor.Cursor = page.Cursor
	}
	return out, nil
}
