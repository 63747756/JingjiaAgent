package pkg

import (
	"github.com/GoYoko/web"
	"github.com/chaitin/MonkeyCode/backend/errcode"
	"github.com/samber/do"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStandaloneWebLoadsProjectErrorMessages(t *testing.T) {
	i := do.New()
	if err := RegisterInfra(i); err != nil {
		t.Fatal(err)
	}
	w := do.MustInvoke[*web.Web](i)
	w.Group("/locale-test").GET("", web.BaseHandler(func(c *web.Context) error { return errcode.ErrRuntimeInstallToken }))
	response := httptest.NewRecorder()
	w.Echo().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/locale-test", nil))
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "安装票据") || strings.Contains(response.Body.String(), "not found") {
		t.Fatal("standalone error response omitted configured Chinese message")
	}
}
