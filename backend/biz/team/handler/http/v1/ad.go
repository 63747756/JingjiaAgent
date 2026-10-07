package v1

import (
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/GoYoko/web"
	"github.com/samber/do"
)

type TeamADHandler struct{ usecase domain.TeamADUsecase }

func NewTeamADHandler(i *do.Injector) (*TeamADHandler, error) {
	h := &TeamADHandler{usecase: do.MustInvoke[domain.TeamADUsecase](i)}
	w := do.MustInvoke[*web.Web](i)
	auth := do.MustInvoke[*middleware.AuthMiddleware](i)
	g := w.Group("/api/v1/teams/ad")
	g.GET("", web.BaseHandler(h.Get), auth.TeamAuth())
	g.PUT("", web.BindHandler(h.Save), auth.TeamAuth())
	g.POST("/test", web.BindHandler(h.Test), auth.TeamAuth())
	return h, nil
}

// Get 获取 AD 配置。
// @Summary 获取 AD 域配置
// @Tags 【Team 管理员】AD 登录
// @Security JingjiaAgentAITeamAuth
// @Produce json
// @Success 200 {object} web.Resp{data=domain.TeamADConfigResp}
// @Router /api/v1/teams/ad [get]
func (h *TeamADHandler) Get(c *web.Context) error {
	r, e := h.usecase.GetConfig(c.Request().Context(), middleware.GetTeamUser(c))
	if e != nil {
		return e
	}
	return c.Success(r)
}

// Save 保存 AD 配置。
// @Summary 保存 AD 域配置
// @Tags 【Team 管理员】AD 登录
// @Security JingjiaAgentAITeamAuth
// @Accept json
// @Produce json
// @Param req body domain.SaveTeamADConfigReq true "AD 配置"
// @Success 200 {object} web.Resp{data=domain.TeamADConfigResp}
// @Router /api/v1/teams/ad [put]
func (h *TeamADHandler) Save(c *web.Context, req domain.SaveTeamADConfigReq) error {
	r, e := h.usecase.SaveConfig(c.Request().Context(), middleware.GetTeamUser(c), &req)
	if e != nil {
		return e
	}
	return c.Success(r)
}

// Test 测试 AD 连接。
// @Summary 测试 AD 域连接
// @Tags 【Team 管理员】AD 登录
// @Security JingjiaAgentAITeamAuth
// @Accept json
// @Produce json
// @Param req body domain.SaveTeamADConfigReq true "候选配置"
// @Success 200 {object} web.Resp{data=domain.TeamADTestResp}
// @Router /api/v1/teams/ad/test [post]
func (h *TeamADHandler) Test(c *web.Context, req domain.SaveTeamADConfigReq) error {
	r, e := h.usecase.TestConfig(c.Request().Context(), middleware.GetTeamUser(c), &req)
	if e != nil {
		return e
	}
	return c.Success(r)
}
