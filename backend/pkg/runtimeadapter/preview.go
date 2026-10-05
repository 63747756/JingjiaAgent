package runtimeadapter

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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
const previewAuthorizePath = "/.jingjiaagent/authorize"

func previewControlCookie(name string) bool {
	return name == previewCookie || name == consts.JingjiaAgentAISession || name == consts.JingjiaAgentAITeamSession
}

type previewGrant struct {
	ForwardID string `json:"forward"`
	UserID    string `json:"user"`
	Revision  int64  `json:"revision"`
	Expires   int64  `json:"expires"`
}

func (c *Client) PreviewEnvironment(ctx context.Context, forward string) (Environment, error) {
	f, err := c.forward(ctx, forward)
	if err != nil {
		return Environment{}, err
	}
	e, _, err := c.environment(ctx, f.EnvironmentID)
	return e, err
}

func (c *Client) previewURL(id string) *url.URL {
	u, _ := url.Parse(c.preview.BaseURL)
	u.Host = id + "." + u.Host
	return u
}

// BeginPreview is called only after original login and HostRepo authorization.
// The browser receives a one-use ticket, never a daemon token or platform cookie.
func (c *Client) BeginPreview(ctx context.Context, user, id string) (string, error) {
	if c.preview.BaseURL == "" {
		return "", errors.New("runtime preview gateway is not configured")
	}
	f, err := c.forward(ctx, id)
	if err != nil {
		return "", err
	}
	e, _, err := c.environment(ctx, f.EnvironmentID)
	if err != nil {
		return "", err
	}
	if user == "" || e.State != "online" || authorize(e, user, "") != nil {
		return "", errors.New("preview permission denied")
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	// Bound expired-ticket growth without invalidating other active admissions.
	if _, err = c.ledger.db.ExecContext(ctx, "DELETE FROM runtime_preview_tickets WHERE expires_at<now()"); err != nil {
		return "", err
	}
	if _, err = c.ledger.db.ExecContext(ctx, "INSERT INTO runtime_preview_tickets(token_hash,forward_id,user_id,expires_at) VALUES($1,$2,$3,now()+interval '30 seconds')", hash([]byte(token)), id, user); err != nil {
		return "", err
	}
	u := c.previewURL(id)
	u.Path = previewAuthorizePath
	u.RawQuery = url.Values{"ticket": {token}}.Encode()
	return u.String(), nil
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
func (c *Client) PreviewEnabled() bool { return c.preview.BaseURL != "" }

func (s *PreviewService) hostForward(host string) (string, bool) {
	base, _ := url.Parse(s.client.preview.BaseURL)
	suffix := "." + base.Host
	if !strings.HasSuffix(strings.ToLower(host), strings.ToLower(suffix)) {
		return "", false
	}
	id := host[:len(host)-len(suffix)]
	parsed, err := uuid.Parse(id)
	return id, err == nil && parsed.String() == id
}

func (s *PreviewService) allowed(ctx context.Context, grant previewGrant, r *http.Request) (Environment, *Engine, error) {
	f, err := s.client.forward(ctx, grant.ForwardID)
	if err != nil {
		return Environment{}, nil, err
	}
	if f.Revision != grant.Revision || grant.Expires <= time.Now().Unix() {
		return Environment{}, nil, errors.New("expired preview grant")
	}
	ip := s.client.PreviewClientIP(r)
	found := false
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
	if e.State != "online" || e.SandboxID == "" || grant.UserID == "" || authorize(e, grant.UserID, "") != nil {
		return e, nil, errors.New("preview environment unavailable")
	}
	if s.authorize == nil {
		return e, nil, errors.New("preview authorizer unavailable")
	}
	if err = s.authorize(ctx, grant.UserID, e.ID); err != nil {
		return e, nil, err
	}
	return e, engine, nil
}

func (s *PreviewService) admit(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", 405)
		return
	}
	token := r.URL.Query().Get("ticket")
	if len(token) != 43 {
		http.Error(w, "Invalid preview ticket", 401)
		return
	}
	// Wrong-host and expired attempts do not consume a valid ticket for another host.
	var user string
	err := s.client.ledger.db.QueryRowContext(r.Context(), "DELETE FROM runtime_preview_tickets WHERE token_hash=$1 AND forward_id=$2 AND expires_at>now() RETURNING user_id", hash([]byte(token)), id).Scan(&user)
	if err != nil {
		http.Error(w, "Invalid or expired preview ticket", 401)
		return
	}
	f, err := s.client.forward(r.Context(), id)
	if err != nil {
		http.Error(w, "Preview unavailable", 403)
		return
	}
	grant := previewGrant{ForwardID: id, UserID: user, Revision: f.Revision, Expires: time.Now().Add(15 * time.Minute).Unix()}
	if _, _, err = s.allowed(r.Context(), grant, r); err != nil {
		http.Error(w, "Preview access denied", 403)
		return
	}
	sealed, err := s.client.ledger.seal("preview:"+id, mustJSON(grant))
	if err != nil {
		http.Error(w, "Preview unavailable", 503)
		return
	}
	base, _ := url.Parse(s.client.preview.BaseURL)
	http.SetCookie(w, &http.Cookie{Name: previewCookie, Value: base64.RawURLEncoding.EncodeToString(sealed), Path: "/", HttpOnly: true, Secure: base.Scheme == "https", SameSite: http.SameSiteLaxMode, MaxAge: 900})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *PreviewService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	id, ok := s.hostForward(r.Host)
	if !ok {
		http.Error(w, "Unknown preview host", 404)
		return
	}
	if r.URL.Path == previewAuthorizePath {
		s.admit(w, r, id)
		return
	}
	cookie, err := r.Cookie(previewCookie)
	if err != nil || len(cookie.Value) > 4096 {
		http.Error(w, "Open this preview from the signed-in workspace", 401)
		return
	}
	sealed, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		http.Error(w, "Invalid preview authorization", 401)
		return
	}
	data, err := s.client.ledger.open("preview:"+id, sealed)
	var grant previewGrant
	if err != nil || json.Unmarshal(data, &grant) != nil || grant.ForwardID != id {
		http.Error(w, "Invalid preview authorization", 401)
		return
	}
	// Prevent cross-origin requests (including cross-preview WebSocket handshakes).
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		base, _ := url.Parse(s.client.preview.BaseURL)
		if err != nil || u.Host != r.Host || u.Scheme != base.Scheme {
			http.Error(w, "Preview origin denied", 403)
			return
		}
	}
	e, engine, err := s.allowed(r.Context(), grant, r)
	if err != nil {
		http.Error(w, "Preview access denied", 403)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		http.Error(w, "Preview connection limit", 503)
		return
	}
	f, err := s.client.forward(r.Context(), id)
	if err != nil {
		http.Error(w, "Preview unavailable", 403)
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
				_, _, err := s.allowed(check, grant, r)
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
			// An application cannot replace the gateway authorization cookie or spread
			// its cookies into sibling previews using a parent Domain.
			cookies := response.Cookies()
			response.Header.Del("Set-Cookie")
			for _, cookie := range cookies {
				if !previewControlCookie(cookie.Name) {
					cookie.Domain = ""
					response.Header.Add("Set-Cookie", cookie.String())
				}
			}
			response.Header.Del("Access-Control-Allow-Origin")
			response.Header.Del("Access-Control-Allow-Credentials")
			response.Header.Set("Referrer-Policy", "no-referrer")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "Preview service unavailable", 502)
		},
	}
	proxy.ServeHTTP(w, r.WithContext(ctx))
}
