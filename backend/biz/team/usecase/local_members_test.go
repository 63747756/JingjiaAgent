package usecase

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/samber/do"

	"github.com/63747756/jingjiaagent/backend/biz/team/repo"
	userrepo "github.com/63747756/jingjiaagent/backend/biz/user/repo"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/enttest"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/crypto"
)

type localMemberStoreStub struct {
	inputs             []repo.LocalMemberInput
	role               consts.UserRole
	group, team, actor uuid.UUID
	oidc               bool
	calls              int
	err                error
}

func (s *localMemberStoreStub) Create(ctx context.Context, team, actor, group uuid.UUID, role consts.UserRole, inputs []repo.LocalMemberInput, oidc bool) ([]*db.User, error) {
	s.calls++
	s.inputs = inputs
	s.role = role
	s.group = group
	s.team = team
	s.actor = actor
	s.oidc = oidc
	if s.err != nil {
		return nil, s.err
	}
	accounts := make([]*db.User, 0, len(inputs))
	for _, input := range inputs {
		accounts = append(accounts, &db.User{ID: uuid.New(), Email: input.Email, Name: "member", Role: role, Status: consts.UserStatusActive,
			Password: input.PasswordHash, Edges: db.UserEdges{Teams: []*db.Team{{ID: team, Name: "fixture"}}}})
	}
	return accounts, nil
}

type localMemberEmailStub struct {
	domain.EmailSender
	email, resetURL string
	calls           int
	err             error
}

func (s *localMemberEmailStub) SendResetPasswordEmail(ctx context.Context, email, name, resetURL string) error {
	s.email = email
	s.resetURL = resetURL
	s.calls++
	return s.err
}

func TestLocalMemberPasswordUsesExistingLoginProtocol(t *testing.T) {
	store := &localMemberStoreStub{}
	m := &LocalMemberManager{store: store}
	actor := &domain.TeamUser{User: &domain.User{ID: uuid.New()}, Team: &domain.Team{ID: uuid.New()}}
	group := uuid.New()
	resp, err := m.AddUserWithPassword(context.Background(), actor, &domain.AddTeamUserReq{Emails: []string{" Member@Example.Invalid "}, GroupID: group})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Users) != 1 || len(resp.Passwords) != 1 || resp.Passwords[0].Email != "member@example.invalid" || len(resp.Passwords[0].Password) != 16 {
		t.Fatal("invalid initial password response")
	}
	if crypto.VerifyPassword(store.inputs[0].PasswordHash, resp.Passwords[0].Password) != nil {
		t.Fatal("generated password cannot use original Web login")
	}
	if store.inputs[0].PasswordHash == resp.Passwords[0].Password || store.actor != actor.User.ID || store.team != actor.Team.ID || store.group != group || store.role != consts.UserRoleSubAccount || store.oidc {
		t.Fatal("password or actor/group/role contract changed")
	}
	store.err = errcode.ErrUserAlreadyExists
	if resp, err := m.AddUserWithPassword(context.Background(), actor, &domain.AddTeamUserReq{Emails: []string{"member@example.invalid"}}); resp != nil || !errors.Is(err, errcode.ErrUserAlreadyExists) {
		t.Fatal("duplicate returned replacement credentials")
	}
}

func TestLocalMemberInitialPasswordWorksWithOriginalLoginRepository(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:local-member-login?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	i := do.New()
	do.ProvideValue(i, client)
	do.ProvideValue(i, redisClient)
	do.ProvideValue(i, &config.Config{})
	do.ProvideValue(i, slog.New(slog.NewTextHandler(io.Discard, nil)))
	login, err := userrepo.NewUserRepo(i)
	if err != nil {
		t.Fatal(err)
	}
	store := &localMemberStoreStub{}
	m := &LocalMemberManager{store: store}
	actor := &domain.TeamUser{User: &domain.User{ID: uuid.New()}, Team: &domain.Team{ID: uuid.New()}}
	resp, err := m.AddUserWithPassword(ctx, actor, &domain.AddTeamUserReq{Emails: []string{"login@example.invalid"}})
	if err != nil {
		t.Fatal(err)
	}
	client.User.Create().SetID(resp.Users[0].User.ID).SetEmail(store.inputs[0].Email).SetName("login member").
		SetPassword(store.inputs[0].PasswordHash).SetRole(consts.UserRoleSubAccount).SetStatus(consts.UserStatusActive).SaveX(ctx)
	account, err := login.PasswordLogin(ctx, &domain.TeamLoginReq{Email: resp.Passwords[0].Email, Password: resp.Passwords[0].Password})
	if err != nil || account.ID != resp.Users[0].User.ID {
		t.Fatalf("original login rejected initial password: %v", err)
	}
	if _, err := login.PasswordLogin(ctx, &domain.TeamLoginReq{Email: resp.Passwords[0].Email, Password: "wrong-password"}); err == nil {
		t.Fatal("original login accepted wrong password")
	}
}

func TestLocalMemberValidationPreventsPartialAdmission(t *testing.T) {
	for _, emails := range [][]string{nil, {"bad-address"}, {"a@example.invalid", " A@EXAMPLE.INVALID "}, {"display name <a@example.invalid>"}, {"a@example.invalid\r\nBcc: b@example.invalid"}} {
		store := &localMemberStoreStub{}
		m := &LocalMemberManager{store: store}
		actor := &domain.TeamUser{User: &domain.User{ID: uuid.New()}, Team: &domain.Team{ID: uuid.New()}}
		if _, err := m.AddUserWithPassword(context.Background(), actor, &domain.AddTeamUserReq{Emails: emails}); err == nil || store.calls != 0 {
			t.Fatal("invalid batch reached store")
		}
	}
	m := &LocalMemberManager{store: &localMemberStoreStub{}}
	if _, err := m.AddUserWithPassword(context.Background(), nil, &domain.AddTeamUserReq{Emails: []string{"a@example.invalid"}}); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatal("missing actor accepted")
	}
}

func TestLocalMemberAdminAndVerifiedOIDC(t *testing.T) {
	ctx := context.Background()
	store := &localMemberStoreStub{}
	m := &LocalMemberManager{store: store}
	actor := &domain.TeamUser{User: &domain.User{ID: uuid.New()}, Team: &domain.Team{ID: uuid.New()}}
	resp, err := m.AddAdmin(ctx, actor, &domain.AddTeamAdminReq{Email: "admin@example.invalid", Name: "Administrator", Password: "caller-cannot-choose-password"})
	if err != nil || store.role != consts.UserRoleEnterprise || len(resp.Password) != 16 || resp.Password == "caller-cannot-choose-password" {
		t.Fatalf("administrator creation: %v", err)
	}
	external := &domain.OIDCExternalUser{Issuer: "https://identity.example.invalid", Subject: "verified-subject", Email: "oidc@example.invalid", Name: "OIDC Member"}
	before := store.calls
	if _, err := m.AutoCreateOIDCMember(ctx, actor.Team.ID, external); !errors.Is(err, errcode.ErrOIDCEmailNotVerified) || before != store.calls {
		t.Fatal("unverified OIDC email created a user")
	}
	external.EmailVerified = true
	account, err := m.AutoCreateOIDCMember(ctx, actor.Team.ID, external)
	if err != nil || account.HasPassword || store.inputs[0].PasswordHash != "" || !store.oidc || store.actor != uuid.Nil || store.role != consts.UserRoleSubAccount {
		t.Fatalf("OIDC silently created password login: %v", err)
	}
	external.Subject = ""
	if _, err := m.AutoCreateOIDCMember(ctx, actor.Team.ID, external); !errors.Is(err, errcode.ErrOIDCTokenInvalid) {
		t.Fatal("missing OIDC subject accepted")
	}
}

func TestLocalMemberResetEmailUsesOriginalTokenAndReportsFailure(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	store := &localMemberStoreStub{}
	email := &localMemberEmailStub{}
	cfg := &config.Config{}
	cfg.Server.BaseURL = "https://web.example.invalid/"
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	m := &LocalMemberManager{store: store, email: email, redis: redisClient, config: cfg}
	actor := &domain.TeamUser{User: &domain.User{ID: uuid.New()}, Team: &domain.Team{ID: uuid.New()}}
	resp, err := m.AddUser(ctx, actor, &domain.AddTeamUserReq{Emails: []string{"mail@example.invalid"}})
	if err != nil || email.calls != 1 {
		t.Fatalf("reset mail: %v", err)
	}
	u, err := url.Parse(email.resetURL)
	if err != nil || u.Host != "web.example.invalid" || u.Path != "/resetpassword" {
		t.Fatal("reset route changed")
	}
	token := u.Query().Get("token")
	if _, err := uuid.Parse(token); err != nil {
		t.Fatal("reset token is not opaque handle")
	}
	key := "jingjiaagent:reset_password_token:" + token
	value, err := mr.Get(key)
	if err != nil || value != resp.Users[0].User.ID.String() || mr.TTL(key) != 24*time.Hour {
		t.Fatal("original reset-password token/TTL contract changed")
	}
	email.err = errors.New("synthetic SMTP rejection")
	resp, err = m.AddUser(ctx, actor, &domain.AddTeamUserReq{Emails: []string{"failed-mail@example.invalid"}})
	if err == nil || resp != nil {
		t.Fatal("mail rejection reported success")
	}
	u, _ = url.Parse(email.resetURL)
	if mr.Exists("jingjiaagent:reset_password_token:" + u.Query().Get("token")) {
		t.Fatal("rejected mail retained a usable token")
	}
	if strings.Contains(err.Error(), store.inputs[0].PasswordHash) {
		t.Fatal("failure leaked password hash")
	}
}
