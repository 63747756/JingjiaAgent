package v1

import (
	"log/slog"
	"context"

	"github.com/GoYoko/web"
	"github.com/samber/do"

	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/brand"
)

type ServerConfigHandler struct {
	provider domain.ServerConfigProvider
	logger   *slog.Logger
}

func NewServerConfigHandler(i *do.Injector) (*ServerConfigHandler, error) {
	w := do.MustInvoke[*web.Web](i)
	provider, err := do.Invoke[domain.ServerConfigProvider](i)
	h := &ServerConfigHandler{
		provider: provider,
		logger:   do.MustInvoke[*slog.Logger](i).With("handler", "server.config"),
	}
	if err != nil { h.provider = localServerConfig{cfg: do.MustInvoke[*config.Config](i)} }

	w.Group("/api/v1/server").GET("/config", web.BaseHandler(h.Get))
	return h, nil
}

// Get 获取服务配置
//
//	@Summary		获取服务配置
//	@Description	返回当前服务的产品形态、SaaS 区域和版本信息，用于前端区分部署形态和升级状态。
//	@Tags			【服务】配置信息
//	@Accept			json
//	@Produce		json
//	@Success		200	{object}	web.Resp{data=domain.ServerConfig}	"获取成功"
//	@Failure		500	{object}	web.Resp							"服务器内部错误"
//	@Router			/api/v1/server/config [get]
func (h *ServerConfigHandler) Get(c *web.Context) error {
	info, err := h.provider.GetServerConfig(c.Request().Context())
	if err != nil {
		h.logger.ErrorContext(c.Request().Context(), "get server config failed", "error", err)
		return err
	}
	info.CurrentVersion = brand.Version()
	info.LatestVersion = ""
	return c.Success(info)
}

type localServerConfig struct { cfg *config.Config }
func (p localServerConfig) GetServerConfig(context.Context) (domain.ServerConfig, error) {
 return domain.ServerConfig{Edition: domain.ProductEditionPrivate, CurrentVersion: brand.Version(), CaptchaEnabled: p.cfg.Security.CaptchaEnabled}, nil
}
