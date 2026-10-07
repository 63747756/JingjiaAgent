package v1

import (
	"context"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/GoYoko/web"
	"net"
	"net/http"
	"time"
)

func (h *AuthHandler) requireNonADLogin(c *web.Context) error {
	if h.adUsecase == nil {
		return nil
	}
	return h.adUsecase.RequireNonADLogin(c.Request().Context())
}

// AuthConfig 获取公开登录方式。
// @Summary 获取登录方式
// @Tags 【用户】企业团队成员认证
// @Produce json
// @Success 200 {object} web.Resp{data=domain.AuthConfigResp}
// @Router /api/v1/users/auth-config [get]
func (h *AuthHandler) AuthConfig(c *web.Context) error {
	if h.adUsecase == nil {
		return errcode.ErrADUnavailable
	}
	r, e := h.adUsecase.PublicConfig(c.Request().Context())
	if e != nil {
		return e
	}
	return c.Success(r)
}

// ADLogin 使用短域账号登录并同步最近 OU。
// @Summary AD 域账号登录
// @Tags 【用户】企业团队成员认证
// @Accept json
// @Produce json
// @Param req body domain.ADLoginReq true "域账号登录"
// @Success 200 {object} web.Resp{data=domain.User}
// @Router /api/v1/users/ad-login [post]
func (h *AuthHandler) ADLogin(c *web.Context, req domain.ADLoginReq) error {
	if h.adUsecase == nil {
		return errcode.ErrADUnavailable
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 15*time.Second)
	defer cancel()
	c.SetRequest(c.Request().WithContext(ctx))
	if h.config.Security.CaptchaEnabled && !h.captcha.ValidateToken(ctx, req.CaptchaToken) {
		return errcode.ErrForbidden
	}
	u, e := h.adUsecase.Login(ctx, &req, adLoginSourceIP(c.Request()))
	if e != nil {
		return e
	}
	if _, e = h.authMiddleware.Session.Save(c, consts.JingjiaAgentAISession, u.ID, u); e != nil {
		return errcode.ErrInternalServer
	}
	return c.Success(u)
}

// A caller-controlled forwarding header must not reset the source limiter.
// Reverse proxies share this connection-IP budget until explicitly trusted.
func adLoginSourceIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return "unknown"
}
