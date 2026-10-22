package work

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/middleware"
	"github.com/63747756/jingjiaagent/backend/pkg/session"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/GoYoko/web"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/samber/do"
)

type workLocalAccount struct {
	domain.UserUsecase
	account *domain.User
	member  *domain.TeamMember
}

func (a *workLocalAccount) Get(context.Context, uuid.UUID) (*domain.User, error) {
	return a.account, nil
}
func (a *workLocalAccount) GetUserWithTeams(context.Context, uuid.UUID) (*domain.TeamUserInfo, error) {
	return &domain.TeamUserInfo{User: a.account, Teams: []*domain.TeamMember{a.member}}, nil
}

func TestWorkManagerUsesRealTeamSessionAndCurrentRole(t *testing.T) {
	for _, tc := range []struct {
		name               string
		userSession, admin bool
		want               int
	}{
		{"team admin session", false, true, http.StatusOK},
		{"demoted admin session", false, false, http.StatusForbidden},
		{"ordinary user session cannot substitute", true, true, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m := mockedService(t)
			w := web.New()
			mr := miniredis.RunT(t)
			port, _ := strconv.Atoi(mr.Port())
			cfg := &config.Config{}
			cfg.Redis.Host = "127.0.0.1"
			cfg.Redis.Port = port
			cfg.Session.ExpireDay = 1
			sess := session.New(cfg)
			uid, team := uuid.New(), uuid.New()
			account := &domain.User{ID: uid, Role: consts.UserRoleEnterprise, Status: consts.UserStatusActive, Team: &domain.Team{ID: team}}
			local := &workLocalAccount{account: account, member: &domain.TeamMember{UserID: uid, TeamID: team, TeamRole: consts.TeamMemberRoleAdmin}}
			i := do.New()
			do.ProvideValue(i, w)
			do.ProvideValue(i, s)
			do.ProvideValue(i, middleware.NewAuthMiddleware(sess, local, slog.New(slog.NewTextHandler(io.Discard, nil))))
			do.ProvideValue(i, &middleware.TargetActiveMiddleware{})
			if _, err := NewHandler(i); err != nil {
				t.Fatal(err)
			}
			saveReq := httptest.NewRequest(http.MethodGet, "/", nil)
			saveRec := httptest.NewRecorder()
			saveContext := w.Echo().NewContext(saveReq, saveRec)
			cookieName := consts.JingjiaAgentAITeamSession
			if tc.userSession {
				cookieName = consts.JingjiaAgentAISession
			}
			if _, err := sess.Save(saveContext, cookieName, uid, account); err != nil {
				t.Fatal(err)
			}
			if !tc.userSession {
				m.ExpectQuery("SELECT EXISTS.*team_members").WithArgs(uid, team, true).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(tc.admin))
				if tc.admin {
					m.ExpectQuery("SELECT .*FROM work_execution_profiles").WithArgs(uid, true).WillReturnRows(sqlmock.NewRows([]string{"id", "team", "name", "description", "model", "host", "image", "cli", "skills", "core", "memory", "enabled", "default"}))
				}
			}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/users/work/profiles", nil)
			for _, cookie := range saveRec.Result().Cookies() {
				req.AddCookie(cookie)
			}
			rec := httptest.NewRecorder()
			w.Echo().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d want %d body %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
