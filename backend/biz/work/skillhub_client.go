package work

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var hubCoordinate = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,119}$`)

type hubSession struct {
	base   *url.URL
	client *http.Client
	token  string
	cfg    config.SkillHub
}

type hubResponseError struct {
	status int
	cause  error
}

func (e *hubResponseError) Error() string { return e.cause.Error() }
func (e *hubResponseError) Unwrap() error { return e.cause }

// Only explicit rejection responses prove that publication did not happen.
// Timeouts, broken success responses and server errors remain ambiguous.
func hubPublishRejected(err error) bool {
	var response *hubResponseError
	if !errors.As(err, &response) {
		return false
	}
	switch response.status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusConflict,
		http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType,
		http.StatusUnprocessableEntity, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

func newHubSession(cfg config.SkillHub, interactive bool) (*hubSession, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.URL), "/"))
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Scheme != "https" && !(cfg.AllowHTTP && base.Scheme == "http")) {
		return nil, skillInputError(errors.New("请配置有效的 SkillHub 地址，正式环境需要 HTTPS"))
	}
	h := &hubSession{base: base, cfg: cfg}
	if cfg.PublicURL != "" {
		public, err := url.Parse(cfg.PublicURL)
		if err != nil || public.Host == "" || public.User != nil || public.RawQuery != "" || public.Fragment != "" || (public.Scheme != "https" && !(cfg.AllowHTTP && public.Scheme == "http")) {
			return nil, skillInputError(errors.New("SkillHub 展示地址无效"))
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The configured intranet registry uses a dedicated, direct connection.
	// Model egress proxies must not change its responses or receive credentials.
	transport.Proxy = nil
	h.client = &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	if interactive {
		h.client.Jar, _ = cookiejar.New(nil)
	}
	return h, nil
}
func (h *hubSession) endpoint(p string) string { return strings.TrimRight(h.base.String(), "/") + p }
func (h *hubSession) publicEndpoint(p string) string {
	base := h.cfg.PublicURL
	if base == "" {
		base = h.base.String()
	}
	return strings.TrimRight(base, "/") + p
}
func (h *hubSession) do(ctx context.Context, method, p string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, h.endpoint(p), body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	if h.client.Jar != nil {
		for _, c := range h.client.Jar.Cookies(h.base) {
			if c.Name == "XSRF-TOKEN" {
				csrf, _ := url.QueryUnescape(c.Value)
				req.Header.Set("X-XSRF-TOKEN", csrf)
			}
		}
	}
	res, err := h.client.Do(req)
	if err != nil {
		return nil, skillInputError(errors.New("SkillHub 连接失败，请检查配置后重试"))
	}
	return res, nil
}
func hubResponse(res *http.Response, out any) error {
	defer res.Body.Close()
	responseError := func(err error) error {
		return &hubResponseError{status: res.StatusCode, cause: err}
	}
	if res.StatusCode == http.StatusUnauthorized {
		return responseError(&skillValidationError{detail: "SkillHub 账号密码错误或授权已失效，请重新绑定", binding: true})
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil {
		return responseError(err)
	}
	if len(data) > 2<<20 {
		return responseError(skillInputError(errors.New("SkillHub 返回内容过大")))
	}
	var e struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(data, &e); err != nil {
		return responseError(skillInputError(errors.New("SkillHub 返回格式异常")))
	}
	if res.StatusCode >= 300 || e.Code != 0 {
		msg := e.Msg
		if msg == "" {
			msg = "SkillHub 请求失败"
		}
		return responseError(skillInputError(errors.New(msg)))
	}
	if out != nil {
		if err := json.Unmarshal(e.Data, out); err != nil {
			return responseError(err)
		}
	}
	return nil
}
func (h *hubSession) json(ctx context.Context, method, p string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	res, err := h.do(ctx, method, p, body, "application/json")
	if err != nil {
		return err
	}
	return hubResponse(res, out)
}

func (h *hubSession) logout(ctx context.Context) error {
	defer func() { h.client.Jar = nil }()
	res, err := h.do(ctx, http.MethodPost, "/api/v1/auth/logout", nil, "")
	if err != nil {
		return skillInputError(errors.New("SkillHub 临时登录会话注销失败，请重试绑定或联系管理员"))
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	// Spring Security redirects to the home page after invalidating SESSION.
	// Do not follow it: the application page is unrelated to cleanup.
	if res.StatusCode == http.StatusFound || res.StatusCode == http.StatusSeeOther {
		target, err := res.Location()
		if err == nil && target.Scheme == h.base.Scheme && strings.EqualFold(target.Host, h.base.Host) &&
			(target.Path == "/" || target.Path == strings.TrimRight(h.base.Path, "/")+"/") &&
			target.RawQuery == "" && target.Fragment == "" {
			return nil
		}
	}
	return skillInputError(fmt.Errorf("SkillHub 临时登录会话注销失败（HTTP %d），请重试绑定或联系管理员", res.StatusCode))
}

func (h *hubSession) packageRequest(ctx context.Context, p string, data []byte, confirm bool, out any) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	f, err := w.CreateFormFile("file", "skill.zip")
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	w.WriteField("visibility", "PUBLIC")
	w.WriteField("confirmWarnings", fmt.Sprint(confirm))
	if err = w.Close(); err != nil {
		return err
	}
	res, err := h.do(ctx, http.MethodPost, p, &buf, w.FormDataContentType())
	if err != nil {
		return err
	}
	return hubResponse(res, out)
}
func (h *hubSession) download(ctx context.Context, p string) ([]byte, error) {
	// This client is always anonymous. Credentials and browser cookies never
	// accompany discovery, package resolution, or storage redirects.
	res, err := h.do(ctx, http.MethodGet, p, nil, "")
	if err != nil {
		return nil, err
	}
	for i := 0; res.StatusCode >= 300 && res.StatusCode < 400 && i < 3; i++ {
		loc := res.Header.Get("Location")
		res.Body.Close()
		target, e := url.Parse(loc)
		if e != nil {
			return nil, errcode.ErrBadRequest
		}
		target = h.base.ResolveReference(target)
		allowed := strings.EqualFold(target.Host, h.base.Host)
		for _, host := range h.cfg.DownloadHosts {
			if strings.EqualFold(target.Host, host) {
				allowed = true
			}
		}
		if !allowed || target.User != nil || (target.Scheme != "https" && !(h.cfg.AllowHTTP && target.Scheme == "http")) {
			return nil, errcode.ErrForbidden.Wrap(errors.New("SkillHub 下载跳转地址不在允许清单中"))
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if e != nil {
			return nil, e
		}
		res, e = h.client.Do(req)
		if e != nil {
			return nil, skillInputError(errors.New("SkillHub 技能下载失败"))
		}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, skillInputError(errors.New("该公开技能版本无法下载"))
	}
	return archiveUpload(res.Body)
}
