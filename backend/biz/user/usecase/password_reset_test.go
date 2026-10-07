package usecase

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/redis/go-redis/v9"
	"github.com/samber/do"

	userrepo "github.com/63747756/jingjiaagent/backend/biz/user/repo"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/enttest"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
)

type passwordResetMail struct {
	email, resetURL string
}

type passwordResetMailSink struct {
	domain.EmailSender
	received chan passwordResetMail
}

func (s *passwordResetMailSink) SendResetPasswordEmail(_ context.Context, email, _ string, resetURL string) error {
	s.received <- passwordResetMail{email: email, resetURL: resetURL}
	return nil
}

func TestSendResetPasswordEmailSelectsUniqueLocalIdentity(t *testing.T) {
	type account struct {
		email, source string
		role          consts.UserRole
		deleted       bool
	}
	local := account{email: "shared@example.invalid", source: "local", role: consts.UserRoleSubAccount}
	ad := account{email: local.email, source: "ad", role: consts.UserRoleSubAccount}
	enterprise := account{email: local.email, source: "local", role: consts.UserRoleEnterprise}
	individual := account{email: local.email, source: "local", role: consts.UserRoleIndividual}
	other := account{email: "other@example.invalid", source: "local", role: consts.UserRoleSubAccount}
	mixedCaseAD := account{email: "Shared@Example.Invalid", source: "ad", role: consts.UserRoleSubAccount}
	deletedLocal := local
	deletedLocal.deleted = true
	deletedAD := ad
	deletedAD.deleted = true
	for _, tt := range []struct {
		name     string
		accounts []account
		emails   []string
		wantIDs  []int
		wantErr  error
	}{
		{name: "local-only", accounts: []account{local}, emails: []string{local.email}, wantIDs: []int{0}},
		{name: "local-with-same-mail-AD", accounts: []account{local, ad, ad}, emails: []string{local.email}, wantIDs: []int{0}},
		{name: "AD-only", accounts: []account{ad}, emails: []string{ad.email}, wantErr: errcode.ErrADLocalPasswordDenied},
		{name: "mixed-case-AD-only", accounts: []account{mixedCaseAD}, emails: []string{" SHARED@EXAMPLE.INVALID "}, wantErr: errcode.ErrADLocalPasswordDenied},
		{name: "enterprise-and-local", accounts: []account{enterprise, local}, emails: []string{local.email}, wantIDs: []int{1}},
		{name: "enterprise-only", accounts: []account{enterprise}, emails: []string{enterprise.email}, wantErr: errcode.ErrEmailNotBound},
		{name: "case-fold-and-trim", accounts: []account{{email: "Shared@Example.Invalid", source: "local", role: local.role}}, emails: []string{" SHARED@EXAMPLE.INVALID "}, wantIDs: []int{0}},
		{name: "ambiguous-local-roles", accounts: []account{local, individual, ad}, emails: []string{local.email}, wantErr: errcode.ErrEmailNotBound},
		{name: "missing", emails: []string{"missing@example.invalid"}, wantErr: errcode.ErrEmailNotBound},
		{name: "valid-batch", accounts: []account{local, other, ad}, emails: []string{local.email, other.email}, wantIDs: []int{0, 1}},
		{name: "batch-rejected-before-any-token", accounts: []account{local}, emails: []string{local.email, "missing@example.invalid"}, wantErr: errcode.ErrEmailNotBound},
		{name: "equal-total-count-cannot-hide-ambiguity", accounts: []account{local, individual}, emails: []string{local.email, "missing@example.invalid"}, wantErr: errcode.ErrEmailNotBound},
		{name: "duplicate-normalized-request", accounts: []account{local, ad}, emails: []string{local.email, " SHARED@EXAMPLE.INVALID "}, wantIDs: []int{0}},
		{name: "deleted-local-does-not-block-live", accounts: []account{deletedLocal, local, ad}, emails: []string{local.email}, wantIDs: []int{1}},
		{name: "deleted-AD-is-not-an-existing-identity", accounts: []account{deletedAD}, emails: []string{ad.email}, wantErr: errcode.ErrEmailNotBound},
		{name: "blank-email", emails: []string{" "}, wantErr: errcode.ErrEmailRequired},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			client := enttest.Open(t, "sqlite3", "file:password-reset-"+uuid.NewString()+"?mode=memory&cache=shared&_fk=1")
			defer client.Close()
			accounts := make([]*db.User, 0, len(tt.accounts))
			for _, fixture := range tt.accounts {
				builder := client.User.Create().SetID(uuid.New()).SetName("Recovery test account").
					SetEmail(fixture.email).SetAuthSource(fixture.source).SetRole(fixture.role).SetStatus(consts.UserStatusActive)
				if fixture.deleted {
					builder.SetDeletedAt(time.Now())
				}
				accounts = append(accounts, builder.SaveX(ctx))
			}
			mr := miniredis.RunT(t)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			defer rdb.Close()
			cfg := &config.Config{}
			cfg.Server.BaseURL = "https://recovery.example.invalid"
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			i := do.New()
			do.ProvideValue(i, client)
			do.ProvideValue(i, logger)
			do.ProvideValue(i, rdb)
			do.ProvideValue(i, cfg)
			realRepo, err := userrepo.NewUserRepo(i)
			if err != nil {
				t.Fatal(err)
			}
			sender := &passwordResetMailSink{received: make(chan passwordResetMail, len(tt.emails)+1)}
			u := &UserUsecase{repo: realRepo, redis: rdb, config: cfg, email: sender, logger: logger}
			if err := u.SendResetPasswordEmail(ctx, &domain.ResetUserPasswordEmailReq{Emails: tt.emails}); err != tt.wantErr {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			keys := mr.Keys()
			if len(keys) != len(tt.wantIDs) {
				t.Fatalf("token count = %d, want %d", len(keys), len(tt.wantIDs))
			}
			wantIDs := make(map[string]string, len(tt.wantIDs))
			for _, index := range tt.wantIDs {
				wantIDs[accounts[index].ID.String()] = accounts[index].Email
			}
			for _, key := range keys {
				userID, err := rdb.Get(ctx, key).Result()
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := wantIDs[userID]; !ok {
					t.Fatalf("recovery token references an ineligible identity: %s", userID)
				}
				if ttl := mr.TTL(key); ttl != 24*time.Hour {
					t.Fatalf("token TTL = %v, want 24h", ttl)
				}
			}
			for range tt.wantIDs {
				select {
				case mail := <-sender.received:
					link, err := url.Parse(mail.resetURL)
					if err != nil || link.Path != "/resetpassword" {
						t.Fatal("invalid recovery link")
					}
					userID, err := rdb.Get(ctx, "jingjiaagent:reset_password_token:"+link.Query().Get("token")).Result()
					if err != nil || wantIDs[userID] != mail.email {
						t.Fatal("email and recovery identity do not match")
					}
					delete(wantIDs, userID)
				case <-time.After(time.Second):
					t.Fatal("recovery mail not sent")
				}
			}
			if len(sender.received) != 0 || len(wantIDs) != 0 {
				t.Fatal("missing or duplicate recovery mail")
			}
		})
	}
}
