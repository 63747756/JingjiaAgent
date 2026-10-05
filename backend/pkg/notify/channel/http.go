package channel

import (
	"context"
	"net/http"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/netguard"
	"github.com/63747756/jingjiaagent/backend/pkg/request"
)

type apiResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
}

func postURL[T any](ctx context.Context, cfg *ChannelConfig, rawURL string, body any, opts ...request.Opt) (*T, error) {
	client := netguard.New(cfg.BlockPrivateNetwork).HTTPClient(&http.Client{Timeout: 30 * time.Second})
	opts = append(opts, request.WithHTTPClient(client))
	return request.PostURL[T](ctx, rawURL, body, opts...)
}
