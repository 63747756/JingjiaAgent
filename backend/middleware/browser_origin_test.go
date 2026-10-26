package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestBrowserOriginProtectsCookieWritesAcrossPortsAndSchemes(t *testing.T) {
	for _, tt := range []struct {
		name, public, method, origin, referer, site string
		allowed                                     bool
	}{
		{"console TLS terminated at proxy", "https://192.0.2.1", "POST", "https://192.0.2.1", "", "same-origin", true},
		{"default port normalized", "https://console.test:443/", "PUT", "https://console.test", "", "same-origin", true},
		{"preview different port", "http://192.0.2.1", "POST", "http://192.0.2.1:31000", "", "same-site", false},
		{"preview other scheme", "https://192.0.2.1", "POST", "http://192.0.2.1", "", "same-site", false},
		{"domain subdomain", "https://console.test", "DELETE", "https://x.console.test", "", "same-site", false},
		{"opaque sandbox", "https://console.test", "POST", "null", "", "cross-site", false},
		{"malformed origin path", "https://console.test", "POST", "https://console.test/path", "", "", false},
		{"forged user info", "https://console.test", "POST", "https://console.test@evil.test", "", "", false},
		{"IPv6 exact", "http://[2001:db8::1]:8080", "POST", "http://[2001:db8::1]:8080", "", "", true},
		{"native MCP", "https://console.test", "POST", "", "", "", true},
		{"OAuth GET navigation", "https://console.test", "GET", "https://gitlab.test", "", "cross-site", true},
		{"preflight", "https://console.test", "OPTIONS", "https://evil.test", "", "cross-site", true},
		{"same-site browser without origin", "https://console.test", "POST", "", "", "same-site", false},
		{"cross-origin referer fallback", "http://192.0.2.1", "POST", "", "http://192.0.2.1:31000/app", "", false},
		{"same-origin referer fallback", "http://192.0.2.1", "POST", "", "http://192.0.2.1/app?a=b", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			e.Pre(BrowserOrigin(tt.public))
			e.Any("/test", func(c echo.Context) error { return c.NoContent(204) })
			r := httptest.NewRequest(tt.method, "http://backend:8888/test", nil)
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if tt.referer != "" {
				r.Header.Set("Referer", tt.referer)
			}
			if tt.site != "" {
				r.Header.Set("Sec-Fetch-Site", tt.site)
			}
			// These must never replace configured identity or transport scheme.
			r.Header.Set("X-Forwarded-Host", "evil.test")
			r.Header.Set("X-Forwarded-Proto", "http")
			w := httptest.NewRecorder()
			e.ServeHTTP(w, r)
			if (w.Code == 204) != tt.allowed {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestBrowserOriginRejectsMultipleOriginsAndCrossPortWebsocket(t *testing.T) {
	e := echo.New()
	e.Pre(BrowserOrigin("https://console.test"))
	e.GET("/ws", func(c echo.Context) error { return c.NoContent(204) })
	for _, origins := range [][]string{{"https://console.test:31001"}, {"https://console.test", "https://evil.test"}} {
		r := httptest.NewRequest(http.MethodGet, "http://backend:8888/ws", nil)
		r.Header.Set("Upgrade", "websocket")
		for _, origin := range origins {
			r.Header.Add("Origin", origin)
		}
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("websocket cross-origin request accepted", w.Code)
		}
	}
}
