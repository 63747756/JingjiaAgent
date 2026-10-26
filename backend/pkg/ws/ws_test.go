package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/coder/websocket"
	"github.com/labstack/echo/v4"
)

func TestAcceptRejectsDifferentPortAndScheme(t *testing.T) {
	for _, origin := range []string{"http://console.test:31000", "https://console.test", "null"} {
		r := httptest.NewRequest(http.MethodGet, "http://console.test/ws", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "console.test:31000")
		w := httptest.NewRecorder()
		if _, err := Accept(w, r); err == nil || w.Code != http.StatusForbidden {
			t.Fatalf("accepted origin %s: %d %v", origin, w.Code, err)
		}
	}
}

func TestAcceptUsesConfiguredConsoleOriginBehindTLSGateway(t *testing.T) {
	e := echo.New()
	e.Pre(middleware.BrowserOrigin("https://console.example.test:443/"))
	e.GET("/ws", func(c echo.Context) error {
		conn, err := Accept(c.Response(), c.Request())
		if err != nil {
			return err
		}
		defer conn.Conn().CloseNow()
		_, _ = conn.ReadMessage()
		return nil
	})
	server := httptest.NewServer(e)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://console.example.test"}}})
	if err != nil {
		t.Fatal("valid public HTTPS origin rejected on internal HTTP hop", err)
	}
	_ = conn.CloseNow()
}

func TestAcceptAllowsSameOriginAndNativeClientAndUsesSocketIP(t *testing.T) {
	addresses := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Accept(w, r)
		if err != nil {
			return
		}
		defer conn.Conn().CloseNow()
		addresses <- conn.IP()
		_, _ = conn.ReadMessage()
	}))
	defer server.Close()
	for _, origin := range []string{"", server.URL} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		headers := http.Header{"X-Real-IP": []string{"203.0.113.99"}}
		if origin != "" {
			headers.Set("Origin", origin)
		}
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPHeader: headers})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if address := <-addresses; address != "127.0.0.1" {
			t.Fatal("untrusted header became client address", address)
		}
		_ = conn.CloseNow()
		cancel()
	}
}
