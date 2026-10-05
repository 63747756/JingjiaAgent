package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
)

func TestComposeInstallerCannotGenerateTaskflowCommand(t *testing.T) {
	// No Redis, token or network access is needed when the legacy installer is
	// unavailable. A fresh install token cannot bypass the backend boundary.
	u := &HostUsecase{cfg: &config.Config{Runtime: config.Runtime{Backend: "agent_compose"}}}
	if _, err := u.GetInstallCommand(context.Background(), &domain.User{}); !errors.Is(err, errcode.ErrRuntimeNodeInstaller) {
		t.Fatal("compose generated legacy install command")
	}
	if _, err := u.InstallScript(context.Background(), &domain.InstallReq{Token: "any"}); !errors.Is(err, errcode.ErrRuntimeNodeInstaller) {
		t.Fatal("compose generated legacy installer script")
	}
}

type installScopeUserRepo struct {
	domain.UserRepo
	user *db.User
	err  error
	id   uuid.UUID
}

func (r *installScopeUserRepo) GetUserWithTeams(_ context.Context, id uuid.UUID) (*db.User, error) {
	r.id = id
	return r.user, r.err
}

func TestRuntimeInstallTeamUsesCurrentAdminTeamWithoutLoginMetadata(t *testing.T) {
	actor, team := uuid.New(), uuid.New()
	r := &installScopeUserRepo{user: &db.User{ID: actor, Status: consts.UserStatusActive,
		Edges: db.UserEdges{Teams: []*db.Team{{ID: team}},
			TeamMembers: []*db.TeamMember{{TeamID: team, Role: consts.TeamMemberRoleAdmin}}}}}
	u := &HostUsecase{userRepo: r}
	// This is the original password-login response: the user ID is present but
	// Team is nil. The console status request obtains the team separately.
	got, err := u.runtimeInstallTeam(context.Background(), &domain.User{ID: actor})
	if err != nil || got != team.String() || r.id != actor {
		t.Fatalf("password-login scope = %q, %v; expected current admin team", got, err)
	}
}

func TestRuntimeInstallTeamRechecksSelectedTeamAndAccount(t *testing.T) {
	actor, team, other := uuid.New(), uuid.New(), uuid.New()
	lookupFailure := errors.New("account lookup unavailable")
	tests := []struct {
		name    string
		cached  *domain.Team
		current *db.User
		lookup  error
		want    string
		wantErr error
	}{
		{"live selected admin", &domain.Team{ID: team}, &db.User{Status: consts.UserStatusActive,
			Edges: db.UserEdges{TeamMembers: []*db.TeamMember{{TeamID: team, Role: consts.TeamMemberRoleAdmin}}}}, nil, team.String(), nil},
		{"revoked cached admin cannot select team node", &domain.Team{ID: team}, &db.User{Status: consts.UserStatusActive,
			Edges: db.UserEdges{TeamMembers: []*db.TeamMember{{TeamID: team, Role: consts.TeamMemberRoleUser}}}}, nil, "", nil},
		{"ordinary member without cached team selects only private host", nil, &db.User{Status: consts.UserStatusActive,
			Edges: db.UserEdges{Teams: []*db.Team{{ID: team}}, TeamMembers: []*db.TeamMember{{TeamID: team, Role: consts.TeamMemberRoleUser}}}}, nil, "", nil},
		{"foreign cached team", &domain.Team{ID: other}, &db.User{Status: consts.UserStatusActive,
			Edges: db.UserEdges{TeamMembers: []*db.TeamMember{{TeamID: team, Role: consts.TeamMemberRoleAdmin}}}}, nil, "", errcode.ErrRuntimeInstallScope},
		{"blocked account", nil, &db.User{Status: consts.UserStatusActive, IsBlocked: true}, nil, "", errcode.ErrRuntimeInstallScope},
		{"missing account", nil, nil, nil, "", errcode.ErrRuntimeInstallScope},
		{"lookup failure cannot select personal node", nil, nil, lookupFailure, "", lookupFailure},
		{"personal scope", nil, &db.User{Status: consts.UserStatusActive}, nil, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &installScopeUserRepo{user: tt.current, err: tt.lookup}
			got, err := (&HostUsecase{userRepo: r}).runtimeInstallTeam(context.Background(), &domain.User{ID: actor, Team: tt.cached})
			if got != tt.want || !errors.Is(err, tt.wantErr) {
				t.Fatalf("scope = %q, %v; want %q, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
