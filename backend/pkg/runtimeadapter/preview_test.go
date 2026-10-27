package runtimeadapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	rpc "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

func installPreviewTunnel(t *testing.T, mux *http.ServeMux, target string) {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("/internal/jingjiaagent/tcp/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "denied", 401)
			return
		}
		tcp, err := net.Dial("tcp", u.Host)
		if err != nil {
			http.Error(w, "failed", 502)
			return
		}
		defer tcp.Close()
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn := &previewConn{ws: ws, done: make(chan struct{})}
		defer conn.Close()
		done := make(chan struct{})
		go func() {
			io.Copy(conn, tcp)
			_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			conn.Close()
			close(done)
		}()
		io.Copy(tcp, conn)
		tcp.Close()
		<-done
	})
}

func TestPreviewIngressProtocolsAndTrust(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		c := &Client{preview: config.RuntimePreview{Mode: "domain", BaseURL: scheme + "://preview.example.net:8444", TrustedProxies: []string{"127.0.0.1/32"}}}
		service := NewPreviewService(c, nil)
		defer service.cancel()
		f := portForward{ID: uuid.NewString()}
		u := c.previewURL(f)
		if u.Host != f.ID+".preview.example.net:8444" {
			t.Fatal("route ID not used in domain", u)
		}
		r := httptest.NewRequest("GET", u.String(), nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Forwarded-Proto", scheme)
		if id, ok := service.ingress(r); !ok || id != f.ID {
			t.Fatal("valid ingress denied", scheme, id)
		}
		for _, change := range []func(*http.Request){
			func(r *http.Request) { r.RemoteAddr = "192.0.2.2:4444" },
			func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "invalid") },
			func(r *http.Request) { r.Host = "attacker.example:8444" },
			func(r *http.Request) { r.Host = "not-a-uuid.preview.example.net:8444" },
			func(r *http.Request) { r.Host = f.ID + ".preview.example.net:8445" },
			func(r *http.Request) { r.Host = f.ID + ".preview.example.net.attacker:8444" },
		} {
			bad := r.Clone(context.Background())
			change(bad)
			if _, ok := service.ingress(bad); ok {
				t.Fatal("spoofed ingress accepted")
			}
		}
		for _, host := range []string{u.Host, "preview.example.net:8444"} {
			probe := r.Clone(context.Background())
			probe.Host = host
			probe.URL.Path = previewHealthPath
			w := httptest.NewRecorder()
			service.ServeHTTP(w, probe)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "jingjiaagent-preview") {
				t.Fatal("domain gateway health failed", host, w.Code)
			}
		}
	}
}

func TestPreviewDomainRouteReuseAndExplicitReopen(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer app.Close()
	mux := http.NewServeMux()
	installPreviewTunnel(t, mux, app.URL)
	path, h := rpc.NewExecServiceHandler(&previewExecFixture{})
	mux.Handle(path, h)
	c, env, owner := previewFixture(t, testEngine(t, mux))
	ctx := context.Background()
	info, err := c.RequestEnvironmentPreview(ctx, env, owner, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if info.AccessURL == nil || *info.AccessURL != "http://"+*info.ForwardID+".localhost:47421" || info.AccessMode != "public" {
		t.Fatal("wrong public domain route", info)
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := c.RequestEnvironmentPreview(ctx, env, owner, 8080)
			if err != nil || got.ForwardID == nil || *got.ForwardID != *info.ForwardID || got.AccessMode != "public" {
				t.Errorf("repeat request: %+v %v", got, err)
			}
		}()
	}
	wg.Wait()
	f, err := c.forward(ctx, *info.ForwardID)
	if err != nil || f.Revision != 1 {
		t.Fatal("idempotent request revoked active route", err, f.Revision)
	}
	if err = c.PortForwarder().Close(ctx, taskflow.ClosePortForward{ID: env, ForwardID: *info.ForwardID}); err != nil {
		t.Fatal(err)
	}
	list, err := c.PortForwarder().List(ctx, taskflow.ListPortforwadReq{ID: env})
	if err != nil || len(list.Ports) != 1 || list.Ports[0].Status != "closed" {
		t.Fatal("running service disappeared after closing access", list, err)
	}
	updated, err := c.PortForwarder().Update(ctx, taskflow.UpdatePortForward{ID: env, ForwardID: *info.ForwardID, AccessMode: "ip_allowlist", WhitelistIPs: []string{}})
	if err != nil || updated.Status != "closed" || updated.AccessURL != nil || len(updated.WhitelistIPs) != 0 {
		t.Fatal("policy edit reopened closed route", err)
	}
	if _, err = c.RequestEnvironmentPreview(ctx, env, owner, 8080); err == nil {
		t.Fatal("AI reopened employee-closed route")
	}
	reopened, err := c.PortForwarder().Create(ctx, taskflow.CreatePortForward{ID: env, UserID: owner, LocalPort: 8080, Reopen: true})
	if err != nil || *reopened.ForwardID != *info.ForwardID || reopened.AccessMode != "ip_allowlist" || reopened.Success {
		t.Fatal("explicit reopen did not preserve deny-all route", reopened, err)
	}
	other := "agent_" + uuid.NewString()
	if err = c.ledger.SaveEnvironment(ctx, Environment{ID: other, OwnerID: owner, NodeID: "node", Request: taskflow.CreateVirtualMachineReq{ID: other}}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ledger.db.Exec("UPDATE runtime_environments SET state='online',sandbox_id='sandbox' WHERE id=$1", other); err != nil {
		t.Fatal(err)
	}
	another, err := c.RequestEnvironmentPreview(ctx, other, owner, 8080)
	if err != nil || *another.ForwardID == *info.ForwardID || *another.AccessURL == *info.AccessURL {
		t.Fatal("another workspace reused a route identity", another, err)
	}
}

func TestPreviewProtectedPortCannotBePublished(t *testing.T) {
	mux := http.NewServeMux()
	path, h := rpc.NewExecServiceHandler(&previewExecFixture{Ports: `[{"port":8080,"process":"opencode","protected":true}]`})
	mux.Handle(path, h)
	c, env, owner := previewFixture(t, testEngine(t, mux))
	list, err := c.PortForwarder().List(context.Background(), taskflow.ListPortforwadReq{ID: env})
	if err != nil || len(list.Ports) != 0 {
		t.Fatal("internal port disclosed as application", err)
	}
	_, err = c.RequestEnvironmentPreview(context.Background(), env, owner, 8080)
	if err == nil || !strings.Contains(err.Error(), "internal ports") {
		t.Fatal("internal port published", err)
	}
}

func TestPreviewAnonymousHTTPAndHTTPSOrigin(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			var expectedHost string
			app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/app" && (r.Header.Get("X-Forwarded-Proto") != scheme || r.Host != expectedHost) {
					t.Error("application origin lost")
				}
				w.Write([]byte("working"))
			}))
			defer app.Close()
			mux := http.NewServeMux()
			installPreviewTunnel(t, mux, app.URL)
			path, h := rpc.NewExecServiceHandler(&previewExecFixture{})
			mux.Handle(path, h)
			c, env, owner := previewFixture(t, testEngine(t, mux))
			c.preview.BaseURL = scheme + "://preview.example.net"
			info, err := c.RequestEnvironmentPreview(context.Background(), env, owner, 8080)
			if err != nil {
				t.Fatal(err)
			}
			expectedHost = *info.ForwardID + ".preview.example.net"
			service := NewPreviewService(c, func(_ context.Context, user, id string) error {
				if user != owner || id != env {
					return errors.New("wrong owner")
				}
				return nil
			})
			defer service.cancel()
			gateway := httptest.NewServer(service)
			defer gateway.Close()
			for _, mode := range []string{"public", "ip_allowlist"} {
				if _, err = c.PortForwarder().Update(context.Background(), taskflow.UpdatePortForward{ID: env, ForwardID: *info.ForwardID, AccessMode: mode, WhitelistIPs: []string{"203.0.113.7"}}); err != nil {
					t.Fatal(err)
				}
				for _, ip := range []string{"203.0.113.7", "203.0.113.8"} {
					r, _ := http.NewRequest("GET", gateway.URL+"/app", nil)
					r.Host = expectedHost
					r.Header.Set("X-Forwarded-Proto", scheme)
					r.Header.Set("X-Forwarded-For", ip)
					res, err := http.DefaultClient.Do(r)
					if err != nil {
						t.Fatal(err)
					}
					body, _ := io.ReadAll(res.Body)
					res.Body.Close()
					allowed := mode == "public" || ip == "203.0.113.7"
					if allowed && (res.StatusCode != 200 || string(body) != "working") {
						t.Fatal("anonymous public/allowed access failed", mode, ip, res.StatusCode)
					}
					if !allowed && res.StatusCode != 403 {
						t.Fatal("unlisted IP allowed", res.StatusCode)
					}
				}
			}
		})
	}
}

type previewExecFixture struct {
	rpc.UnimplementedExecServiceHandler
	Ports string
}

func (f *previewExecFixture) Exec(context.Context, *connect.Request[v2.ExecRequest]) (*connect.Response[v2.ExecResponse], error) {
	ports := f.Ports
	if ports == "" {
		ports = `[{"port":8080,"process":"python3"}]`
	}
	return connect.NewResponse(&v2.ExecResponse{Result: &v2.ExecResult{Stdout: ports}}), nil
}

func previewFixture(t *testing.T, engine *Engine) (*Client, string, string) {
	t.Helper()
	l := testLedger(t)
	env := "agent_" + uuid.NewString()
	owner := uuid.NewString()
	if err := l.SaveEnvironment(context.Background(), Environment{ID: env, OwnerID: owner, NodeID: "node", Request: taskflow.CreateVirtualMachineReq{ID: env}}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec("UPDATE runtime_environments SET state='online',sandbox_id='sandbox' WHERE id=$1", env); err != nil {
		t.Fatal(err)
	}
	return &Client{ledger: l, engines: map[string]*Engine{"node": engine}, callbackURL: "http://127.0.0.1:47420", preview: config.RuntimePreview{Mode: "domain", BaseURL: "http://localhost:47421", Listen: "127.0.0.1:47421", TrustedProxies: []string{"127.0.0.1/32"}}}, env, owner
}

func TestPreviewPortScopesAndRevision(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer upstream.Close()
	mux := http.NewServeMux()
	installPreviewTunnel(t, mux, upstream.URL)
	path, h := rpc.NewExecServiceHandler(&previewExecFixture{})
	mux.Handle(path, h)
	c, env, owner := previewFixture(t, testEngine(t, mux))
	ctx := context.Background()
	p := c.PortForwarder()
	for _, ips := range [][]string{{"bad"}, {"0.0.0.0/0"}} {
		if _, err := p.Create(ctx, taskflow.CreatePortForward{ID: env, UserID: owner, LocalPort: 8080, AccessMode: "ip_allowlist", WhitelistIPs: ips}); err == nil {
			t.Fatal("invalid whitelist accepted")
		}
	}
	if _, err := p.Create(ctx, taskflow.CreatePortForward{ID: env, UserID: uuid.NewString(), LocalPort: 8080, AccessMode: "ip_allowlist", WhitelistIPs: []string{"127.0.0.1"}}); err == nil {
		t.Fatal("foreign owner opened port")
	}
	list, err := p.List(ctx, taskflow.ListPortforwadReq{ID: env, RequestId: "list"})
	if err != nil || len(list.Ports) != 1 || list.Ports[0].Status != "reserved" || list.RequestId != "list" {
		t.Fatal("listening port discovery failed", err)
	}
	if list.Ports[0].AccessURL != nil || list.Ports[0].ForwardID != nil {
		t.Fatal("unconfigured listener advertised preview access")
	}
	info, err := p.Create(ctx, taskflow.CreatePortForward{ID: env, UserID: owner, LocalPort: 8080, AccessMode: "ip_allowlist", WhitelistIPs: []string{"127.0.0.1", "::ffff:127.0.0.1"}})
	if err != nil || !info.Success || len(info.WhitelistIPs) != 1 {
		t.Fatal("create failed", err)
	}
	id := *info.ForwardID
	list, err = p.List(ctx, taskflow.ListPortforwadReq{ID: env})
	if err != nil || len(list.Ports) != 1 || list.Ports[0].Status != "connected" || list.Ports[0].AccessURL == nil || !list.Ports[0].Success {
		t.Fatal("configured listener did not advertise preview access", err)
	}
	if _, err = p.Update(ctx, taskflow.UpdatePortForward{ID: "other", ForwardID: id, AccessMode: "ip_allowlist", WhitelistIPs: []string{"127.0.0.1"}}); err == nil {
		t.Fatal("cross-environment update accepted")
	}
	if err = p.Close(ctx, taskflow.ClosePortForward{ID: "other", ForwardID: id}); err == nil {
		t.Fatal("cross-environment close accepted")
	}
	updated, err := p.Update(ctx, taskflow.UpdatePortForward{ID: env, ForwardID: id, AccessMode: "ip_allowlist", WhitelistIPs: []string{"::1"}})
	if err != nil || updated.WhitelistIPs[0] != "::1" {
		t.Fatal("update failed", err)
	}
	f, err := c.forward(ctx, id)
	if err != nil || f.Revision != 2 {
		t.Fatal("update did not invalidate existing connections")
	}
	if err = p.Close(ctx, taskflow.ClosePortForward{ID: env, ForwardID: id}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.forward(ctx, id); err == nil {
		t.Fatal("closed forward accessible")
	}
	reopened, err := p.Create(ctx, taskflow.CreatePortForward{ID: env, UserID: owner, Reopen: true, LocalPort: 8080, AccessMode: "ip_allowlist", WhitelistIPs: []string{"127.0.0.1"}})
	if err != nil || *reopened.ForwardID != id {
		t.Fatal("reopen created duplicate forward")
	}
	f, _ = c.forward(ctx, id)
	if f.Revision != 4 {
		t.Fatal("reopen did not advance the connection revision")
	}
	if _, err = c.ledger.db.Exec("UPDATE runtime_environments SET state='hibernated' WHERE id=$1", env); err != nil {
		t.Fatal(err)
	}
	list, err = p.List(ctx, taskflow.ListPortforwadReq{ID: env})
	if err != nil || len(list.Ports) != 0 || list.Status != "stopped" {
		t.Fatal("hibernated workspace still advertised services", list, err)
	}
	saved, err := c.EnvironmentPreview(ctx, env, 8080)
	if err != nil || saved.ForwardID == nil || *saved.ForwardID != id || saved.AccessMode != "ip_allowlist" {
		t.Fatal("hibernation discarded saved route settings", saved, err)
	}
	if err = p.Close(ctx, taskflow.ClosePortForward{ID: env, ForwardID: id}); err != nil {
		t.Fatal("hibernated forward could not be closed", err)
	}
}

func TestPreviewClientIPTrustBoundary(t *testing.T) {
	c := &Client{}
	r := httptest.NewRequest("GET", "http://host/", nil)
	r.RemoteAddr = "192.0.2.20:4567"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 192.0.2.15")
	if c.PreviewClientIP(r) != "192.0.2.20" {
		t.Fatal("untrusted forwarding spoofed whitelist")
	}
	c.preview.TrustedProxies = []string{"192.0.2.0/24"}
	r.Header.Del("X-Forwarded-For")
	if c.PreviewClientIP(r) != "192.0.2.20" {
		t.Fatal("direct trusted peer without forwarding lost its IP")
	}
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 192.0.2.15")
	if c.PreviewClientIP(r) != "203.0.113.7" {
		t.Fatal("trusted ingress chain not resolved")
	}
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 198.51.100.9")
	if c.PreviewClientIP(r) != "198.51.100.9" {
		t.Fatal("untrusted inner hop allowed spoof")
	}
	r.Header.Set("X-Forwarded-For", "garbage")
	if c.PreviewClientIP(r) != "" {
		t.Fatal("malformed trusted chain accepted")
	}
}

func TestPreviewGatewayHTTPWebSocketAndRevocation(t *testing.T) {
	binary := bytes.Repeat([]byte{0, 255, 13, 10, 42}, 300000)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Cookie"), previewCookie) || strings.Contains(r.Header.Get("Cookie"), "jingjiaagent_session") || r.Header.Get("Authorization") == "Bearer test-token" {
			t.Error("gateway or node credential reached preview application")
		}
		switch r.URL.Path {
		case "/ws":
			ws, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer ws.Close()
			for {
				kind, b, err := ws.ReadMessage()
				if err != nil {
					return
				}
				if ws.WriteMessage(kind, b) != nil {
					return
				}
			}
		case "/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("start\n"))
			w.(http.Flusher).Flush()
			<-release
			w.Write([]byte("end\n"))
		case "/echo":
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			w.Header().Set("Clear-Site-Data", `"cookies"`)
			w.Header().Add("Set-Cookie", "app=value; Domain=localhost; Path=/")
			w.Header().Add("Set-Cookie", previewCookie+"=attack; Path=/")
			b, _ := io.ReadAll(r.Body)
			if r.URL.RawQuery != "q=%E4%B8%AD" {
				t.Error("query changed")
			}
			w.Write(b)
		default:
			w.Write(binary)
		}
	}))
	defer func() { unblock(); upstream.Close() }()
	u, _ := url.Parse(upstream.URL)
	nodeMux := http.NewServeMux()
	execPath, execHandler := rpc.NewExecServiceHandler(&previewExecFixture{})
	nodeMux.Handle(execPath, execHandler)
	nodeMux.HandleFunc("/internal/jingjiaagent/tcp/sandbox/8080", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "denied", 401)
			return
		}
		tcp, err := net.Dial("tcp", u.Host)
		if err != nil {
			http.Error(w, "failed", 502)
			return
		}
		defer tcp.Close()
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn := &previewConn{ws: ws, done: make(chan struct{})}
		defer conn.Close()
		done := make(chan struct{})
		go func() {
			io.Copy(conn, tcp)
			_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			conn.Close()
			close(done)
		}()
		io.Copy(tcp, conn)
		tcp.Close()
		<-done
	})
	engine := testEngine(t, nodeMux)
	c, env, owner := previewFixture(t, engine)
	info, err := c.PortForwarder().Create(context.Background(), taskflow.CreatePortForward{ID: env, UserID: owner, LocalPort: 8080, AccessMode: "ip_allowlist", WhitelistIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	id := *info.ForwardID
	host := id + ".localhost:47421"
	var denied atomic.Bool
	s := NewPreviewService(c, func(_ context.Context, user, environment string) error {
		if denied.Load() || user != owner || environment != env {
			return errors.New("denied")
		}
		return nil
	})
	defer s.cancel()
	gateway := httptest.NewServer(s)
	defer gateway.Close()
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(method, path string, b []byte, cookie *http.Cookie, origin string) *http.Response {
		t.Helper()
		r, _ := http.NewRequest(method, gateway.URL+path, bytes.NewReader(b))
		r.Host = host
		r.Header.Set("X-Forwarded-Proto", "http")
		if cookie != nil {
			r.AddCookie(cookie)
			r.AddCookie(&http.Cookie{Name: "jingjiaagent_session", Value: "platform-private-session"})
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	unauth := call("GET", "/", nil, nil, "")
	unauth.Body.Close()
	if unauth.StatusCode != 200 {
		t.Fatal("whitelisted anonymous preview denied", unauth.StatusCode)
	}
	cookie := &http.Cookie{Name: previewCookie, Value: "obsolete-cookie"}
	response := call("POST", "/echo?q=%E4%B8%AD", binary, cookie, "http://"+host)
	b, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !bytes.Equal(b, binary) || len(response.Cookies()) != 1 || response.Cookies()[0].Domain != "" {
		t.Fatal("binary streaming/cookie isolation failed", response.StatusCode)
	}
	if response.Header.Get("Strict-Transport-Security") != "" || response.Header.Get("Clear-Site-Data") != "" {
		t.Fatal("preview may change sibling transport or cookies")
	}
	response = call("GET", "/", nil, cookie, "http://other.localhost:47421")
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cross-origin preview accepted")
	}
	stream := call("GET", "/stream", nil, cookie, "")
	first := make([]byte, 6)
	if _, err = io.ReadFull(stream.Body, first); err != nil || string(first) != "start\n" {
		t.Fatal("stream was buffered", err)
	}
	unblock()
	last, err := io.ReadAll(stream.Body)
	if err != nil || string(last) != "end\n" {
		t.Fatal("stream completion was truncated", err)
	}
	stream.Body.Close()
	gw, _ := url.Parse(gateway.URL)
	gw.Scheme = "ws"
	gw.Path = "/ws"
	header := http.Header{"Host": {host}, "Cookie": {cookie.String()}, "Origin": {"http://" + host}, "X-Forwarded-Proto": {"http"}}
	ws, resp, err := websocket.DefaultDialer.Dial(gw.String(), header)
	if err != nil {
		if resp != nil {
			t.Log(resp.StatusCode)
		}
		t.Fatal(err)
	}
	defer ws.Close()
	if err = ws.WriteMessage(websocket.BinaryMessage, binary[:80000]); err != nil {
		t.Fatal(err)
	}
	kind, got, err := ws.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || !bytes.Equal(got, binary[:80000]) {
		t.Fatal("WebSocket corrupted")
	}
	// Permission changes must terminate already upgraded connections too.
	denied.Store(true)
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err = ws.ReadMessage(); err == nil {
		t.Fatal("revoked WebSocket remained usable")
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("revocation did not close the live connection")
	}
	response = call("GET", "/", nil, cookie, "")
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("revoked owner still admitted")
	}
	denied.Store(false)
	ws2, _, err := websocket.DefaultDialer.Dial(gw.String(), header)
	if err != nil {
		t.Fatal(err)
	}
	defer ws2.Close()
	if _, err = c.PortForwarder().Update(context.Background(), taskflow.UpdatePortForward{ID: env, ForwardID: id, AccessMode: "ip_allowlist", WhitelistIPs: []string{"192.0.2.2"}}); err != nil {
		t.Fatal(err)
	}
	ws2.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err = ws2.ReadMessage(); err == nil {
		t.Fatal("whitelist revocation kept WebSocket open")
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("whitelist revocation did not close live connection")
	}
	response = call("GET", "/", nil, nil, "")
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("non-whitelisted anonymous client allowed")
	}
	if err = c.PortForwarder().Close(context.Background(), taskflow.ClosePortForward{ID: env, ForwardID: id}); err != nil {
		t.Fatal(err)
	}
	response = call("GET", "/", nil, cookie, "")
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("closed port accessible")
	}
	// Unknown sibling mapping remains inaccessible without disclosing its state.
	r, _ := http.NewRequest("GET", gateway.URL, nil)
	r.Host = uuid.NewString() + ".localhost:47421"
	r.Header.Set("X-Forwarded-Proto", "http")
	response, err = client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("unknown mapping accepted")
	}
}

func TestPreviewGatewayNeverWakesAndTaskDeletionDisconnects(t *testing.T) {
	c, owner, task, env := previewTaskFixture(t)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer app.Close()
	mux := http.NewServeMux()
	installPreviewTunnel(t, mux, app.URL)
	path, handler := rpc.NewExecServiceHandler(&previewExecFixture{})
	mux.Handle(path, handler)
	c.engines["node"] = testEngine(t, mux)
	raw, err := c.RequestPreview(context.Background(), owner, task, 8080)
	if err != nil {
		t.Fatal(err)
	}
	info := decodePreviewInfo(t, raw)
	u, _ := url.Parse(*info.AccessURL)
	service := NewPreviewService(c, func(context.Context, string, string) error { return nil })
	defer service.cancel()
	gateway := httptest.NewServer(service)
	defer gateway.Close()
	var beforeCommands int
	if err := c.ledger.db.QueryRow(`SELECT count(*) FROM runtime_commands WHERE task_id=$1`, task).Scan(&beforeCommands); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ledger.db.Exec(`UPDATE runtime_environments SET state='hibernated' WHERE id=$1`, env); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("GET", gateway.URL, nil)
	request.Host = u.Host
	request.Header.Set("X-Forwarded-Proto", "http")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 403 || !strings.Contains(string(body), "预览暂不可用") {
		t.Fatal("unavailable link did not return a friendly page", response.StatusCode, string(body))
	}
	var unchanged bool
	if err := c.ledger.db.QueryRow(`SELECT state='hibernated' AND (SELECT count(*) FROM runtime_commands WHERE task_id=$2)=$3 FROM runtime_environments WHERE id=$1`, env, task, beforeCommands).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("anonymous access woke the workspace or enqueued work", unchanged, err)
	}
	if _, err := c.ledger.db.Exec(`UPDATE runtime_environments SET state='online' WHERE id=$1`, env); err != nil {
		t.Fatal(err)
	}
	address, _ := url.Parse(gateway.URL)
	address.Scheme, address.Path = "ws", "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(address.String(), http.Header{"Host": {u.Host}, "X-Forwarded-Proto": {"http"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := c.ledger.db.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err = conn.ReadMessage(); err == nil {
		t.Fatal("deleted task retained an active WebSocket")
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("task deletion did not close its live connection")
	}
}
