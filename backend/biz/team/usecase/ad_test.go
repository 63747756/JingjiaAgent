package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/adldap"
	"github.com/63747756/jingjiaagent/backend/pkg/secretbox"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type adStoreStub struct {
	team               uuid.UUID
	config             *db.TeamADConfig
	admin              bool
	saves, completions int
	completeErr        error
}

func (s *adStoreStub) DefaultTeamID(context.Context) (uuid.UUID, error) { return s.team, nil }
func (s *adStoreStub) IsAdmin(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return s.admin, nil
}
func (s *adStoreStub) Get(context.Context, uuid.UUID) (*db.TeamADConfig, error) { return s.config, nil }
func (s *adStoreStub) Enabled(context.Context) (*db.TeamADConfig, error)        { return s.config, nil }
func (s *adStoreStub) Save(_ context.Context, _ uuid.UUID, r *domain.SaveTeamADConfigReq, c string, _ int) (*db.TeamADConfig, error) {
	s.saves++
	s.config = &db.TeamADConfig{TeamID: s.team, Enabled: r.Enabled, BindPasswordCiphertext: c, Revision: 1}
	return s.config, nil
}
func (s *adStoreStub) Complete(_ context.Context, _ *db.TeamADConfig, p *adldap.Profile) (*db.User, error) {
	s.completions++
	if s.completeErr != nil {
		return nil, s.completeErr
	}
	return &db.User{ID: uuid.New(), AuthSource: "ad", LoginName: p.Username, Status: consts.UserStatusActive}, nil
}

type adDirectoryStub struct {
	tests, logins     int
	config            adldap.Config
	account, password string
	err               error
}

func (s *adDirectoryStub) Test(_ context.Context, c adldap.Config) error {
	s.tests++
	s.config = c
	return s.err
}
func (s *adDirectoryStub) Authenticate(_ context.Context, c adldap.Config, a, p string) (*adldap.Profile, error) {
	s.logins++
	s.config = c
	s.account = a
	s.password = p
	if s.err != nil {
		return nil, s.err
	}
	return &adldap.Profile{GUID: "directory-user", Username: a}, nil
}

func adUsecaseFixture(t *testing.T) (*TeamADUsecase, *adStoreStub, *adDirectoryStub, *domain.TeamUser, *miniredis.Miniredis) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	s := &adStoreStub{team: uuid.New(), admin: true}
	d := &adDirectoryStub{}
	cfg := &config.Config{}
	cfg.AD.SecretKeyFile = path
	cfg.Session.ExpireDay = 30
	u := &TeamADUsecase{repo: s, cfg: cfg, directory: d, redis: rdb}
	actor := &domain.TeamUser{Team: &domain.Team{ID: s.team}, User: &domain.User{ID: uuid.New()}}
	return u, s, d, actor, mr
}

func TestADConfigDraftEnableAndPasswordPreservation(t *testing.T) {
	u, s, d, actor, _ := adUsecaseFixture(t)
	req := &domain.SaveTeamADConfigReq{BindPassword: " query-secret "}
	resp, err := u.SaveConfig(context.Background(), actor, req)
	if err != nil {
		t.Fatal(err)
	}
	if d.tests != 0 || s.saves != 1 || !resp.Config.HasBindPassword {
		t.Fatal("offline draft did not persist securely")
	}
	encoded, _ := json.Marshal(resp)
	if strings.Contains(string(encoded), "query-secret") || strings.Contains(string(encoded), "ciphertext") {
		t.Fatal("response exposed bind secret")
	}
	box, err := secretbox.NewFromFile(u.cfg.AD.SecretKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := box.Open(s.config.BindPasswordCiphertext)
	if original != " query-secret " {
		t.Fatal("bind password was trimmed")
	}
	d.err = adldap.ErrUnavailable
	enable := &domain.SaveTeamADConfigReq{Enabled: true, Revision: 1}
	if _, err := u.SaveConfig(context.Background(), actor, enable); !errors.Is(err, errcode.ErrADUnavailable) {
		t.Fatalf("enable: %v", err)
	}
	if s.saves != 1 || d.config.BindPassword != original {
		t.Fatal("failed enable mutated config or lost saved password")
	}
	d.err = nil
	if _, err := u.TestConfig(context.Background(), actor, enable); err != nil {
		t.Fatal(err)
	}
	if s.saves != 1 {
		t.Fatal("connection test changed persisted config")
	}
	if _, err := u.SaveConfig(context.Background(), actor, &domain.SaveTeamADConfigReq{Revision: 0}); !errors.Is(err, errcode.ErrADConfigConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	s.admin = false
	if _, err := u.GetConfig(context.Background(), actor); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatalf("non-admin read: %v", err)
	}
}

func TestADLoginRateLimitRawPasswordAndAtomicFailure(t *testing.T) {
	u, s, d, _, mr := adUsecaseFixture(t)
	box, _ := secretbox.NewFromFile(u.cfg.AD.SecretKeyFile)
	cipher, _ := box.Seal("query-secret")
	s.config = &db.TeamADConfig{Enabled: true, TeamID: s.team, BindPasswordCiphertext: cipher, Revision: 2}
	ctx := context.Background()
	req := &domain.ADLoginReq{Account: "employee", Password: " user password "}
	if _, err := u.Login(ctx, req, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if d.password != req.Password || d.config.BindPassword != "query-secret" {
		t.Fatal("credentials changed at boundary")
	}
	s.completeErr = errcode.ErrADConfigConflict
	if _, err := u.Login(ctx, req, "192.0.2.1"); !errors.Is(err, errcode.ErrADConfigConflict) {
		t.Fatalf("completion error: %v", err)
	}
	d.err = adldap.ErrInvalidCredentials
	for i := 0; i < 3; i++ {
		if _, err := u.Login(ctx, req, "192.0.2.1"); !errors.Is(err, errcode.ErrADInvalidCredentials) {
			t.Fatalf("credential error: %v", err)
		}
	}
	if _, err := u.Login(ctx, req, "192.0.2.1"); !errors.Is(err, errcode.ErrADRateLimited) {
		t.Fatalf("account limit: %v", err)
	}
	if d.logins != 5 {
		t.Fatal("rate-limited request reached directory")
	}
	mr.FastForward(time.Minute)
	if _, err := u.Login(ctx, req, "192.0.2.1"); !errors.Is(err, errcode.ErrADInvalidCredentials) {
		t.Fatalf("expired limit: %v", err)
	}
	u.redis = nil
	if _, err := u.Login(ctx, req, "192.0.2.1"); !errors.Is(err, errcode.ErrADUnavailable) {
		t.Fatal("limiter failure did not fail closed")
	}
}

func TestADConfigNormalizationRejectsUnsafeConnection(t *testing.T) {
	for _, address := range []string{"ldap://dc:389", "ldaps://user:password@dc", "ldaps://dc/path", "ldaps://dc?insecure=1"} {
		if err := normalizeADRequest(&domain.SaveTeamADConfigReq{URL: address}); !errors.Is(err, errcode.ErrADConfigInvalid) {
			t.Fatalf("unsafe address accepted: %s", address)
		}
	}
	r := &domain.SaveTeamADConfigReq{URL: " ldaps://dc:636 ", BaseDN: "DC=example,DC=test", AllowedGroupDNs: []string{"CN=AI,DC=example,DC=test", "cn=AI,dc=example,dc=test"}, BindPassword: " secret "}
	if err := normalizeADRequest(r); err != nil {
		t.Fatal(err)
	}
	if len(r.AllowedGroupDNs) != 1 || r.BindPassword != " secret " {
		t.Fatal("normalization changed password or failed DN dedup")
	}
}
