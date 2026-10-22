package work

import (
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/GoYoko/web"
	"github.com/google/uuid"
	"io"
)

type skillListQuery struct {
	TeamID *uuid.UUID `query:"team_id"`
}
type skillReviewQuery struct {
	TeamID uuid.UUID `query:"team_id" validate:"required"`
	ID     uuid.UUID `param:"id"`
}
type skillDraftUpdate struct {
	ID uuid.UUID `param:"id" json:"-"`
	DraftSkillRequest
}
type skillRollbackUpdate struct {
	ID uuid.UUID `param:"id" json:"-"`
	SkillRollbackRequest
}

func (h *Handler) registerSkills(g, a, r *web.Group) {
	r.GET("/skills", web.BindHandler(h.ListSkills))
	r.GET("/skills/mentions", web.BindHandler(h.MentionSkills))
	g.POST("/skills", web.BindHandler(h.CreateDraft))
	r.GET("/skills/:id", web.BindHandler(h.GetSkill))
	g.POST("/skills/:id/versions", web.BindHandler(h.UpdateDraft))
	g.POST("/skills/package", web.BaseHandler(h.UploadPersonalPackage))
	g.POST("/skills/:id/package", web.BaseHandler(h.UploadPersonalPackage))
	g.PUT("/skills/:id/status", web.BindHandler(h.PersonalSkillStatus))
	g.DELETE("/skills/:id", web.BindHandler(h.DeletePersonalSkill))
	g.POST("/skills/selection", web.BindHandler(h.PreviewSkills))
	g.POST("/skills/:id/trial", web.BaseHandler(h.RetiredSkillWorkflow))
	g.POST("/skills/:id/submit", web.BaseHandler(h.RetiredSkillWorkflow))
	a.POST("/skills/:id/rollback", web.BindHandler(h.Rollback))
	a.GET("/skill-reviews", web.BindHandler(h.ListReviews))
	a.GET("/skill-reviews/:id", web.BindHandler(h.GetReview))
	a.POST("/skill-reviews/:id/review", web.BaseHandler(h.RetiredSkillWorkflow))
	a.POST("/skill-reviews/:id/publish", web.BaseHandler(h.RetiredSkillWorkflow))
}
func (h *Handler) MentionSkills(c *web.Context, req SelectionPreviewRequest) error {
	out, err := h.service.PreviewSkills(c.Request().Context(), middleware.GetUser(c), req)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}

type personalSkillStatus struct {
	ID      uuid.UUID `param:"id" json:"-"`
	Enabled *bool     `json:"enabled"`
}

func (h *Handler) PersonalSkillStatus(c *web.Context, r personalSkillStatus) error {
	if err := h.service.SetPersonalSkillEnabled(c.Request().Context(), middleware.GetUser(c), r.ID, r.Enabled); err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(true)
}
func (h *Handler) DeletePersonalSkill(c *web.Context, r domain.IDReq[uuid.UUID]) error {
	if err := h.service.DeletePersonalSkill(c.Request().Context(), middleware.GetUser(c), r.ID); err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(true)
}
func (h *Handler) PreviewSkills(c *web.Context, r SelectionPreviewRequest) error {
	out, err := h.service.PreviewSkills(c.Request().Context(), middleware.GetUser(c), r)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
func (h *Handler) RetiredSkillWorkflow(c *web.Context) error {
	return c.JSON(410, map[string]any{"code": 410, "message": "本地技能审核已下线，请上传到 SkillHub 完成审核"})
}
func (h *Handler) UploadPersonalPackage(c *web.Context) error {
	req := c.Request()
	if err := req.ParseMultipartForm(32 << 20); err != nil {
		return errcode.ErrBadRequest
	}
	if req.MultipartForm != nil {
		defer req.MultipartForm.RemoveAll()
	}
	f, _, err := req.FormFile("file")
	if err != nil {
		return errcode.ErrBadRequest
	}
	defer f.Close()
	data, err := archiveUpload(io.LimitReader(f, (32<<20)+1))
	if err != nil {
		return errcode.ErrBadRequest.Wrap(err)
	}
	id := uuid.Nil
	if raw := c.Param("id"); raw != "" {
		id, err = uuid.Parse(raw)
		if err != nil {
			return errcode.ErrBadRequest
		}
	}
	out, err := h.service.SavePersonalPackage(req.Context(), middleware.GetUser(c), id, data, nil)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
func (h *Handler) ListSkills(c *web.Context, req skillListQuery) error {
	out, err := h.service.Skills(c.Request().Context(), middleware.GetUser(c), req.TeamID)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(struct {
		Items []SkillView `json:"items"`
	}{out})
}
func (h *Handler) CreateDraft(c *web.Context, req DraftSkillRequest) error {
	out, err := h.service.SaveDraft(c.Request().Context(), middleware.GetUser(c), uuid.Nil, req)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
func (h *Handler) GetSkill(c *web.Context, req domain.IDReq[uuid.UUID]) error {
	out, err := h.service.Skill(c.Request().Context(), middleware.GetUser(c), req.ID)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
func (h *Handler) UpdateDraft(c *web.Context, req skillDraftUpdate) error {
	out, err := h.service.SaveDraft(c.Request().Context(), middleware.GetUser(c), req.ID, req.DraftSkillRequest)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
func (h *Handler) Rollback(c *web.Context, req skillRollbackUpdate) error {
	out, err := h.service.RollbackSkill(c.Request().Context(), middleware.GetUser(c), req.ID, req.SkillRollbackRequest)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
func (h *Handler) ListReviews(c *web.Context, req skillReviewQuery) error {
	out, err := h.service.Reviews(c.Request().Context(), middleware.GetUser(c), req.TeamID)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(struct {
		Items []SkillReview `json:"items"`
	}{out})
}
func (h *Handler) GetReview(c *web.Context, req skillReviewQuery) error {
	out, err := h.service.ReviewDetail(c.Request().Context(), middleware.GetUser(c), req.ID, req.TeamID)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
