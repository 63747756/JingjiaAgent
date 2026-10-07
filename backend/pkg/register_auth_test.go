package pkg

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/samber/do"
)

type wiredAuthUserLookup struct {
	domain.UserUsecase
	user  *domain.User
	calls int
}

func (s *wiredAuthUserLookup) Get(context.Context, uuid.UUID) (*domain.User, error) {
	s.calls++
	return s.user, nil
}

func TestProductionAuthFactoryWiresLocalAccountLookup(t *testing.T) {
	i := do.New()
	if err := RegisterInfra(i); err != nil {
		t.Fatal(err)
	}
	mr := miniredis.RunT(t)
	host, port, _ := net.SplitHostPort(mr.Addr())
	cfg := &config.Config{}
	cfg.Redis.Host = host
	cfg.Redis.Port, _ = strconv.Atoi(port)
	cfg.Session.ExpireDay = 30
	do.ProvideValue(i, cfg)
	do.OverrideValue(i, slog.New(slog.NewTextHandler(io.Discard, nil)))
	lookup := &wiredAuthUserLookup{user: &domain.User{ID: uuid.New(), Role: consts.UserRoleSubAccount, Status: consts.UserStatusActive}}
	do.ProvideValue[domain.UserUsecase](i, lookup)
	a := do.MustInvoke[*middleware.AuthMiddleware](i)
	e := echo.New()
	save := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest("POST", "/login", nil), save)
	if _, err := a.Session.Save(c, consts.JingjiaAgentAISession, lookup.user.ID, lookup.user); err != nil {
		t.Fatal(err)
	}
	e.GET("/protected", func(c echo.Context) error { return c.NoContent(http.StatusOK) }, a.Auth())
	r := httptest.NewRequest("GET", "/protected", nil)
	r.AddCookie(save.Result().Cookies()[0])
	result := httptest.NewRecorder()
	e.ServeHTTP(result, r)
	if result.Code != http.StatusOK || lookup.calls != 1 {
		t.Fatalf("production auth wiring status=%d lookup calls=%d", result.Code, lookup.calls)
	}
}
