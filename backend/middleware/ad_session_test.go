package middleware

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/pkg/session"
)

// Only local lookup methods are implemented. An unexpected authentication or AD
// call would panic through the embedded interface instead of silently succeeding.
type localAccountStateStub struct {
	domain.UserUsecase
	current   *domain.User
	getErr    error
	teamInfo  *domain.TeamUserInfo
	teamErr   error
	getCalls  int
	teamCalls int
}

func (s *localAccountStateStub) Get(context.Context, uuid.UUID) (*domain.User, error) {
	s.getCalls++
	if s.current == nil {
		return nil, s.getErr
	}
	copyUser := *s.current
	return &copyUser, s.getErr
}
func (s *localAccountStateStub) GetUserWithTeams(context.Context, uuid.UUID) (*domain.TeamUserInfo, error) {
	s.teamCalls++
	return s.teamInfo, s.teamErr
}

func newSessionTest(t *testing.T, state *localAccountStateStub) (*AuthMiddleware, *session.Session, *miniredis.Miniredis) {
	t.Helper()
	redis := miniredis.RunT(t)
	host, port, err := net.SplitHostPort(redis.Addr())
	if err != nil {
		t.Fatal(err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Redis.Host = host
	cfg.Redis.Port = number
	cfg.Session.ExpireDay = 30
	sess := session.New(cfg)
	return NewAuthMiddleware(sess, state, slog.New(slog.NewTextHandler(io.Discard, nil))), sess, redis
}

func saveTestSession(t *testing.T, sess *session.Session, name string, user *domain.User) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	c := echo.New().NewContext(request, response)
	if _, err := sess.Save(c, name, user.ID, user); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatal("session cookie missing")
	return nil
}

func runSessionMiddleware(t *testing.T, middleware echo.MiddlewareFunc, cookie *http.Cookie) (int, bool, *domain.User, *domain.TeamUser) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	called := false
	err := middleware(func(c echo.Context) error { called = true; return c.NoContent(http.StatusNoContent) })(c)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Code, called, GetUser(c), GetTeamUser(c)
}

func sessionStored(t *testing.T, sess *session.Session, cookie *http.Cookie) bool {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	u, err := session.Get[*domain.User](sess, echo.New().NewContext(req, httptest.NewRecorder()), cookie.Name)
	return err == nil && u != nil
}

func TestADSessionAuthorizationAlwaysChecksCurrentLocalState(t *testing.T) {
	userID, teamID := uuid.New(), uuid.New()
	for _, method := range []string{"Auth", "Check", "TeamAuth", "TeamAuthCheck"} {
		for _, scenario := range []string{"active", "blocked", "inactive", "deleted", "role changed", "database unavailable"} {
			t.Run(method+"/"+scenario, func(t *testing.T) {
				teamMode := method == "TeamAuth" || method == "TeamAuthCheck"
				role := consts.UserRoleSubAccount
				memberRole := consts.TeamMemberRoleUser
				name := consts.JingjiaAgentAISession
				if teamMode {
					role = consts.UserRoleEnterprise
					memberRole = consts.TeamMemberRoleAdmin
					name = consts.JingjiaAgentAITeamSession
				}
				cached := &domain.User{ID: userID, Name: "cached name", Role: role, Status: consts.UserStatusActive, AuthSource: "ad", Team: &domain.Team{ID: teamID, Name: "cached team"}}
				current := *cached
				current.Name = "current directory name"
				state := &localAccountStateStub{current: &current, teamInfo: &domain.TeamUserInfo{User: &current, Teams: []*domain.TeamMember{{TeamID: teamID, UserID: userID, TeamName: "current team", TeamRole: memberRole}}}}
				switch scenario {
				case "blocked":
					current.IsBlocked = true
				case "inactive":
					current.Status = consts.UserStatusInactive
				case "deleted":
					state.current = nil
					state.getErr = &db.NotFoundError{}
				case "role changed":
					current.Role = consts.UserRoleIndividual
				case "database unavailable":
					state.getErr = errors.New("temporary database outage")
				}
				a, sess, _ := newSessionTest(t, state)
				cookie := saveTestSession(t, sess, name, cached)
				var middleware echo.MiddlewareFunc
				switch method {
				case "Auth":
					middleware = a.Auth()
				case "Check":
					middleware = a.Check()
				case "TeamAuth":
					middleware = a.TeamAuth()
				case "TeamAuthCheck":
					middleware = a.TeamAuthCheck()
				}
				status, called, user, team := runSessionMiddleware(t, middleware, cookie)
				if state.getCalls != 1 {
					t.Fatalf("local account lookups=%d", state.getCalls)
				}
				switch scenario {
				case "active":
					if status != http.StatusNoContent || !called || !sessionStored(t, sess, cookie) {
						t.Fatalf("active user rejected: status=%d", status)
					}
					if teamMode {
						if team == nil || team.Team.Name != "current team" || team.User.Name != "current directory name" {
							t.Fatalf("team was not refreshed: %+v", team)
						}
					} else {
						if user == nil || user.Name != "current directory name" || user.Team == nil || user.Team.ID != teamID {
							t.Fatalf("user/team state not refreshed: %+v", user)
						}
					}
				case "database unavailable":
					if status != http.StatusServiceUnavailable || called || user != nil || team != nil || !sessionStored(t, sess, cookie) {
						t.Fatalf("database failure authorized or revoked user: status=%d", status)
					}
				default:
					wantStatus := http.StatusUnauthorized
					wantCalled := false
					if method == "Check" {
						wantStatus = http.StatusNoContent
						wantCalled = true
					}
					if status != wantStatus || called != wantCalled || user != nil || team != nil || sessionStored(t, sess, cookie) {
						t.Fatalf("invalid user authorized: status=%d called=%v", status, called)
					}
				}
			})
		}
	}
}

func TestTeamSessionRejectsMembershipAndRoleChanges(t *testing.T) {
	for _, scenario := range []string{"membership removed", "different team", "membership lookup unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			userID, teamID := uuid.New(), uuid.New()
			cached := &domain.User{ID: userID, Role: consts.UserRoleEnterprise, Status: consts.UserStatusActive, Team: &domain.Team{ID: teamID}}
			current := *cached
			state := &localAccountStateStub{current: &current, teamInfo: &domain.TeamUserInfo{User: &current, Teams: []*domain.TeamMember{{TeamID: teamID, UserID: userID, TeamRole: consts.TeamMemberRoleAdmin}}}}
			switch scenario {
			case "membership removed":
				state.teamInfo.Teams = nil
			case "different team":
				state.teamInfo.Teams[0].TeamID = uuid.New()
			case "membership lookup unavailable":
				state.teamErr = errors.New("temporary database outage")
			}
			a, sess, _ := newSessionTest(t, state)
			cookie := saveTestSession(t, sess, consts.JingjiaAgentAITeamSession, cached)
			status, called, _, team := runSessionMiddleware(t, a.TeamAuth(), cookie)
			want := http.StatusUnauthorized
			if scenario == "membership lookup unavailable" {
				want = http.StatusServiceUnavailable
			}
			if status != want || called || team != nil {
				t.Fatalf("invalid team authorized: status=%d", status)
			}
			if scenario == "membership lookup unavailable" && !sessionStored(t, sess, cookie) {
				t.Fatal("temporary lookup error revoked session")
			}
		})
	}
}

func TestTeamAdminPermissionIsCheckedByRouteAfterDemotion(t *testing.T) {
	userID, teamID := uuid.New(), uuid.New()
	cached := &domain.User{ID: userID, Role: consts.UserRoleEnterprise, Status: consts.UserStatusActive, Team: &domain.Team{ID: teamID}}
	current := *cached
	state := &localAccountStateStub{current: &current, teamInfo: &domain.TeamUserInfo{User: &current, Teams: []*domain.TeamMember{{TeamID: teamID, UserID: userID, TeamRole: consts.TeamMemberRoleUser}}}}
	a, sess, _ := newSessionTest(t, state)
	cookie := saveTestSession(t, sess, consts.JingjiaAgentAITeamSession, cached)
	// A member remains authenticated for routes that only require membership.
	status, called, _, team := runSessionMiddleware(t, a.TeamAuth(), cookie)
	if status != http.StatusNoContent || !called || team == nil {
		t.Fatal("generic team authentication unexpectedly grants administrator semantics")
	}
	admin := TeamAdminAuth(func(ctx context.Context, teamID, userID uuid.UUID) bool {
		for _, member := range state.teamInfo.Teams {
			if member.TeamID == teamID && member.UserID == userID {
				return member.TeamRole == consts.TeamMemberRoleAdmin
			}
		}
		return false
	})
	combined := func(next echo.HandlerFunc) echo.HandlerFunc { return a.TeamAuth()(admin(next)) }
	status, called, _, _ = runSessionMiddleware(t, combined, cookie)
	if status != http.StatusForbidden || called || !sessionStored(t, sess, cookie) {
		t.Fatal("administrator route accepted demoted member or revoked ordinary access")
	}
}

func TestNewSessionAfterAdministrativeRevocationStillCannotAuthorize(t *testing.T) {
	userID := uuid.New()
	cached := &domain.User{ID: userID, Status: consts.UserStatusActive, Role: consts.UserRoleSubAccount, AuthSource: "ad"}
	blocked := *cached
	blocked.IsBlocked = true
	state := &localAccountStateStub{current: &blocked}
	a, sess, _ := newSessionTest(t, state)
	old := saveTestSession(t, sess, consts.JingjiaAgentAISession, cached)
	team := saveTestSession(t, sess, consts.JingjiaAgentAITeamSession, cached)
	if err := sess.Trunc(context.Background(), consts.JingjiaAgentAISession, userID); err != nil {
		t.Fatal(err)
	}
	if sessionStored(t, sess, old) {
		t.Fatal("administrative revocation failed")
	}
	// A login that began before the block can finish after Trunc. Its newly issued
	// cookie must still fail local state validation on the following request.
	late := saveTestSession(t, sess, consts.JingjiaAgentAISession, cached)
	status, called, user, _ := runSessionMiddleware(t, a.Auth(), late)
	if status != http.StatusUnauthorized || called || user != nil || sessionStored(t, sess, late) || sessionStored(t, sess, team) {
		t.Fatal("late-issued session bypassed account block")
	}
}

func TestOptionalAuthRemainsAnonymousWithoutSession(t *testing.T) {
	state := &localAccountStateStub{}
	a, _, _ := newSessionTest(t, state)
	status, called, user, team := runSessionMiddleware(t, a.Check(), nil)
	if status != http.StatusNoContent || !called || user != nil || team != nil || state.getCalls != 0 {
		t.Fatal("anonymous optional request changed")
	}
}
