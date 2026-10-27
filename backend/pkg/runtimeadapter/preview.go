package runtimeadapter

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/google/uuid"
)

const previewCookie = "jingjiaagent_preview"
const previewHealthPath = "/.well-known/jingjiaagent-preview-health"

func previewControlCookie(name string) bool {
	return name == previewCookie || name == consts.JingjiaAgentAISession || name == consts.JingjiaAgentAITeamSession
}

func (c *Client) previewURL(f portForward) *url.URL {
	u, err := url.Parse(c.preview.BaseURL)
	if err != nil || !c.PreviewEnabled() {
		return nil
	}
	u.Host = f.ID + "." + u.Host
	u.Path = ""
	return u
}

func (c *Client) trustedPreviewPeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, value := range c.preview.TrustedProxies {
		_, cidr, err := net.ParseCIDR(value)
		if err == nil && cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// PreviewClientIP ignores forwarded headers unless the socket peer is an
// explicitly trusted ingress. Read its chain from right to left.
func (c *Client) PreviewClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	current := net.ParseIP(host)
	if current == nil {
		return ""
	}
	if strings.TrimSpace(r.Header.Get("X-Forwarded-For")) == "" {
		return current.String()
	}
	trusted := func(ip net.IP) bool {
		for _, value := range c.preview.TrustedProxies {
			_, cidr, err := net.ParseCIDR(value)
			if err == nil && cidr.Contains(ip) {
				return true
			}
		}
		return false
	}
	chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(chain) - 1; i >= 0 && trusted(current); i-- {
		next := net.ParseIP(strings.TrimSpace(chain[i]))
		if next == nil {
			return ""
		}
		current = next
	}
	return current.String()
}

type PreviewService struct {
	client    *Client
	authorize func(context.Context, string, string) error
	server    *http.Server
	ctx       context.Context
	cancel    context.CancelFunc
	slots     chan struct{}
}

func NewPreviewService(c *Client, authorize func(context.Context, string, string) error) *PreviewService {
	ctx, cancel := context.WithCancel(context.Background())
	s := &PreviewService{client: c, authorize: authorize, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 64)}
	s.server = &http.Server{Addr: c.preview.Listen, Handler: s, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 64 << 10}
	return s
}
func (s *PreviewService) Name() string { return "Remote preview gateway" }
func (s *PreviewService) Start() error {
	err := s.server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func (s *PreviewService) Stop() error {
	s.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}
func (c *Client) PreviewEnabled() bool {
	return c.preview.EffectiveMode() != "off" && c.preview.BaseURL != ""
}

// ingress accepts only the configured gateway, with its overwritten protocol and
// port metadata. Neither a public client header nor an arbitrary Host selects an
// upstream service.
func (s *PreviewService) ingress(r *http.Request) (string, bool) {
	if !s.client.PreviewEnabled() || !s.client.trustedPreviewPeer(r) {
		return "", false
	}
	base, err := url.Parse(s.client.preview.BaseURL)
	if err != nil || r.Header.Get("X-Forwarded-Proto") != base.Scheme {
		return "", false
	}
	if strings.EqualFold(r.Host, base.Host) {
		return "", r.URL.Path == previewHealthPath
	}
	suffix := "." + base.Host
	if !strings.HasSuffix(strings.ToLower(r.Host), strings.ToLower(suffix)) {
		return "", false
	}
	id := r.Host[:len(r.Host)-len(suffix)]
	parsed, err := uuid.Parse(id)
	return id, err == nil && parsed.String() == id
}

func (s *PreviewService) allowed(ctx context.Context, initial portForward, r *http.Request) (Environment, *Engine, error) {
	f, err := s.client.forward(ctx, initial.ID)
	if err != nil {
		return Environment{}, nil, err
	}
	if f.Revision != initial.Revision {
		return Environment{}, nil, errors.New("preview configuration changed")
	}
	if f.TaskID.Valid {
		var current bool
		err = s.client.ledger.db.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM runtime_task_intents i JOIN tasks t ON t.id=i.task_id
WHERE i.task_id=$1 AND i.environment_id=$2 AND NOT i.deletion_requested AND t.deleted_at IS NULL)`, f.TaskID.String, f.EnvironmentID).Scan(&current)
		if err != nil || !current {
			return Environment{}, nil, errors.New("preview task environment unavailable")
		}
	}
	ip := s.client.PreviewClientIP(r)
	found := f.AccessMode == "public"
	for _, allowed := range f.Whitelist {
		if ip == allowed {
			found = true
		}
	}
	if !found {
		return Environment{}, nil, errors.New("preview IP is not allowed")
	}
	e, engine, err := s.client.environment(ctx, f.EnvironmentID)
	if err != nil {
		return e, nil, err
	}
	if e.State != "online" || e.SandboxID == "" || e.OwnerID == "" {
		return e, nil, errors.New("preview environment unavailable")
	}
	if s.authorize == nil {
		return e, nil, errors.New("preview authorizer unavailable")
	}
	if err = s.authorize(ctx, e.OwnerID, e.ID); err != nil {
		return e, nil, err
	}
	return e, engine, nil
}

func (s *PreviewService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	id, ok := s.ingress(r)
	if !ok {
		http.Error(w, "Unknown preview ingress", 404)
		return
	}
	if r.URL.Path == previewHealthPath {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", 405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"service":"jingjiaagent-preview"}`))
		}
		return
	}
	f, err := s.client.forward(r.Context(), id)
	if err != nil {
		previewUnavailable(w, 403)
		return
	}
	// Prevent cross-origin requests (including cross-preview WebSocket handshakes).
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		base, _ := url.Parse(s.client.preview.BaseURL)
		if err != nil || u.Host != r.Host || u.Scheme != base.Scheme || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			http.Error(w, "Preview origin denied", 403)
			return
		}
	}
	e, engine, err := s.allowed(r.Context(), f, r)
	if err != nil {
		previewUnavailable(w, 403)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		http.Error(w, "Preview connection limit", 503)
		return
	}
	// Recheck actual listeners before forwarding so a restarted executor cannot
	// inherit an application's former public port.
	detected, err := s.client.discoverPreviewPorts(r.Context(), e, engine)
	if err != nil {
		previewUnavailable(w, 503)
		return
	}
	listening := false
	for _, d := range detected {
		if d.Port == f.Port {
			if d.Protected {
				http.Error(w, "Platform internal port", 403)
				return
			}
			listening = true
		}
	}
	if !listening {
		previewUnavailable(w, 502)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.ctx.Done():
				cancel()
				return
			case <-ticker.C:
				check, stop := context.WithTimeout(ctx, 3*time.Second)
				_, _, err := s.allowed(check, f, r)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 30 * time.Second,
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			return engine.dialPreview(dialCtx, ctx, e.SandboxID, f.Port)
		}}
	defer transport.CloseIdleConnections()
	target := &url.URL{Scheme: "http", Host: "guest-preview"}
	proxy := &httputil.ReverseProxy{Transport: transport, FlushInterval: -1,
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			p.Out.Host = p.In.Host
			p.SetXForwarded()
			p.Out.Header.Set("X-Forwarded-For", s.client.PreviewClientIP(r))
			p.Out.Header.Set("X-Real-IP", s.client.PreviewClientIP(r))
			base, _ := url.Parse(s.client.preview.BaseURL)
			p.Out.Header.Set("X-Forwarded-Proto", base.Scheme)
			p.Out.Header.Del("Cookie")
			for _, cookie := range p.In.Cookies() {
				if !previewControlCookie(cookie.Name) {
					p.Out.AddCookie(cookie)
				}
			}
		},
		ModifyResponse: func(response *http.Response) error {
			// Applications cannot replace platform cookies or spread domain cookies
			// into sibling previews.
			cookies := response.Cookies()
			response.Header.Del("Set-Cookie")
			for _, cookie := range cookies {
				if !previewControlCookie(cookie.Name) {
					cookie.Domain = ""
					response.Header.Add("Set-Cookie", cookie.String())
				}
			}
			response.Header.Del("Strict-Transport-Security")
			response.Header.Del("Clear-Site-Data")
			response.Header.Del("Access-Control-Allow-Origin")
			response.Header.Del("Access-Control-Allow-Credentials")
			response.Header.Set("Referrer-Policy", "no-referrer")
			response.Header.Set("Cache-Control", "no-store")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			previewUnavailable(w, 502)
		},
	}
	proxy.ServeHTTP(w, r.WithContext(ctx))
}

// Anonymous access never wakes the workspace or executes an agent command.
func previewUnavailable(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>预览暂不可用</title><body style="font-family:system-ui;text-align:center;padding:15vh 24px;color:#475569"><h2>预览暂不可用</h2><p>服务可能已停止，或当前访问权限不允许。</p><p>任务所属员工可在任务中查看服务状态。</p></body></html>`))
}
