package middleware

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
)

type browserOriginKey struct{}
type browserOriginPolicy struct {
	configured bool
	origin     string
}

// BrowserOrigin protects cookie-authenticated writes from applications served
// on another preview port. Cookies and SameSite do not isolate TCP ports.
// PublicURL is deployment configuration, never a forwarded request header.
func BrowserOrigin(publicURL string) echo.MiddlewareFunc {
	policy := browserOriginPolicy{configured: strings.TrimSpace(publicURL) != "", origin: canonicalBrowserOrigin(publicURL, true)}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			r := c.Request()
			r = r.WithContext(context.WithValue(r.Context(), browserOriginKey{}, policy))
			c.SetRequest(r)
			unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
			if (unsafe || strings.EqualFold(r.Header.Get("Upgrade"), "websocket")) && !CheckBrowserOrigin(r) {
				return c.JSON(http.StatusForbidden, map[string]string{"message": "不允许从其他网页来源操作平台，请从平台页面重试"})
			}
			return next(c)
		}
	}
}

// CheckBrowserOrigin is also called at the WebSocket boundary. Requests from
// executors/CLI have no browser headers and retain their own token checks.
func CheckBrowserOrigin(r *http.Request) bool {
	policy, _ := r.Context().Value(browserOriginKey{}).(browserOriginPolicy)
	expected := policy.origin
	if !policy.configured {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		expected = canonicalBrowserOrigin(scheme+"://"+r.Host, false)
	}
	if values := r.Header.Values("Origin"); len(values) > 0 {
		return len(values) == 1 && expected != "" && canonicalBrowserOrigin(values[0], false) == expected
	}
	// Fetch Metadata cannot be set by application JavaScript. Same-site still
	// includes a different port, so it is not sufficient for authenticated writes.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if referer := r.Header.Get("Referer"); referer != "" {
		return expected != "" && canonicalBrowserOrigin(referer, true) == expected
	}
	return true
}

func canonicalBrowserOrigin(raw string, allowPath bool) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	if !allowPath && (u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if strings.ContainsAny(host, "*?\\% \t\r\n") {
		return ""
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return ""
		}
		port = strconv.Itoa(n)
	}
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return u.Scheme + "://" + net.JoinHostPort(host, port)
}
