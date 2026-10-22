package work

import (
	"errors"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/GoYoko/web"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/samber/do"
	"net/http"
)

type Handler struct{ service *Service }

func NewHandler(i *do.Injector) (*Handler, error) {
	h := &Handler{service: do.MustInvoke[*Service](i)}
	h.service.startBuiltinSkills()
	w := do.MustInvoke[*web.Web](i)
	auth := do.MustInvoke[*middleware.AuthMiddleware](i)
	active := do.MustInvoke[*middleware.TargetActiveMiddleware](i)
	g := w.Group("/api/v1/users/work-tasks")
	g.Use(auth.Auth(), active.TargetActive())
	g.POST("", web.BindHandler(h.Create))
	g.GET("", web.BindHandler(h.List))
	g.GET("/:id", web.BindHandler(h.Get))
	c := w.Group("/api/v1/users/work")
	c.Use(auth.Auth(), active.TargetActive())
	c.GET("/model-options", web.BindHandler(h.ModelOptions))
	c.GET("/config", web.BaseHandler(h.Config))
	c.GET("/navigation/pins", web.BaseHandler(h.navigationPins))
	c.PUT("/navigation/pins", web.BindHandler(h.setNavigationPin))
	c.GET("/navigation/tasks/:id", web.BindHandler(h.navigationTask))
	library := w.Group("/api/v1/users/task-library")
	library.Use(auth.Auth(), active.TargetActive())
	library.GET("", web.BindHandler(h.UnifiedTasks))
	a := w.Group("/api/v1/users/work")
	a.Use(auth.TeamAuth(), h.managerContext, active.TargetActive())
	a.GET("/profiles", web.BaseHandler(h.Profiles))
	a.POST("/profiles", web.BindHandler(h.CreateProfile))
	a.PUT("/profiles/default", web.BindHandler(h.SetDefaultProfile))
	a.PUT("/profiles/:id", web.BindHandler(h.UpdateProfile))
	a.PUT("/profiles/:id/status", web.BindHandler(h.SetProfileStatus))
	a.POST("/skill-imports", web.BindHandler(h.Import))
	r := w.Group("/api/v1/users/work")
	r.Use(auth.Auth(), active.TargetActive())
	r.GET("/skill-packages", web.BaseHandler(h.Packages))
	h.registerSkills(c, a, r)
	h.registerSkillHub(c)
	managerRead := w.Group("/api/v1/teams/work")
	managerRead.Use(auth.TeamAuth(), h.managerContext, active.TargetActive())
	managerRead.GET("/skills", web.BindHandler(h.ListSkills))
	managerRead.GET("/skills/:id", web.BindHandler(h.GetSkill))
	managerRead.GET("/skill-packages", web.BaseHandler(h.Packages))
	return h, nil
}

func (h *Handler) UnifiedTasks(c *web.Context, req ListRequest) error {
	out, err := h.service.UnifiedTasks(c.Request().Context(), middleware.GetUser(c), req)
	if err != nil {
		return err
	}
	return c.Success(out)
}

func (h *Handler) managerContext(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := middleware.GetTeamUser(c)
		if u == nil || u.User == nil || u.Team == nil {
			return c.String(http.StatusForbidden, "Forbidden")
		}
		if err := h.service.teamPermission(c.Request().Context(), u.User.ID, u.Team.ID, true); err != nil {
			if errors.Is(err, errcode.ErrForbidden) || errors.Is(err, errcode.ErrBadRequest) {
				return c.String(http.StatusForbidden, "Forbidden")
			}
			return c.String(http.StatusServiceUnavailable, "Authorization temporarily unavailable")
		}
		middleware.SetUser(c, u.User)
		return next(c)
	}
}

func (h *Handler) Create(c *web.Context, req CreateRequest) error {
	out, err := h.service.Create(c.Request().Context(), middleware.GetUser(c), req)
	if err != nil {
		return skillHandlerError(c, err)
	}
	return c.Success(out)
}
func (h *Handler) List(c *web.Context, req ListRequest) error {
	out, err := h.service.List(c.Request().Context(), middleware.GetUser(c), req)
	if err != nil {
		return err
	}
	return c.Success(out)
}
func (h *Handler) Get(c *web.Context, req domain.IDReq[uuid.UUID]) error {
	out, err := h.service.Get(c.Request().Context(), middleware.GetUser(c), req.ID)
	if err != nil {
		return err
	}
	return c.Success(out)
}
func (h *Handler) Config(c *web.Context) error {
	out, err := h.service.Config(c.Request().Context(), middleware.GetUser(c))
	if err != nil {
		return err
	}
	return c.Success(out)
}
func (h *Handler) Profiles(c *web.Context) error {
	out, err := h.service.Profiles(c.Request().Context(), middleware.GetUser(c))
	if err != nil {
		return err
	}
	return c.Success(struct {
		Items []Profile `json:"items"`
	}{out})
}
func (h *Handler) CreateProfile(c *web.Context, req Profile) error {
	req.ID = uuid.Nil
	out, err := h.service.SaveProfile(c.Request().Context(), middleware.GetUser(c), req)
	if err != nil {
		return err
	}
	return c.Success(out)
}
func (h *Handler) UpdateProfile(c *web.Context, req ProfileRequest) error {
	req.Profile.ID = req.ID
	out, err := h.service.SaveProfile(c.Request().Context(), middleware.GetUser(c), req.Profile)
	if err != nil {
		return err
	}
	return c.Success(out)
}

func (h *Handler) SetDefaultProfile(c *web.Context, req DefaultProfileRequest) error {
	if err := h.service.SetDefaultProfile(c.Request().Context(), middleware.GetUser(c), req); err != nil {
		return err
	}
	return c.Success(req)
}

func (h *Handler) SetProfileStatus(c *web.Context, req ProfileStatusRequest) error {
	out, err := h.service.SetProfileStatus(c.Request().Context(), middleware.GetUser(c), req)
	if err != nil {
		return err
	}
	return c.Success(out)
}
