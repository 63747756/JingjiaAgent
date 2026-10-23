package llmproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	urlpath "path"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/model"
	"github.com/63747756/jingjiaagent/backend/db/modelapikey"
	"github.com/63747756/jingjiaagent/backend/db/taskvirtualmachine"
	"github.com/63747756/jingjiaagent/backend/db/teamgroup"
	"github.com/63747756/jingjiaagent/backend/db/user"
	"github.com/63747756/jingjiaagent/backend/pkg/modelusage"
	"github.com/63747756/jingjiaagent/backend/pkg/netguard"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

const upstreamFailureMessage = "连接上游模型失败，请检查模型配置，或重试"

var errModelForbidden = errors.New("model is not accessible")

var allowPaths = map[string]string{
	"/v1/chat/completions": "/chat/completions",
	"/v1/responses":        "/responses",
	"/v1/messages":         "/messages",
}

type contextKey struct{}

type modelContext struct {
	contextLimit  int
	modelID       uuid.UUID
	userID        uuid.UUID
	vmID          string
	keyKind       modelapikey.Kind
	provider      string
	modelName     string
	baseURL       string
	apiKey        string
	signingSecret string
}

type proxyContext struct {
	startedAt    time.Time
	model        *modelContext
	upstreamPath string
	stream       bool
}

type Proxy struct {
	contextRecorder     taskflow.ContextUsageRecorder
	db                  *db.Client
	logger              *slog.Logger
	recorder            usageRecorder
	transport           http.RoundTripper
	proxy               *httputil.ReverseProxy
	blockPrivateNetwork bool
}

type usageRecorder interface {
	Record(ctx context.Context, event modelusage.Event) error
}

type Option func(*Proxy)

func WithContextUsageRecorder(recorder taskflow.ContextUsageRecorder) Option {
	return func(p *Proxy) { p.contextRecorder = recorder }
}

func WithUsageRecorder(recorder usageRecorder) Option {
	return func(p *Proxy) {
		p.recorder = recorder
	}
}

func WithPrivateNetworkBlocked(block bool) Option {
	return func(p *Proxy) {
		p.blockPrivateNetwork = block
	}
}

func NewProxy(db *db.Client, logger *slog.Logger, opts ...Option) *Proxy {
	if logger == nil {
		logger = slog.Default()
	}
	p := &Proxy{
		db:     db,
		logger: logger.With("module", "llmproxy"),
	}
	for _, opt := range opts {
		opt(p)
	}
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
		Proxy:               http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 300 * time.Second,
	}
	p.transport = netguard.New(p.blockPrivateNetwork).HTTPClient(&http.Client{Transport: transport}).Transport
	p.proxy = &httputil.ReverseProxy{
		Transport:      p.transport,
		Rewrite:        p.rewrite,
		ModifyResponse: p.modifyResponse,
		ErrorHandler:   p.errorHandler,
		FlushInterval:  100 * time.Millisecond,
	}
	return p
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upstreamPath, ok := allowPaths[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	token, ok := extractToken(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))

	reqMeta, err := readRequestMeta(body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	m, err := p.resolveModel(r.Context(), token, reqMeta.Model)
	if err != nil {
		if errors.Is(err, errModelForbidden) {
			p.logger.WarnContext(r.Context(), "resolve ohmyagent model failed", "error", err)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		p.logger.WarnContext(r.Context(), "resolve runtime model failed", "error", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if err := config.ValidateModelURL(m.baseURL); err != nil {
		http.Error(w, "模型地址无效", http.StatusForbidden)
		return
	}
	if m.keyKind == modelapikey.KindRuntime && reqMeta.Model != "" && reqMeta.Model != m.modelName {
		p.logger.WarnContext(r.Context(), "model mismatch", "request_model", reqMeta.Model, "expected_model", m.modelName)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if m.keyKind == modelapikey.KindOhmyagent {
		body, err = replaceRequestModel(body, m.modelName)
		if err != nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		if err := ValidateOhMyAgentPrompt(body, r.Header.Get(ohMyAgentSignatureHeader), m.signingSecret); err != nil {
			p.logger.WarnContext(r.Context(), "ohmyagent prompt validation failed", "error", err)
			http.Error(w, "invalid ohmyagent request", http.StatusForbidden)
			return
		}
	}

	ctx := context.WithValue(r.Context(), contextKey{}, &proxyContext{
		startedAt:    time.Now(),
		model:        m,
		upstreamPath: upstreamPath,
		stream:       reqMeta.Stream,
	})
	p.proxy.ServeHTTP(w, r.WithContext(ctx))
}

func (p *Proxy) resolveModel(ctx context.Context, token, requestedModel string) (*modelContext, error) {
	keyID, err := uuid.Parse(token)
	query := p.db.ModelApiKey.Query().
		WithModel().
		Where(modelapikey.APIKey(token))
	if err == nil {
		query = p.db.ModelApiKey.Query().
			WithModel().
			Where(modelapikey.Or(modelapikey.ID(keyID), modelapikey.APIKey(token)))
	}
	key, err := query.Only(ctx)
	if err != nil {
		return nil, err
	}

	selected := key.Edges.Model
	if key.Kind == modelapikey.KindOhmyagent {
		selected, err = p.db.Model.Query().
			Where(
				model.Model(requestedModel),
				model.Or(
					model.UserID(key.UserID),
					model.HasGroupsWith(teamgroup.HasMembersWith(user.ID(key.UserID))),
					model.HasUserWith(user.Role(consts.UserRoleAdmin)),
				),
			).
			Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errModelForbidden, err)
		}
	}
	if selected == nil {
		return nil, errors.New("model not found")
	}
	return &modelContext{
		contextLimit:  selected.ContextLimit,
		modelID:       selected.ID,
		userID:        key.UserID,
		vmID:          key.VirtualmachineID,
		keyKind:       key.Kind,
		provider:      selected.Provider,
		modelName:     selected.Model,
		baseURL:       selected.BaseURL,
		apiKey:        selected.APIKey,
		signingSecret: key.SigningSecret,
	}, nil
}

func replaceRequestModel(body []byte, modelName string) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(modelName)
	if err != nil {
		return nil, err
	}
	payload["model"] = encoded
	return json.Marshal(payload)
}

var LLMAllowPaths []string = []string{
	"/v1/messages",
	"/chat/completions",
	"/responses",
}

func fetchAllowPath(path string) string {
	for _, v := range LLMAllowPaths {
		if strings.HasSuffix(path, v) {
			return v
		}
	}
	return ""
}

func (p *Proxy) rewrite(r *httputil.ProxyRequest) {
	path := r.In.URL.Path
	p.logger.With("path", path).DebugContext(r.In.Context(), "new rewrite request")

	ctx, ok := r.In.Context().Value(contextKey{}).(*proxyContext)
	if !ok || ctx == nil || ctx.model == nil {
		p.logger.WarnContext(r.In.Context(), "missing model context")
		return
	}

	uppath := fetchAllowPath(path)
	if uppath == "" {
		p.logger.With("path", path).WarnContext(r.In.Context(), "unsupport api type")
		return
	}

	m := ctx.model
	ul, err := url.Parse(m.baseURL)
	if err != nil {
		p.logger.ErrorContext(r.In.Context(), "parse model base url failed", "base_url", m.baseURL, "error", err)
		return
	}
	r.Out.URL.Scheme = ul.Scheme
	r.Out.URL.Host = ul.Host
	r.Out.URL.Path = urlpath.Join(ul.Path, uppath)
	r.Out.Header.Set("Authorization", "Bearer "+m.apiKey)
	r.Out.Header.Set("X-Api-Key", m.apiKey)
	r.Out.Header.Del(ohMyAgentSignatureHeader)
	// Let our transport negotiate/decode gzip. Forwarding the caller's header
	// disables Go's automatic decoding and leaves the usage reader compressed.
	r.Out.Header.Del("Accept-Encoding")
	r.SetXForwarded()
	r.Out.Host = ul.Host
	p.logger.With(
		"model", m.modelName,
		"in", r.In.URL.String(),
		"out", r.Out.URL.String(),
	).DebugContext(r.In.Context(), "rewrite request success")
}

func (p *Proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	p.logger.ErrorContext(r.Context(), "proxy upstream failed", "path", r.URL.Path, "error", err)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_, _ = w.Write([]byte(upstreamFailureMessage))
}

func (p *Proxy) modifyResponse(resp *http.Response) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	if err := decodeModelResponse(resp); err != nil {
		return err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return sanitizeErrorResponse(resp)
	}
	ctx, ok := resp.Request.Context().Value(contextKey{}).(*proxyContext)
	if !ok || ctx == nil || ctx.model == nil {
		return nil
	}
	resp.Body = NewUsageCapture(p.logger, resp.Body, &UsageCaptureContext{
		ctx:      resp.Request.Context(),
		path:     normalizeUsageCapturePath(resp.Request.URL.Path),
		stream:   ctx.stream,
		proxyCtx: ctx,
		proxy:    p,
	})
	return nil
}

// Also handle upstreams that send gzip without negotiation, or custom
// transports that leave it encoded. Decode incrementally to preserve SSE.
func decodeModelResponse(resp *http.Response) error {
	encoding := strings.TrimSpace(resp.Header.Get("Content-Encoding"))
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return nil
	}
	if !strings.EqualFold(encoding, "gzip") {
		return fmt.Errorf("unsupported model response encoding: %s", encoding)
	}
	reader, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("decode model response gzip: %w", err)
	}
	resp.Body = &decodedModelBody{Reader: reader, source: resp.Body}
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	resp.Uncompressed = true
	return nil
}

type decodedModelBody struct {
	*gzip.Reader
	source io.ReadCloser
}

func (b *decodedModelBody) Close() error {
	return errors.Join(b.Reader.Close(), b.source.Close())
}

// Some upstream gateways reflect complete request headers on malformed input.
// Those responses must not disclose shared model credentials to an Agent/user.
func sanitizeErrorResponse(resp *http.Response) error {
	if resp.Request == nil {
		return nil
	}
	ctx, ok := resp.Request.Context().Value(contextKey{}).(*proxyContext)
	if !ok || ctx == nil || ctx.model == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	if len(body) > 1<<20 || bytes.Contains(body, []byte("invalid header:")) || (ctx.model.apiKey != "" && bytes.Contains(body, []byte(ctx.model.apiKey))) {
		body = []byte(`{"error":{"message":"Model service request failed","type":"upstream_error"}}`)
		resp.Header.Set("Content-Type", "application/json")
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("ETag")
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", fmt.Sprint(len(body)))
	return nil
}

func normalizeUsageCapturePath(path string) string {
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		return "/v1/chat/completions"
	case strings.HasSuffix(path, "/responses"):
		return "/v1/responses"
	case strings.HasSuffix(path, "/messages"):
		return "/v1/messages"
	default:
		return path
	}
}

func (p *Proxy) recordUsage(ctx context.Context, proxyCtx *proxyContext, result usageResult) {
	if p.contextRecorder != nil && proxyCtx != nil && proxyCtx.model != nil && result.hasTokens() {
		m := proxyCtx.model
		// OpenAI input includes cache; Anthropic reports cache reads/writes separately.
		used := result.InputTokens + result.CacheReadInputTokens + result.CacheCreationInputTokens + result.OutputTokens
		if err := p.contextRecorder.RecordContextUsage(ctx, m.vmID, m.contextLimit, used, proxyCtx.startedAt); err != nil {
			p.logger.WarnContext(ctx, "record task context usage failed", "error", err)
		}
	}
	event, ok := p.buildUsageEvent(ctx, proxyCtx, result)
	if !ok {
		return
	}
	if err := p.recorder.Record(ctx, event); err != nil {
		p.logger.WarnContext(ctx, "record model usage failed", "model", proxyCtx.model.modelName, "error", err)
	}
}

func (p *Proxy) buildUsageEvent(ctx context.Context, proxyCtx *proxyContext, result usageResult) (modelusage.Event, bool) {
	if p.recorder == nil || proxyCtx == nil || proxyCtx.model == nil {
		return modelusage.Event{}, false
	}
	if !result.hasTokens() {
		return modelusage.Event{}, false
	}
	m := proxyCtx.model
	taskID := p.resolveTaskID(ctx, m.vmID)
	if taskID == uuid.Nil {
		p.logger.WarnContext(ctx, "skip model usage event without task", "user_id", m.userID, "vm_id", m.vmID, "model_id", m.modelID)
		return modelusage.Event{}, false
	}
	return modelusage.Event{
		EventTime:    time.Now(),
		TaskID:       taskID,
		UserID:       m.userID,
		Provider:     m.provider,
		ModelID:      m.modelID.String(),
		ModelName:    m.modelName,
		InputTokens:  result.InputTokens + result.CacheReadInputTokens + result.CacheCreationInputTokens,
		OutputTokens: result.OutputTokens,
		CachedTokens: result.CacheReadInputTokens + result.CachedTokens,
		TotalTokens:  result.totalTokens(),
		Success:      true,
		RequestID:    result.ResponseID,
		Source:       "llmproxy",
	}, true
}

func (p *Proxy) resolveTaskID(ctx context.Context, vmID string) uuid.UUID {
	if p == nil || p.db == nil || vmID == "" {
		return uuid.Nil
	}
	taskVM, err := p.db.TaskVirtualMachine.Query().
		Where(taskvirtualmachine.VirtualmachineIDEQ(vmID)).
		Order(db.Desc(taskvirtualmachine.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		p.logger.WarnContext(ctx, "resolve task from vm failed", "vm_id", vmID, "error", err)
		return uuid.Nil
	}
	return taskVM.TaskID
}

func extractToken(req *http.Request) (string, bool) {
	token := strings.TrimSpace(req.Header.Get("X-Api-Key"))
	if token != "" {
		return token, true
	}
	token, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

type requestMeta struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

func readRequestMeta(body []byte) (requestMeta, error) {
	var payload struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return requestMeta{}, fmt.Errorf("parse llm request: %w", err)
	}
	return requestMeta{Model: payload.Model, Stream: payload.Stream}, nil
}
