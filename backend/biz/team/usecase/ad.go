package usecase

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"github.com/63747756/jingjiaagent/backend/biz/team/repo"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/adldap"
	"github.com/63747756/jingjiaagent/backend/pkg/cvt"
	"github.com/63747756/jingjiaagent/backend/pkg/secretbox"
	"github.com/go-ldap/ldap/v3"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/samber/do"
	"net/url"
	"strings"
	"time"
)

type adStore interface {
	DefaultTeamID(context.Context) (uuid.UUID, error)
	IsAdmin(context.Context, uuid.UUID, uuid.UUID) (bool, error)
	Get(context.Context, uuid.UUID) (*db.TeamADConfig, error)
	Enabled(context.Context) (*db.TeamADConfig, error)
	Save(context.Context, uuid.UUID, *domain.SaveTeamADConfigReq, string, int) (*db.TeamADConfig, error)
	Complete(context.Context, *db.TeamADConfig, *adldap.Profile) (*db.User, error)
}
type TeamADUsecase struct {
	repo      adStore
	cfg       *config.Config
	directory adldap.Directory
	redis     *redis.Client
}

func NewTeamADUsecase(i *do.Injector) (domain.TeamADUsecase, error) {
	return &TeamADUsecase{repo: do.MustInvoke[*repo.TeamADRepo](i), cfg: do.MustInvoke[*config.Config](i), directory: adldap.New(), redis: do.MustInvoke[*redis.Client](i)}, nil
}
func (u *TeamADUsecase) checkTeam(ctx context.Context, actor *domain.TeamUser) error {
	if actor == nil || actor.User == nil || actor.GetTeamID() == [16]byte{} {
		return errcode.ErrForbidden
	}
	id, err := u.repo.DefaultTeamID(ctx)
	if err != nil {
		return err
	}
	if id != actor.GetTeamID() {
		return errcode.ErrForbidden
	}
	allowed, err := u.repo.IsAdmin(ctx, actor.GetTeamID(), actor.User.ID)
	if err != nil {
		return err
	}
	if !allowed {
		return errcode.ErrForbidden
	}
	return nil
}
func adConfigResponse(c *db.TeamADConfig) *domain.TeamADConfig {
	return &domain.TeamADConfig{DirectoryID: c.DirectoryID, TeamID: c.TeamID, Enabled: c.Enabled, DisplayName: c.DisplayName, URL: c.URL, BaseDN: c.BaseDn, BindDN: c.BindDn, CAPEM: c.CaPem, AllowedGroupDNs: c.AllowedGroupDNS, HasBindPassword: c.BindPasswordCiphertext != "", Revision: c.Revision}
}
func (u *TeamADUsecase) GetConfig(ctx context.Context, actor *domain.TeamUser) (*domain.TeamADConfigResp, error) {
	if err := u.checkTeam(ctx, actor); err != nil {
		return nil, err
	}
	c, err := u.repo.Get(ctx, actor.GetTeamID())
	if db.IsNotFound(err) {
		return &domain.TeamADConfigResp{Config: &domain.TeamADConfig{TeamID: actor.GetTeamID(), DisplayName: "AD 域登录", AllowedGroupDNs: []string{}}}, nil
	}
	if err != nil {
		return nil, err
	}
	return &domain.TeamADConfigResp{Config: adConfigResponse(c)}, nil
}
func normalizeADRequest(req *domain.SaveTeamADConfigReq) error {
	req.URL = strings.TrimSpace(req.URL)
	req.BaseDN = strings.TrimSpace(req.BaseDN)
	req.BindDN = strings.TrimSpace(req.BindDN)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.DisplayName == "" {
		req.DisplayName = "AD 域登录"
	}
	if len(req.DisplayName) > 128 || len(req.CAPEM) > 128*1024 || len(req.BindPassword) > 4096 || len(req.AllowedGroupDNs) > 32 {
		return errcode.ErrADConfigInvalid
	}
	if req.URL != "" {
		parsed, err := url.Parse(req.URL)
		if err != nil || parsed.Scheme != "ldaps" || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errcode.ErrADConfigInvalid
		}
	}
	for _, dn := range []string{req.BaseDN, req.BindDN} {
		if dn != "" {
			if _, err := ldap.ParseDN(dn); err != nil {
				return errcode.ErrADConfigInvalid
			}
		}
	}
	seen := map[string]bool{}
	groups := []string{}
	for _, dn := range req.AllowedGroupDNs {
		dn = strings.TrimSpace(dn)
		if dn == "" {
			return errcode.ErrADConfigInvalid
		}
		parsed, err := ldap.ParseDN(dn)
		if err != nil {
			return errcode.ErrADConfigInvalid
		}
		key := strings.ToLower(parsed.String())
		if !seen[key] {
			seen[key] = true
			groups = append(groups, dn)
		}
	}
	req.AllowedGroupDNs = groups
	if req.CAPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(req.CAPEM)) {
			return errcode.ErrADConfigInvalid
		}
	}
	return nil
}
func (u *TeamADUsecase) candidate(ctx context.Context, actor *domain.TeamUser, req *domain.SaveTeamADConfigReq) (adldap.Config, string, int, error) {
	if err := u.checkTeam(ctx, actor); err != nil {
		return adldap.Config{}, "", 0, err
	}
	if err := normalizeADRequest(req); err != nil {
		return adldap.Config{}, "", 0, err
	}
	current, err := u.repo.Get(ctx, actor.GetTeamID())
	if err != nil && !db.IsNotFound(err) {
		return adldap.Config{}, "", 0, err
	}
	ciphertext := ""
	revision := 0
	if current != nil {
		ciphertext = current.BindPasswordCiphertext
		revision = current.Revision
	}
	if req.Revision != revision {
		return adldap.Config{}, "", 0, errcode.ErrADConfigConflict
	}
	password := req.BindPassword
	if req.BindPassword != "" || ciphertext != "" && req.Enabled {
		box, err := secretbox.NewFromFile(u.cfg.AD.SecretKeyFile)
		if err != nil {
			return adldap.Config{}, "", 0, errcode.ErrADUnavailable
		}
		if req.BindPassword != "" {
			ciphertext, err = box.Seal(req.BindPassword)
		} else {
			password, err = box.Open(ciphertext)
		}
		if err != nil {
			return adldap.Config{}, "", 0, errcode.ErrADUnavailable
		}
	}
	return adldap.Config{URL: req.URL, BaseDN: req.BaseDN, BindDN: req.BindDN, BindPassword: password, CAPEM: req.CAPEM, AllowedGroupDNs: req.AllowedGroupDNs}, ciphertext, revision, nil
}
func (u *TeamADUsecase) SaveConfig(ctx context.Context, actor *domain.TeamUser, req *domain.SaveTeamADConfigReq) (*domain.TeamADConfigResp, error) {
	c, ciphertext, revision, err := u.candidate(ctx, actor, req)
	if err != nil {
		return nil, err
	}
	if req.Enabled {
		if err := u.directory.Test(ctx, c); err != nil {
			return nil, mapADDirectoryError(err)
		}
	}
	result, err := u.repo.Save(ctx, actor.GetTeamID(), req, ciphertext, revision)
	if err != nil {
		return nil, err
	}
	return &domain.TeamADConfigResp{Config: adConfigResponse(result)}, nil
}
func (u *TeamADUsecase) TestConfig(ctx context.Context, actor *domain.TeamUser, req *domain.SaveTeamADConfigReq) (*domain.TeamADTestResp, error) {
	copyReq := *req
	copyReq.Enabled = true
	c, _, _, err := u.candidate(ctx, actor, &copyReq)
	if err != nil {
		return nil, err
	}
	if err := u.directory.Test(ctx, c); err != nil {
		return nil, mapADDirectoryError(err)
	}
	return &domain.TeamADTestResp{Success: true, Message: "连接测试通过；请继续验证真实员工登录与 OU 归组"}, nil
}
func (u *TeamADUsecase) PublicConfig(ctx context.Context) (*domain.AuthConfigResp, error) {
	result := &domain.AuthConfigResp{Mode: "local", DisplayName: "AD 域登录", AccountFormat: "short", SessionDays: u.cfg.Session.ExpireDay}
	c, err := u.repo.Enabled(ctx)
	if db.IsNotFound(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	result.Mode = "ad"
	result.DisplayName = c.DisplayName
	return result, nil
}
func (u *TeamADUsecase) RequireNonADLogin(ctx context.Context) error {
	c, err := u.PublicConfig(ctx)
	if err != nil {
		return err
	}
	if c.Mode == "ad" {
		return errcode.ErrADDisabled
	}
	return nil
}
func (u *TeamADUsecase) rateLimit(ctx context.Context, account, ip string) error {
	if u.redis == nil {
		return errcode.ErrADUnavailable
	}
	for _, rule := range []struct {
		kind, value string
		limit       int64
	}{{"account", strings.ToLower(strings.TrimSpace(account)), 5}, {"ip", ip, 60}} {
		hash := sha256.Sum256([]byte(rule.value))
		key := "jingjiaagent:ad:login:" + rule.kind + ":" + hex.EncodeToString(hash[:])
		count, err := u.redis.Eval(ctx, `local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],60) end; return n`, []string{key}).Int64()
		if err != nil {
			return errcode.ErrADUnavailable
		}
		if count > rule.limit {
			return errcode.ErrADRateLimited
		}
	}
	return nil
}
func (u *TeamADUsecase) Login(ctx context.Context, req *domain.ADLoginReq, ip string) (*domain.User, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := u.rateLimit(ctx, req.Account, ip); err != nil {
		return nil, err
	}
	c, err := u.repo.Enabled(ctx)
	if db.IsNotFound(err) {
		return nil, errcode.ErrADDisabled
	}
	if err != nil {
		return nil, err
	}
	box, err := secretbox.NewFromFile(u.cfg.AD.SecretKeyFile)
	if err != nil {
		return nil, errcode.ErrADUnavailable
	}
	password, err := box.Open(c.BindPasswordCiphertext)
	if err != nil {
		return nil, errcode.ErrADUnavailable
	}
	p, err := u.directory.Authenticate(ctx, adldap.Config{URL: c.URL, BaseDN: c.BaseDn, BindDN: c.BindDn, BindPassword: password, CAPEM: c.CaPem, AllowedGroupDNs: c.AllowedGroupDNS}, req.Account, req.Password)
	if err != nil {
		return nil, mapADDirectoryError(err)
	}
	account, err := u.repo.Complete(ctx, c, p)
	if err != nil {
		return nil, err
	}
	return cvt.From(account, &domain.User{}), nil
}
func mapADDirectoryError(err error) error {
	if errors.Is(err, adldap.ErrInvalidCredentials) {
		return errcode.ErrADInvalidCredentials
	}
	if errors.Is(err, adldap.ErrInvalidConfig) {
		return errcode.ErrADConfigInvalid
	}
	return errcode.ErrADUnavailable
}
