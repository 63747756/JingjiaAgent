package work

import (
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/GoYoko/web"
)

type hubMarketQuery struct {
	Q    string `query:"q"`
	Page int    `query:"page"`
	Size int    `query:"size"`
}
type hubDetailQuery struct {
	Namespace string `param:"namespace"`
	Slug      string `param:"slug"`
}

func (h *Handler) registerSkillHub(g *web.Group) {
	g.GET("/skillhub/status", web.BaseHandler(h.SkillHubStatus))
	g.POST("/skillhub/binding", web.BindHandler(h.BindSkillHub))
	g.DELETE("/skillhub/binding", web.BaseHandler(h.UnbindSkillHub))
	g.GET("/skillhub/market", web.BindHandler(h.SkillHubMarket))
	g.GET("/skillhub/market/:namespace/:slug", web.BindHandler(h.SkillHubDetail))
	g.POST("/skillhub/install", web.BindHandler(h.InstallSkillHub))
	g.POST("/skills/:id/skillhub/validate", web.BindHandler(h.ValidateSkillHubUpload))
	g.POST("/skills/:id/skillhub/upload", web.BindHandler(h.UploadSkillHub))
}
func (h *Handler) SkillHubStatus(c *web.Context) error {
	out, err := h.service.HubStatus(c.Request().Context(), middleware.GetUser(c))
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
func (h *Handler) BindSkillHub(c *web.Context, r HubBindRequest) error {
	out, err := h.service.BindHub(c.Request().Context(), middleware.GetUser(c), r)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
func (h *Handler) UnbindSkillHub(c *web.Context) error {
	if err := h.service.UnbindHub(c.Request().Context(), middleware.GetUser(c)); err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(true)
}
func (h *Handler) SkillHubMarket(c *web.Context, r hubMarketQuery) error {
	out, err := h.service.HubMarket(c.Request().Context(), r.Q, r.Page, r.Size)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
func (h *Handler) SkillHubDetail(c *web.Context, r hubDetailQuery) error {
	out, err := h.service.HubDetail(c.Request().Context(), r.Namespace, r.Slug)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
func (h *Handler) InstallSkillHub(c *web.Context, r HubInstallRequest) error {
	out, err := h.service.InstallHubSkill(c.Request().Context(), middleware.GetUser(c), r)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
func (h *Handler) ValidateSkillHubUpload(c *web.Context, r HubUploadRequest) error {
	out, err := h.service.PrecheckHubUpload(c.Request().Context(), middleware.GetUser(c), r)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
func (h *Handler) UploadSkillHub(c *web.Context, r HubUploadRequest) error {
	out, err := h.service.UploadHubSkill(c.Request().Context(), middleware.GetUser(c), r)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
