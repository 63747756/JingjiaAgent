package agentresource

import (
	"context"
	"testing"
	"time"

	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db/agentplugin"
	"github.com/chaitin/MonkeyCode/backend/db/agentpluginrepo"
	"github.com/chaitin/MonkeyCode/backend/db/agentskill"
	"github.com/chaitin/MonkeyCode/backend/db/agentskillrepo"
	"github.com/chaitin/MonkeyCode/backend/db/teamgroupmember"
	"github.com/chaitin/MonkeyCode/backend/db/teammember"
	"github.com/google/uuid"
)

func TestSkillGroupListingDispatchAndRevocation(t *testing.T) {
	ctx := context.Background()
	c := newTestDB(t, t.Name())
	teamID, otherTeamID, memberID, otherID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{teamID, otherTeamID} {
		c.Team.Create().SetID(id).SetName("scope fixture").SetMemberLimit(5).SaveX(ctx)
	}
	for _, id := range []uuid.UUID{memberID, otherID} {
		c.User.Create().SetID(id).SetName("scope fixture").SetRole(consts.UserRoleSubAccount).SetStatus(consts.UserStatusActive).SaveX(ctx)
		c.TeamMember.Create().SetID(uuid.New()).SetUserID(id).SetTeamID(teamID).SetRole(consts.TeamMemberRoleUser).SaveX(ctx)
	}
	group := c.TeamGroup.Create().SetID(uuid.New()).SetTeamID(teamID).SetName("allowed").SaveX(ctx)
	foreign := c.TeamGroup.Create().SetID(uuid.New()).SetTeamID(otherTeamID).SetName("foreign private label").SaveX(ctx)
	c.TeamGroupMember.Create().SetID(uuid.New()).SetUserID(memberID).SetGroupID(group.ID).SaveX(ctx)
	c.TeamGroupMember.Create().SetID(uuid.New()).SetUserID(memberID).SetGroupID(foreign.ID).SaveX(ctx)
	repoID := seedSkillRepo(t, ctx, c)
	makeSkill := func(name string, forced bool, groups ...uuid.UUID) uuid.UUID {
		id := seedSkill(t, ctx, c, repoID, skillSeed{name: name, withVersion: true, isForceDelivery: forced})
		c.AgentSkill.UpdateOneID(id).SetScopeType(agentskill.ScopeTypeTeam).SetScopeID(teamID.String()).SaveX(ctx)
		for _, gid := range groups {
			c.AgentSkillGroupBinding.Create().SetSkillID(id).SetGroupID(gid).SaveX(ctx)
		}
		return id
	}
	shared := makeSkill("team-shared", false)
	restricted := makeSkill("restricted", true, group.ID, foreign.ID)
	foreignOnly := makeSkill("cross-team", true, foreign.ID)
	r := NewRepo(c)
	assertScope := func(scope ScopeFilter, names []string) {
		t.Helper()
		listed, err := r.ListSkillsForListingScoped(ctx, scope)
		if err != nil || !equalStringSet(listingNames(listed), names) {
			t.Fatalf("listing = %v, %v; want %v", listingNames(listed), err, names)
		}
		active, err := r.ListActiveSkillsScoped(ctx, SkillSelection{Scope: scope, UserSelectedIDs: []uuid.UUID{shared, restricted, foreignOnly}})
		if err != nil || !equalStringSet(skillNames(active), names) {
			t.Fatalf("dispatch = %v, %v; want %v", skillNames(active), err, names)
		}
		for _, item := range listed {
			for _, ref := range item.Groups {
				if ref.ID == foreign.ID {
					t.Fatal("foreign group label leaked")
				}
			}
		}
	}
	scope := ScopeFilter{TeamID: &teamID, MemberID: &memberID}
	assertScope(scope, []string{"team-shared", "restricted"})
	assertScope(ScopeFilter{TeamID: &teamID, MemberID: &otherID}, []string{"team-shared"})
	assertScope(ScopeFilter{TeamID: &teamID}, []string{"team-shared"})
	assertScope(ScopeFilter{}, nil)
	c.TeamGroupMember.Delete().Where(teamgroupmember.GroupIDEQ(group.ID), teamgroupmember.UserIDEQ(memberID)).ExecX(ctx)
	assertScope(scope, []string{"team-shared"})
	c.TeamGroupMember.Create().SetID(uuid.New()).SetUserID(memberID).SetGroupID(group.ID).SaveX(ctx)
	assertScope(scope, []string{"team-shared", "restricted"})
	c.TeamGroup.UpdateOneID(group.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	assertScope(scope, []string{"team-shared"})
	c.TeamGroup.UpdateOneID(group.ID).ClearDeletedAt().SaveX(ctx)
	c.TeamMember.Delete().Where(teammember.UserIDEQ(memberID), teammember.TeamIDEQ(teamID)).ExecX(ctx)
	assertScope(scope, []string{"team-shared"})
}

func TestScopedOverridesDoNotDispatchHiddenOrDisabledVersions(t *testing.T) {
	ctx := context.Background()
	c := newTestDB(t, t.Name())
	teamID := uuid.New()
	r := NewRepo(c)
	scope := ScopeFilter{IncludeGlobal: true, TeamID: &teamID}
	skillRepo, pluginRepo := seedSkillRepo(t, ctx, c), seedPluginRepo(t, ctx, c)
	teamSkillRepo := c.AgentSkillRepo.Create().SetName("team fixture").SetSourceType(agentskillrepo.SourceTypeBare).SetCreatedBy(uuid.New()).SaveX(ctx).ID
	teamPluginRepo := c.AgentPluginRepo.Create().SetName("team fixture").SetSourceType(agentpluginrepo.SourceTypeBare).SetCreatedBy(uuid.New()).SaveX(ctx).ID
	globalSkill := seedSkill(t, ctx, c, skillRepo, skillSeed{name: "same", withVersion: true, isForceDelivery: true})
	teamSkill := seedSkill(t, ctx, c, teamSkillRepo, skillSeed{name: "same", withVersion: true})
	c.AgentSkill.UpdateOneID(teamSkill).SetName("same").SetScopeType(agentskill.ScopeTypeTeam).SetScopeID(teamID.String()).SetEnabled(false).SaveX(ctx)
	globalPlugin := seedPlugin(t, ctx, c, pluginRepo, pluginSeed{name: "same", withVersion: true, isForceDelivery: true})
	teamPlugin := seedPlugin(t, ctx, c, teamPluginRepo, pluginSeed{name: "same", withVersion: true})
	c.AgentPlugin.UpdateOneID(teamPlugin).SetName("same").SetScopeType(agentplugin.ScopeTypeTeam).SetScopeID(teamID.String()).SetEnabled(false).SaveX(ctx)
	check := func(selected []uuid.UUID, want int) {
		t.Helper()
		skills, err := r.ListActiveSkillsScoped(ctx, SkillSelection{Scope: scope, UserSelectedIDs: selected})
		if err != nil || len(skills) != want {
			t.Fatalf("skills: %v, %v", skills, err)
		}
		plugins, err := r.ListActivePluginsScoped(ctx, SkillSelection{Scope: scope, UserSelectedIDs: selected})
		if err != nil || len(plugins) != want {
			t.Fatalf("plugins: %v, %v", plugins, err)
		}
		if want > 0 && (skills[0].ID != teamSkill || plugins[0].ID != teamPlugin) {
			t.Fatal("hidden global resource dispatched")
		}
	}
	check([]uuid.UUID{globalSkill, globalPlugin, teamSkill, teamPlugin}, 0)
	c.AgentSkill.UpdateOneID(teamSkill).SetEnabled(true).SaveX(ctx)
	c.AgentPlugin.UpdateOneID(teamPlugin).SetEnabled(true).SaveX(ctx)
	check([]uuid.UUID{globalSkill, globalPlugin}, 0)
	check([]uuid.UUID{teamSkill, teamPlugin}, 1)
	check(nil, 0)
}
