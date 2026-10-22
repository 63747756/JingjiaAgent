package llmproxy

import (
	"log/slog"

	"github.com/GoYoko/web"
	"github.com/samber/do"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/pkg/modelusage"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

type Handler struct {
	proxy *Proxy
}

func ProvideLLMProxy(i *do.Injector) {
	do.Provide(i, NewHandler)
}

func InvokeLLMProxy(i *do.Injector) {
	do.MustInvoke[*Handler](i)
}

func NewHandler(i *do.Injector) (*Handler, error) {
	w := do.MustInvoke[*web.Web](i)
	client := do.MustInvoke[*db.Client](i)
	logger := do.MustInvoke[*slog.Logger](i)
	var opts []Option
	if cfg, err := do.Invoke[*config.Config](i); err == nil && cfg != nil {
		opts = append(opts, WithPrivateNetworkBlocked(cfg.Security.BlockPrivateNetwork))
		opts = append(opts, WithOfflineWork(cfg.Work))
	}
	if recorder, err := do.Invoke[*modelusage.Recorder](i); err == nil {
		opts = append(opts, WithUsageRecorder(recorder))
	}
	if runtime, err := do.Invoke[taskflow.Clienter](i); err == nil {
		if recorder, ok := runtime.(taskflow.ContextUsageRecorder); ok {
			opts = append(opts, WithContextUsageRecorder(recorder))
		}
	}

	h := &Handler{proxy: NewProxy(client, logger, opts...)}
	g := w.Group("/v1")
	g.POST("/chat/completions", web.BaseHandler(h.ServeHTTP))
	g.POST("/responses", web.BaseHandler(h.ServeHTTP))
	g.POST("/messages", web.BaseHandler(h.ServeHTTP))
	return h, nil
}

func (h *Handler) ServeHTTP(c *web.Context) error {
	h.proxy.ServeHTTP(c.Response(), c.Request())
	return nil
}
