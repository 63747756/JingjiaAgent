package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GoYoko/web"
	"github.com/samber/do"

	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/brand"
)

type serverConfigProviderStub struct {
	info domain.ServerConfig
}

func (s serverConfigProviderStub) GetServerConfig(context.Context) (domain.ServerConfig, error) {
	return s.info, nil
}

func TestServerRegistersConfigRoute(t *testing.T) {
	injector := do.New()
	w := web.New()
	do.ProvideValue(injector, w)
	do.ProvideValue(injector, slog.New(slog.NewTextHandler(io.Discard, nil)))
	do.ProvideValue[domain.ServerConfigProvider](injector, serverConfigProviderStub{})

	ProvideServer(injector)
	InvokeServer(injector)

	if !hasRoute(w, http.MethodGet, "/api/v1/server/config") {
		t.Fatal("GET /api/v1/server/config route is not registered")
	}
}

func TestServerUsesLocalConfigWithoutProvider(t *testing.T) {
	injector := do.New()
	w := web.New()
	do.ProvideValue(injector, w)
	do.ProvideValue(injector, slog.New(slog.NewTextHandler(io.Discard, nil)))
	do.ProvideValue(injector, &config.Config{})

	ProvideServer(injector)
	InvokeServer(injector)

	if !hasRoute(w, http.MethodGet, "/api/v1/server/config") {
		t.Fatal("GET /api/v1/server/config must be available in a private installation")
	}
}

func TestServerConfigReturnsInjectedProviderInfo(t *testing.T) {
	injector := do.New()
	w := web.New()
	do.ProvideValue(injector, w)
	do.ProvideValue(injector, slog.New(slog.NewTextHandler(io.Discard, nil)))
	do.ProvideValue[domain.ServerConfigProvider](injector, serverConfigProviderStub{
		info: domain.ServerConfig{
			Edition:        domain.ProductEditionSaaS,
			Region:         domain.ProductRegionCN,
			CurrentVersion: "v1.2.3",
			LatestVersion:  "v1.2.4",
		},
	})

	ProvideServer(injector)
	InvokeServer(injector)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/config", nil)
	rec := httptest.NewRecorder()
	w.Echo().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Code    int                 `json:"code"`
		Message string              `json:"message"`
		Data    domain.ServerConfig `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Code != 0 || resp.Message != "success" {
		t.Fatalf("response meta = (%d, %q), want (0, success)", resp.Code, resp.Message)
	}
	if resp.Data.Edition != domain.ProductEditionSaaS || resp.Data.Region != domain.ProductRegionCN {
		t.Fatalf("data = %+v", resp.Data)
	}
	if resp.Data.CurrentVersion != brand.Version() || resp.Data.LatestVersion != "" {
		t.Fatalf("versions = (%q, %q), want current build revision and no unconfigured upgrade", resp.Data.CurrentVersion, resp.Data.LatestVersion)
	}
}

func hasRoute(w *web.Web, method, path string) bool {
	for _, route := range w.Routes() {
		if route.Method == method && route.Path == path {
			return true
		}
	}
	return false
}
