package v1

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/GoYoko/web"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/middleware"
)

type groupScopeRepo struct {
	domain.TeamGroupUserRepo
	group *db.TeamGroup
	err   error
}

func (r *groupScopeRepo) Get(context.Context, uuid.UUID) (*db.TeamGroup, error) {
	return r.group, r.err
}
func (r *groupScopeRepo) GetMember(context.Context, uuid.UUID, uuid.UUID) (*db.TeamMember, error) {
	return nil, errcode.ErrNotFound
}

func groupScopeContext(teamID uuid.UUID) *web.Context {
	e := echo.New()
	c := &web.Context{Context: e.NewContext(httptest.NewRequest("GET", "/", nil), httptest.NewRecorder())}
	middleware.SetTeamUser(c, &domain.TeamUser{User: &domain.User{ID: uuid.New()}, Team: &domain.Team{ID: teamID}})
	return c
}

func TestTeamGroupHandlersRejectForeignGroupBeforeUsecase(t *testing.T) {
	teamID, groupID := uuid.New(), uuid.New()
	h := &TeamGroupUserHandler{repo: &groupScopeRepo{group: &db.TeamGroup{ID: groupID, TeamID: uuid.New()}}}
	c := groupScopeContext(teamID)
	for _, action := range []func() error{
		func() error { return h.Update(c, domain.UpdateTeamGroupReq{GroupID: groupID, Name: "forged"}) },
		func() error { return h.Delete(c, domain.DeleteTeamGroupReq{GroupID: groupID}) },
		func() error { return h.ListGroupUsers(c, domain.ListTeamGroupUsersReq{GroupID: groupID}) },
		func() error {
			return h.ModifyGroupUsers(c, domain.AddTeamGroupUsersReq{GroupID: groupID, UserIDs: []uuid.UUID{uuid.New()}})
		},
	} {
		if err := action(); !errors.Is(err, errcode.ErrNotFound) {
			t.Fatalf("foreign group was not denied before usecase: %v", err)
		}
	}
	if err := h.UpdateUser(c, domain.UpdateTeamUserReq{UserID: uuid.New()}); !errors.Is(err, errcode.ErrNotFound) {
		t.Fatal("foreign user reached update usecase")
	}
}

func TestTeamGroupScopePreservesDatabaseFailure(t *testing.T) {
	teamID := uuid.New()
	failure := errors.New("synthetic database outage")
	h := &TeamGroupUserHandler{repo: &groupScopeRepo{err: failure}}
	if err := h.checkGroupAccess(groupScopeContext(teamID), uuid.New()); !errors.Is(err, failure) {
		t.Fatal("database outage was treated as an authorization success/denial")
	}
	h.repo = &groupScopeRepo{group: &db.TeamGroup{TeamID: teamID}}
	if err := h.checkGroupAccess(groupScopeContext(teamID), uuid.New()); err != nil {
		t.Fatal("own group was denied")
	}
}
