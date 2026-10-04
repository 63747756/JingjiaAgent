package usecase

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/samber/do"

	"github.com/chaitin/MonkeyCode/backend/biz/team/repo"
	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/errcode"
	"github.com/chaitin/MonkeyCode/backend/pkg/crypto"
)

type localMemberStore interface {
	Create(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, consts.UserRole, []repo.LocalMemberInput, bool) ([]*db.User, error)
}

type LocalMemberManager struct {
	store  localMemberStore
	email  domain.EmailSender
	redis  *redis.Client
	config *config.Config
}

func NewLocalMemberManager(i *do.Injector) (domain.MemberManager, error) {
	return &LocalMemberManager{
		store:  do.MustInvoke[*repo.LocalMemberStore](i),
		email:  do.MustInvoke[domain.EmailSender](i),
		redis:  do.MustInvoke[*redis.Client](i),
		config: do.MustInvoke[*config.Config](i),
	}, nil
}

func memberEmails(emails []string) ([]string, error) {
	if len(emails) == 0 {
		return nil, errcode.ErrEmailRequired
	}
	result := make([]string, 0, len(emails))
	seen := make(map[string]bool)
	for _, raw := range emails {
		email := strings.ToLower(strings.TrimSpace(raw))
		address, err := mail.ParseAddress(email)
		if err != nil || address.Address != email || strings.ContainsAny(email, "\r\n") {
			return nil, errcode.ErrEmailRequired
		}
		if seen[email] {
			return nil, errcode.ErrUserAlreadyExists
		}
		seen[email] = true
		result = append(result, email)
	}
	return result, nil
}

func memberPassword() (plain, hashed string, err error) {
	var value [12]byte
	if _, err = rand.Read(value[:]); err != nil {
		return "", "", err
	}
	plain = base64.RawURLEncoding.EncodeToString(value[:])
	// Current Web login submits the password directly. Preserve the running
	// implementation rather than the outdated MD5 comment on the API schema.
	hashed, err = crypto.HashPassword(plain)
	return
}

func memberActor(actor *domain.TeamUser) (uuid.UUID, uuid.UUID, error) {
	if actor == nil || actor.User == nil || actor.User.ID == uuid.Nil || actor.GetTeamID() == uuid.Nil {
		return uuid.Nil, uuid.Nil, errcode.ErrForbidden
	}
	return actor.GetTeamID(), actor.User.ID, nil
}

func (m *LocalMemberManager) createUsers(ctx context.Context, actor *domain.TeamUser, req *domain.AddTeamUserReq) ([]*domain.TeamUser, []*domain.TeamUserPassword, error) {
	teamID, actorID, err := memberActor(actor)
	if err != nil {
		return nil, nil, err
	}
	if req == nil {
		return nil, nil, errcode.ErrEmailRequired
	}
	emails, err := memberEmails(req.Emails)
	if err != nil {
		return nil, nil, err
	}
	inputs := make([]repo.LocalMemberInput, 0, len(emails))
	passwords := make([]*domain.TeamUserPassword, 0, len(emails))
	for _, email := range emails {
		plain, hashed, err := memberPassword()
		if err != nil {
			return nil, nil, errcode.ErrPasswordHashFailed
		}
		inputs = append(inputs, repo.LocalMemberInput{Email: email, PasswordHash: hashed})
		passwords = append(passwords, &domain.TeamUserPassword{Email: email, Password: plain})
	}
	accounts, err := m.store.Create(ctx, teamID, actorID, req.GroupID, consts.UserRoleSubAccount, inputs, false)
	if err != nil {
		return nil, nil, err
	}
	users := make([]*domain.TeamUser, 0, len(accounts))
	for _, account := range accounts {
		users = append(users, (&domain.TeamUser{}).From(account))
	}
	return users, passwords, nil
}

func (m *LocalMemberManager) AddUserWithPassword(ctx context.Context, actor *domain.TeamUser, req *domain.AddTeamUserReq) (*domain.AddTeamUserWithPasswordResp, error) {
	users, passwords, err := m.createUsers(ctx, actor, req)
	if err != nil {
		return nil, err
	}
	return &domain.AddTeamUserWithPasswordResp{Users: users, Passwords: passwords}, nil
}

func (m *LocalMemberManager) AddUser(ctx context.Context, actor *domain.TeamUser, req *domain.AddTeamUserReq) (*domain.AddTeamUserResp, error) {
	users, _, err := m.createUsers(ctx, actor, req)
	if err != nil {
		return nil, err
	}
	for _, account := range users {
		token := uuid.NewString()
		key := "reset_password_token:" + token
		if err := m.redis.Set(ctx, key, account.User.ID.String(), 24*time.Hour).Err(); err != nil {
			return nil, errcode.ErrHTTPRequest.Wrap(err)
		}
		resetURL := fmt.Sprintf("%s/resetpassword?token=%s", strings.TrimRight(m.config.Server.BaseURL, "/"), token)
		if err := m.email.SendResetPasswordEmail(ctx, account.User.Email, account.User.Name, resetURL); err != nil {
			_ = m.redis.Del(ctx, key).Err()
			return nil, errcode.ErrHTTPRequest.Wrap(err)
		}
	}
	return &domain.AddTeamUserResp{Users: users}, nil
}

func (m *LocalMemberManager) AddAdmin(ctx context.Context, actor *domain.TeamUser, req *domain.AddTeamAdminReq) (*domain.AddTeamAdminResp, error) {
	teamID, actorID, err := memberActor(actor)
	if err != nil {
		return nil, err
	}
	if req == nil || strings.TrimSpace(req.Name) == "" {
		return nil, errcode.ErrEmailRequired
	}
	emails, err := memberEmails([]string{req.Email})
	if err != nil {
		return nil, err
	}
	plain, hashed, err := memberPassword()
	if err != nil {
		return nil, errcode.ErrPasswordHashFailed
	}
	accounts, err := m.store.Create(ctx, teamID, actorID, uuid.Nil, consts.UserRoleEnterprise,
		[]repo.LocalMemberInput{{Email: emails[0], Name: req.Name, PasswordHash: hashed}}, false)
	if err != nil {
		return nil, err
	}
	return &domain.AddTeamAdminResp{User: (&domain.TeamUser{}).From(accounts[0]), Password: plain}, nil
}

func (m *LocalMemberManager) AutoCreateOIDCMember(ctx context.Context, teamID uuid.UUID, external *domain.OIDCExternalUser) (*domain.User, error) {
	if external == nil || !external.EmailVerified {
		return nil, errcode.ErrOIDCEmailNotVerified
	}
	if strings.TrimSpace(external.Issuer) == "" || strings.TrimSpace(external.Subject) == "" {
		return nil, errcode.ErrOIDCTokenInvalid
	}
	emails, err := memberEmails([]string{external.Email})
	if err != nil {
		return nil, errcode.ErrOIDCEmailRequired
	}
	// The existing verified OIDC callback enforces enabled/auto-create/domain
	// policy before invoking this interface. OIDC-only users get no password.
	accounts, err := m.store.Create(ctx, teamID, uuid.Nil, uuid.Nil, consts.UserRoleSubAccount,
		[]repo.LocalMemberInput{{Email: emails[0], Name: oidcDisplayName(external)}}, true)
	if err != nil {
		return nil, err
	}
	return (&domain.User{}).From(accounts[0]), nil
}

var _ domain.MemberManager = (*LocalMemberManager)(nil)
