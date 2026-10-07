package v1

import (
	"encoding/json"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/GoYoko/web"
	"github.com/GoYoko/web/locale"
	"github.com/labstack/echo/v4"
	"golang.org/x/text/language"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestADLoginSourceIgnoresForgedForwardingHeaders(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/v1/users/ad-login", nil)
	r.RemoteAddr = "192.0.2.10:12345"
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP"} {
		r.Header.Set(header, "198.51.100.99")
	}
	if actual := adLoginSourceIP(r); actual != "192.0.2.10" {
		t.Fatalf("forwarding header changed source budget: %s", actual)
	}
	r.RemoteAddr = "[2001:db8::1]:23456"
	if actual := adLoginSourceIP(r); actual != "2001:db8::1" {
		t.Fatalf("IPv6 source: %s", actual)
	}
}

func TestADPasswordChangeReturnsExplicitPolicyError(t *testing.T) {
	w := web.New()
	w.SetLocale(locale.NewLocalizerWithFile(language.Chinese, errcode.LocalFS, []string{"locale.zh.toml", "locale.en.toml"}))
	h := &AuthHandler{}
	w.Group("/password").PUT("", web.BindHandler(h.ChangePassword), func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { middleware.SetUser(c, &domain.User{AuthSource: "ad"}); return next(c) }
	})
	r := httptest.NewRequest(http.MethodPut, "/password", strings.NewReader(`{"new_password":"valid-password"}`))
	r.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	w.Echo().ServeHTTP(response, r)
	var result struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusForbidden || result.Code != 10648 {
		t.Fatalf("policy status=%d code=%d", response.Code, result.Code)
	}
}
