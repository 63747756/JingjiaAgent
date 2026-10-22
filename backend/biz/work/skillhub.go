package work

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/secretbox"
	"github.com/google/uuid"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type HubStatus struct {
	Configured bool   `json:"configured"`
	Bound      bool   `json:"bound"`
	URL        string `json:"url"`
	Namespace  string `json:"namespace"`
	Account    string `json:"account,omitempty"`
}

func (s *Service) hub(ctx context.Context) (*hubSession, error) {
	if err := s.enabled(); err != nil {
		return nil, err
	}
	return newHubSession(s.cfg.Work.SkillHub, false)
}
func (s *Service) hubNamespace() string {
	if s.cfg.Work.SkillHub.Namespace != "" {
		return s.cfg.Work.SkillHub.Namespace
	}
	return "global"
}
func (s *Service) HubStatus(ctx context.Context, u *domain.User) (HubStatus, error) {
	out := HubStatus{URL: strings.TrimRight(s.cfg.Work.SkillHub.URL, "/"), Namespace: s.hubNamespace()}
	if out.URL == "" {
		return out, nil
	}
	if _, err := s.hub(ctx); err != nil {
		return out, err
	}
	out.Configured = true
	h, _ := s.hub(ctx)
	out.URL = h.publicEndpoint("")
	var name string
	err := s.store.db.QueryRowContext(ctx, `SELECT remote_name FROM work_skillhub_bindings WHERE user_id=$1 AND registry_url=$2`, u.ID, h.base.String()).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Bound = true
	out.Account = name
	return out, nil
}
func (s *Service) hubBox() (*secretbox.Box, error) {
	p := s.cfg.Work.SkillHub.SecretKeyFile
	if p == "" {
		p = s.cfg.AD.SecretKeyFile
	}
	box, err := secretbox.NewFromFile(p)
	if err != nil {
		return nil, skillInputError(errors.New("请管理员配置 SkillHub 凭证加密密钥"))
	}
	return box, nil
}

type HubBindRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Service) BindHub(ctx context.Context, u *domain.User, r HubBindRequest) (out HubStatus, err error) {
	if len(r.Username) > 256 || len(r.Password) > 4096 || strings.TrimSpace(r.Username) == "" || r.Password == "" {
		return HubStatus{}, errcode.ErrBadRequest
	}
	box, err := s.hubBox()
	if err != nil {
		return HubStatus{}, err
	}
	h, err := newHubSession(s.cfg.Work.SkillHub, true)
	if err != nil {
		return HubStatus{}, err
	}
	defer h.client.CloseIdleConnections()
	unlock, err := s.store.requestLock(ctx, u.ID, uuid.NewSHA1(uuid.Nil, []byte("skillhub-binding")))
	if err != nil {
		return HubStatus{}, err
	}
	defer unlock()
	var methods []struct {
		Provider string `json:"provider"`
		Type     string `json:"methodType"`
	}
	if err = h.json(ctx, http.MethodGet, "/api/v1/auth/methods", nil, &methods); err != nil {
		return HubStatus{}, err
	}
	provider := ""
	for _, m := range methods {
		if m.Provider == "ad" && m.Type == "DIRECT_PASSWORD" {
			provider = "ad"
			break
		}
		if m.Provider == "local" && m.Type == "PASSWORD" {
			provider = "local"
		}
	}
	if provider == "" {
		return HubStatus{}, skillInputError(errors.New("SkillHub 未开放可用的账号密码登录方式"))
	}
	loginPath := "/api/v1/auth/local/login"
	input := map[string]string{"username": r.Username, "password": r.Password}
	if provider == "ad" {
		loginPath = "/api/v1/auth/direct/login"
		input["provider"] = "ad"
	}
	var account struct {
		ID   string `json:"userId"`
		Name string `json:"displayName"`
	}
	// Also clean up when login succeeds remotely but its response, token
	// creation or local persistence fails. A cancelled request must not skip it.
	sessionClosed := false
	closeSession := func() error {
		if sessionClosed {
			return nil
		}
		sessionClosed = true
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return h.logout(cleanup)
	}
	defer func() {
		if cleanupErr := closeSession(); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	err = h.json(ctx, http.MethodPost, loginPath, input, &account)
	delete(input, "password")
	r.Password = ""
	if err != nil {
		return HubStatus{}, err
	}
	if account.ID == "" {
		return HubStatus{}, errcode.ErrBadRequest
	}
	var token struct {
		Token string `json:"token"`
		ID    int64  `json:"id"`
	}
	instance := s.cfg.Work.SkillHub.InstanceID
	if instance == "" {
		sum := sha256.Sum256([]byte(h.base.String()))
		instance = hex.EncodeToString(sum[:6])
	}
	if len(instance) > 24 {
		instance = instance[:24]
	}
	err = h.json(ctx, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "JingjiaAgent-" + instance + "-" + u.ID.String()[:8], "scopes": []string{"skill:read", "skill:publish"}, "expiresAt": time.Now().Add(90 * 24 * time.Hour).UTC().Format(time.RFC3339)}, &token)
	if err != nil {
		return HubStatus{}, err
	}
	if token.Token == "" {
		return HubStatus{}, errcode.ErrBadRequest
	}
	encrypted, err := box.Seal(token.Token)
	token.Token = ""
	if err != nil {
		return HubStatus{}, err
	}
	// Do not report or persist a successful binding if session cleanup failed.
	if err = closeSession(); err != nil {
		return HubStatus{}, err
	}
	_, err = s.store.db.ExecContext(ctx, `INSERT INTO work_skillhub_bindings(user_id,registry_url,remote_user_id,remote_name,token_ciphertext,token_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(user_id) DO UPDATE SET registry_url=excluded.registry_url,remote_user_id=excluded.remote_user_id,remote_name=excluded.remote_name,token_ciphertext=excluded.token_ciphertext,token_id=excluded.token_id,updated_at=now()`, u.ID, h.base.String(), account.ID, account.Name, encrypted, token.ID)
	if err != nil {
		return HubStatus{}, err
	}
	return s.HubStatus(ctx, u)
}
func (s *Service) UnbindHub(ctx context.Context, u *domain.User) error {
	_, err := s.store.db.ExecContext(ctx, `DELETE FROM work_skillhub_bindings WHERE user_id=$1`, u.ID)
	return err
}
func (s *Service) authenticatedHub(ctx context.Context, u *domain.User) (*hubSession, error) {
	h, err := s.hub(ctx)
	if err != nil {
		return nil, err
	}
	var cipher string
	err = s.store.db.QueryRowContext(ctx, `SELECT token_ciphertext FROM work_skillhub_bindings WHERE user_id=$1 AND registry_url=$2`, u.ID, h.base.String()).Scan(&cipher)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, skillInputError(errors.New("请先绑定 SkillHub 账号"))
	}
	if err != nil {
		return nil, err
	}
	box, err := s.hubBox()
	if err != nil {
		return nil, err
	}
	h.token, err = box.Open(cipher)
	if err != nil {
		return nil, skillInputError(errors.New("SkillHub 授权不可用，请重新绑定"))
	}
	return h, nil
}
func (s *Service) HubMarket(ctx context.Context, q string, page, size int) (json.RawMessage, error) {
	h, err := s.hub(ctx)
	if err != nil {
		return nil, err
	}
	if page < 0 {
		page = 0
	}
	if size < 1 || size > 50 {
		size = 20
	}
	if len(q) > 256 {
		return nil, errcode.ErrBadRequest
	}
	values := url.Values{"q": {q}, "page": {strconv.Itoa(page)}, "size": {strconv.Itoa(size)}, "sort": {"newest"}}
	var out json.RawMessage
	err = h.json(ctx, http.MethodGet, "/api/web/skills?"+values.Encode(), nil, &out)
	return out, err
}
func (s *Service) HubDetail(ctx context.Context, namespace, slug string) (json.RawMessage, error) {
	if !hubCoordinate.MatchString(namespace) || !hubCoordinate.MatchString(slug) {
		return nil, errcode.ErrBadRequest
	}
	h, err := s.hub(ctx)
	if err != nil {
		return nil, err
	}
	var out json.RawMessage
	err = h.json(ctx, http.MethodGet, "/api/v1/skills/"+namespace+"/"+slug, nil, &out)
	return out, err
}

type HubInstallRequest struct {
	Namespace string `json:"namespace"`
	Slug      string `json:"slug"`
	Version   string `json:"version"`
}

func (s *Service) InstallHubSkill(ctx context.Context, u *domain.User, r HubInstallRequest) (SkillView, error) {
	if !hubCoordinate.MatchString(r.Namespace) || !hubCoordinate.MatchString(r.Slug) || len(r.Version) > 100 || r.Version == "" {
		return SkillView{}, errcode.ErrBadRequest
	}
	h, err := s.hub(ctx)
	if err != nil {
		return SkillView{}, err
	}
	detail, err := s.HubDetail(ctx, r.Namespace, r.Slug)
	if err != nil {
		return SkillView{}, err
	}
	var visibility struct {
		Visibility string `json:"visibility"`
	}
	json.Unmarshal(detail, &visibility)
	if visibility.Visibility != "PUBLIC" {
		return SkillView{}, errcode.ErrForbidden
	}
	var version struct {
		Version   string `json:"version"`
		VersionID int64  `json:"versionId"`
	}
	p := "/api/cli/v1/skills/" + r.Namespace + "/" + r.Slug
	if err = h.json(ctx, http.MethodGet, p+"/resolve?version="+url.QueryEscape(r.Version), nil, &version); err != nil {
		return SkillView{}, err
	}
	if version.Version != r.Version {
		return SkillView{}, errcode.ErrBadRequest
	}
	unlock, err := s.store.requestLock(ctx, u.ID, uuid.NewSHA1(uuid.Nil, []byte("install:"+h.base.String()+":"+p)))
	if err != nil {
		return SkillView{}, err
	}
	defer unlock()
	var id uuid.UUID
	var current string
	err = s.store.db.QueryRowContext(ctx, `SELECT i.skill_id,i.remote_version FROM work_skillhub_installs i JOIN agent_skills s ON s.id=i.skill_id WHERE i.user_id=$1 AND i.registry_url=$2 AND i.namespace=$3 AND i.slug=$4 AND NOT s.is_deleted`, u.ID, h.base.String(), r.Namespace, r.Slug).Scan(&id, &current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SkillView{}, err
	}
	if err == nil && current == r.Version {
		return s.Skill(ctx, u, id)
	}
	data, err := h.download(ctx, p+"/versions/"+url.PathEscape(r.Version)+"/download")
	if err != nil {
		return SkillView{}, err
	}
	sourceURL := h.publicEndpoint("/space/" + r.Namespace + "/" + r.Slug)
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return SkillView{}, err
	}
	defer tx.Rollback()
	id, err = s.savePersonalPackageTx(ctx, tx, u, id, data, map[string]any{"source_type": "market", "source_label": "SkillHub · " + r.Namespace + "/" + r.Slug, "source_url": sourceURL, "remote_version": r.Version})
	if err != nil {
		return SkillView{}, err
	}
	sum := sha256.Sum256(data)
	_, err = tx.ExecContext(ctx, `INSERT INTO work_skillhub_installs(user_id,registry_url,namespace,slug,skill_id,remote_version,package_hash) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(user_id,registry_url,namespace,slug) DO UPDATE SET skill_id=excluded.skill_id,remote_version=excluded.remote_version,package_hash=excluded.package_hash,updated_at=now()`, u.ID, h.base.String(), r.Namespace, r.Slug, id, r.Version, hex.EncodeToString(sum[:]))
	if err != nil {
		return SkillView{}, err
	}
	if err = tx.Commit(); err != nil {
		return SkillView{}, err
	}
	return s.Skill(ctx, u, id)
}

type HubUploadRequest struct {
	ID              uuid.UUID `param:"id" json:"-"`
	Version         string    `json:"version"`
	ConfirmWarnings bool      `json:"confirm_warnings"`
}
type HubPrecheck struct {
	Valid     bool     `json:"valid"`
	Errors    []string `json:"errors"`
	Warnings  []string `json:"warnings"`
	Slug      string   `json:"resolvedSlug"`
	Version   string   `json:"resolvedVersion"`
	Namespace string   `json:"namespace"`
	SkillName string   `json:"skill_name"`
}

var semanticVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

func (s *Service) hubUploadPackage(ctx context.Context, u *domain.User, r HubUploadRequest) ([]byte, SkillView, error) {
	if len(r.Version) > 100 || !semanticVersion.MatchString(r.Version) {
		return nil, SkillView{}, skillInputError(errors.New("请输入版本号，例如 1.0.0"))
	}
	view, err := s.skill(ctx, u, r.ID, false)
	if err != nil {
		return nil, view, err
	}
	if view.Scope.Type != "user" || view.Scope.ID != u.ID.String() || view.ActiveVersionID == nil {
		return nil, view, errcode.ErrForbidden
	}
	var key string
	err = s.store.db.QueryRowContext(ctx, `SELECT s3_key FROM agent_skill_versions WHERE id=$1 AND resource_id=$2`, *view.ActiveVersionID, view.ID).Scan(&key)
	if err != nil {
		return nil, view, err
	}
	data, err := s.readSkillPackage(ctx, key)
	if err != nil {
		return nil, view, err
	}
	data, _, err = normalizeSkillZip(data)
	if err != nil {
		return nil, view, err
	}
	content, _ := skillMarkdown(data)
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, view, errcode.ErrBadRequest
	}
	found := false
	for i := 1; i < end; i++ {
		if strings.HasPrefix(lines[i], "version:") {
			lines[i] = "version: " + r.Version
			found = true
		}
	}
	if !found {
		lines = append(lines[:end], append([]string{"version: " + r.Version}, lines[end:]...)...)
	}
	data, err = replaceSkillMarkdown(data, strings.Join(lines, "\n"))
	return data, view, err
}
func (s *Service) PrecheckHubUpload(ctx context.Context, u *domain.User, r HubUploadRequest) (HubPrecheck, error) {
	h, err := s.authenticatedHub(ctx, u)
	if err != nil {
		return HubPrecheck{}, err
	}
	data, view, err := s.hubUploadPackage(ctx, u, r)
	if err != nil {
		return HubPrecheck{}, err
	}
	return s.precheckHubPackage(ctx, h, data, view)
}

func (s *Service) precheckHubPackage(ctx context.Context, h *hubSession, data []byte, view SkillView) (HubPrecheck, error) {
	out := HubPrecheck{Errors: []string{}, Warnings: []string{}, Namespace: s.hubNamespace()}
	if !hubCoordinate.MatchString(out.Namespace) {
		return out, errcode.ErrBadRequest
	}
	err := h.packageRequest(ctx, "/api/cli/v1/skills/"+out.Namespace+"/publish/validate", data, false, &out)
	out.SkillName = view.Name
	out.Namespace = s.hubNamespace()
	return out, err
}

type HubUploadResult struct {
	Status    string `json:"status"`
	URL       string `json:"url"`
	Version   string `json:"version"`
	Namespace string `json:"namespace"`
	Slug      string `json:"slug"`
}

func (s *Service) UploadHubSkill(ctx context.Context, u *domain.User, r HubUploadRequest) (HubUploadResult, error) {
	var out HubUploadResult
	h, err := s.authenticatedHub(ctx, u)
	if err != nil {
		return out, err
	}
	data, view, err := s.hubUploadPackage(ctx, u, r)
	if err != nil {
		return out, err
	}
	// Validate the exact bytes whose version and digest will be recorded and
	// published, even if the employee saves another version during this call.
	pre, err := s.precheckHubPackage(ctx, h, data, view)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	namespace := s.hubNamespace()
	out = HubUploadResult{Namespace: namespace, Slug: pre.Slug, Version: r.Version, URL: h.publicEndpoint("/space/" + namespace + "/" + pre.Slug)}
	unlock, err := s.store.requestLock(ctx, u.ID, uuid.NewSHA1(uuid.Nil, []byte("upload:"+view.ID.String()+":"+r.Version)))
	if err != nil {
		return out, err
	}
	defer unlock()
	var state, priorHash string
	var record uuid.UUID
	err = s.store.db.QueryRowContext(ctx, `SELECT id,status,package_hash FROM work_skillhub_uploads WHERE user_id=$1 AND skill_id=$2 AND registry_url=$3 AND namespace=$4 AND remote_version=$5`, u.ID, view.ID, h.base.String(), namespace, r.Version).Scan(&record, &state, &priorHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	found := err == nil
	if found && state != "failed" {
		if priorHash != hash {
			return out, skillInputError(errors.New("该版本已有上传记录，请修改版本号"))
		}
		if state != "pending" && state != "unknown" {
			out.Status = state
			return out, nil
		}
		var remote struct {
			Status string `json:"status"`
		}
		if e := h.json(ctx, http.MethodGet, "/api/v1/skills/"+namespace+"/"+pre.Slug+"/versions/"+url.PathEscape(r.Version), nil, &remote); e == nil && remote.Status != "" {
			out.Status = remote.Status
			s.store.db.ExecContext(ctx, `UPDATE work_skillhub_uploads SET status=$2,updated_at=now() WHERE id=$1`, record, remote.Status)
			return out, nil
		}
		return out, skillInputError(errors.New("上次上传结果尚未确认，请在 SkillHub 核对该版本，系统不会重复发布"))
	}
	if len(pre.Errors) > 0 || (!pre.Valid && !(r.ConfirmWarnings && len(pre.Warnings) > 0)) {
		return out, skillInputError(errors.New("请先处理 SkillHub 上传校验问题"))
	}
	if found {
		// A definite rejection permits retrying this version, including a package
		// corrected in response to validation errors. Persist before sending.
		_, err = s.store.db.ExecContext(ctx, `UPDATE work_skillhub_uploads SET version_id=$2,slug=$3,package_hash=$4,status='pending',result='{}',updated_at=now() WHERE id=$1`, record, *view.ActiveVersionID, pre.Slug, hash)
	} else {
		record = uuid.New()
		_, err = s.store.db.ExecContext(ctx, `INSERT INTO work_skillhub_uploads(id,user_id,skill_id,version_id,registry_url,namespace,slug,remote_version,package_hash,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending')`, record, u.ID, view.ID, *view.ActiveVersionID, h.base.String(), namespace, pre.Slug, r.Version, hash)
	}
	if err != nil {
		return out, err
	}
	var remote struct {
		Status  string `json:"status"`
		Slug    string `json:"slug"`
		Version string `json:"version"`
	}
	err = h.packageRequest(ctx, "/api/v1/skills/"+namespace+"/publish", data, r.ConfirmWarnings, &remote)
	if err != nil {
		status := "unknown"
		if hubPublishRejected(err) {
			status = "failed"
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, saveErr := s.store.db.ExecContext(cleanup, `UPDATE work_skillhub_uploads SET status=$2,updated_at=now() WHERE id=$1`, record, status)
		if saveErr != nil {
			return out, errors.Join(err, saveErr)
		}
		return out, err
	}
	if remote.Status == "" {
		remote.Status = "UPLOADED"
	}
	out.Status = remote.Status
	raw, _ := json.Marshal(out)
	_, err = s.store.db.ExecContext(ctx, `UPDATE work_skillhub_uploads SET status=$2,result=$3,updated_at=now() WHERE id=$1`, record, out.Status, string(raw))
	return out, err
}
