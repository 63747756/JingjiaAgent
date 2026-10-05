package runtimeadapter

import (
	"bytes"
	"context"
	"encoding/base64"
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

type previewExecFixture struct {
	rpc.UnimplementedExecServiceHandler
}

func (*previewExecFixture) Exec(context.Context, *connect.Request[v2.ExecRequest]) (*connect.Response[v2.ExecResponse], error) {
	return connect.NewResponse(&v2.ExecResponse{Result: &v2.ExecResult{Stdout: `[{"port":8080,"process":"python3"}]`}}), nil
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
	return &Client{ledger: l, engines: map[string]*Engine{"node": engine}, callbackURL: "http://127.0.0.1:47420", preview: config.RuntimePreview{BaseURL: "http://localhost:47421", Listen: "127.0.0.1:47421"}}, env, owner
}

func TestPreviewPortScopesAndRevision(t *testing.T) {
	mux := http.NewServeMux()
	path, h := rpc.NewExecServiceHandler(&previewExecFixture{})
	mux.Handle(path, h)
	c, env, owner := previewFixture(t, testEngine(t, mux))
	ctx := context.Background()
	p := c.PortForwarder()
	for _, ips := range [][]string{nil, {}, {"bad"}, {"0.0.0.0/0"}} {
		if _, err := p.Create(ctx, taskflow.CreatePortForward{ID: env, UserID: owner, LocalPort: 8080, WhitelistIPs: ips}); err == nil {
			t.Fatal("invalid whitelist accepted")
		}
	}
	if _, err := p.Create(ctx, taskflow.CreatePortForward{ID: env, UserID: uuid.NewString(), LocalPort: 8080, WhitelistIPs: []string{"127.0.0.1"}}); err == nil {
		t.Fatal("foreign owner opened port")
	}
	list, err := p.List(ctx, taskflow.ListPortforwadReq{ID: env, RequestId: "list"})
	if err != nil || len(list.Ports) != 1 || list.Ports[0].Status != "reserved" || list.RequestId != "list" {
		t.Fatal("listening port discovery failed", err)
	}
	if list.Ports[0].AccessURL != nil || list.Ports[0].ForwardID != nil {
		t.Fatal("unconfigured listener advertised preview access")
	}
	info, err := p.Create(ctx, taskflow.CreatePortForward{ID: env, UserID: owner, LocalPort: 8080, WhitelistIPs: []string{"127.0.0.1", "::ffff:127.0.0.1"}})
	if err != nil || !info.Success || len(info.WhitelistIPs) != 1 {
		t.Fatal("create failed", err)
	}
	id := *info.ForwardID
	list, err = p.List(ctx, taskflow.ListPortforwadReq{ID: env})
	if err != nil || len(list.Ports) != 1 || list.Ports[0].Status != "connected" || list.Ports[0].AccessURL == nil || !list.Ports[0].Success {
		t.Fatal("configured listener did not advertise preview access", err)
	}
	if _, err = p.Update(ctx, taskflow.UpdatePortForward{ID: "other", ForwardID: id, WhitelistIPs: []string{"127.0.0.1"}}); err == nil {
		t.Fatal("cross-environment update accepted")
	}
	if err = p.Close(ctx, taskflow.ClosePortForward{ID: "other", ForwardID: id}); err == nil {
		t.Fatal("cross-environment close accepted")
	}
	updated, err := p.Update(ctx, taskflow.UpdatePortForward{ID: env, ForwardID: id, WhitelistIPs: []string{"::1"}})
	if err != nil || updated.WhitelistIPs[0] != "::1" {
		t.Fatal("update failed", err)
	}
	f, err := c.forward(ctx, id)
	if err != nil || f.Revision != 2 {
		t.Fatal("update did not revoke old grants")
	}
	if err = p.Close(ctx, taskflow.ClosePortForward{ID: env, ForwardID: id}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.forward(ctx, id); err == nil {
		t.Fatal("closed forward accessible")
	}
	reopened, err := p.Create(ctx, taskflow.CreatePortForward{ID: env, UserID: owner, LocalPort: 8080, WhitelistIPs: []string{"127.0.0.1"}})
	if err != nil || *reopened.ForwardID != id {
		t.Fatal("reopen created duplicate forward")
	}
	f, _ = c.forward(ctx, id)
	if f.Revision != 4 {
		t.Fatal("reopen accepted earlier cookie revision")
	}
	if _, err = c.BeginPreview(ctx, uuid.NewString(), id); err == nil {
		t.Fatal("foreign user obtained an admission ticket")
	}
	if _, err = c.ledger.db.Exec("UPDATE runtime_environments SET state='hibernated' WHERE id=$1", env); err != nil {
		t.Fatal(err)
	}
	list, err = p.List(ctx, taskflow.ListPortforwadReq{ID: env})
	if err != nil || len(list.Ports) != 1 || list.Ports[0].Status != "reserved" {
		t.Fatal("hibernation lost persisted forward metadata", err)
	}
	if list.Ports[0].AccessURL != nil || list.Ports[0].Success || list.Ports[0].ForwardID == nil || list.Ports[0].ErrorMessage != "Port is not listening" {
		t.Fatal("stopped service advertised access or lost its port settings")
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
	info, err := c.PortForwarder().Create(context.Background(), taskflow.CreatePortForward{ID: env, UserID: owner, LocalPort: 8080, WhitelistIPs: []string{"127.0.0.1"}})
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
	authorize := func() *http.Cookie {
		t.Helper()
		location, err := c.BeginPreview(context.Background(), owner, id)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(location)
		r := call("GET", u.RequestURI(), nil, nil, "")
		defer r.Body.Close()
		if r.StatusCode != 303 || len(r.Cookies()) != 1 {
			t.Fatal("admission failed", r.StatusCode)
		}
		cookie := r.Cookies()[0]
		if !cookie.HttpOnly || cookie.Domain != "" {
			t.Fatal("cookie scope")
		}
		replay := call("GET", u.RequestURI(), nil, nil, "")
		replay.Body.Close()
		if replay.StatusCode != 401 {
			t.Fatal("ticket replay accepted")
		}
		return cookie
	}
	unauth := call("GET", "/", nil, nil, "")
	unauth.Body.Close()
	if unauth.StatusCode != 401 {
		t.Fatal("unauthenticated preview")
	}
	cookie := authorize()
	response := call("POST", "/echo?q=%E4%B8%AD", binary, cookie, "http://"+host)
	b, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !bytes.Equal(b, binary) || len(response.Cookies()) != 1 || response.Cookies()[0].Domain != "" {
		t.Fatal("binary streaming/cookie isolation failed", response.StatusCode)
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
	header := http.Header{"Host": {host}, "Cookie": {cookie.String()}, "Origin": {"http://" + host}}
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
	if err = c.PortForwarder().Close(context.Background(), taskflow.ClosePortForward{ID: env, ForwardID: id}); err != nil {
		t.Fatal(err)
	}
	response = call("GET", "/", nil, cookie, "")
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("closed port accessible")
	}
	// A valid encrypted grant cannot be reused on a sibling host.
	sealed, _ := c.ledger.seal("preview:"+id, mustJSON(previewGrant{ForwardID: id, UserID: owner, Revision: 1, Expires: time.Now().Add(time.Minute).Unix()}))
	r, _ := http.NewRequest("GET", gateway.URL, nil)
	r.Host = uuid.NewString() + ".localhost:47421"
	r.AddCookie(&http.Cookie{Name: previewCookie, Value: base64.RawURLEncoding.EncodeToString(sealed)})
	response, err = client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("sibling cookie accepted")
	}
}
