package repo

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/db/teamgroupmember"
	"github.com/chaitin/MonkeyCode/backend/errcode"
)

func checkGroupReplacement(t *testing.T, client *db.Client) {
	t.Helper()
	ctx := context.Background()
	r := &TeamGroupUserRepo{db: client}
	teamID, adminID := localMemberFixture(t, client, 5)
	otherTeam, otherAdmin := localMemberFixture(t, client, 5)
	group := client.TeamGroup.Create().SetID(uuid.New()).SetTeamID(teamID).SetName("replace fixture").SaveX(ctx)
	foreignGroup := client.TeamGroup.Create().SetID(uuid.New()).SetTeamID(otherTeam).SetName("foreign fixture").SaveX(ctx)
	first, err := r.ModifyGroupUsers(ctx, group.ID, []uuid.UUID{adminID, adminID})
	if err != nil || len(first) != 1 || first[0].Edges.User == nil {
		t.Fatalf("initial membership: %v", err)
	}
	if _, err := r.ModifyGroupUsers(ctx, group.ID, []uuid.UUID{otherAdmin}); !errors.Is(err, errcode.ErrNotFound) {
		t.Fatalf("foreign member assigned: %v", err)
	}
	if count := client.TeamGroupMember.Query().Where(teamgroupmember.GroupIDEQ(group.ID), teamgroupmember.UserIDEQ(adminID)).CountX(ctx); count != 1 {
		t.Fatal("rejected replacement removed existing authorization")
	}
	repeated, err := r.ModifyGroupUsers(ctx, group.ID, []uuid.UUID{adminID})
	if err != nil || len(repeated) != 1 || repeated[0].ID != first[0].ID {
		t.Fatal("identical replacement recreated membership")
	}
	if err := r.Delete(ctx, teamID, foreignGroup.ID); !errors.Is(err, errcode.ErrNotFound) {
		t.Fatal("foreign team group deleted")
	}
	if _, err := client.TeamGroup.Get(ctx, foreignGroup.ID); err != nil {
		t.Fatal("foreign group was changed")
	}
	if _, err := r.ModifyGroupUsers(ctx, group.ID, nil); err != nil {
		t.Fatal(err)
	}
	if count := client.TeamGroupMember.Query().Where(teamgroupmember.GroupIDEQ(group.ID)).CountX(ctx); count != 0 {
		t.Fatal("empty selection did not revoke memberships")
	}
	if _, err := r.ModifyGroupUsers(ctx, group.ID, []uuid.UUID{adminID}); err != nil {
		t.Fatal("membership could not be restored")
	}
}

func TestGroupMembersReplaceAndRejectForeignUsers(t *testing.T) {
	checkGroupReplacement(t, newTeamRepoTestDB(t))
}

func TestGroupMembersPostgresReplaceAndRejectForeignUsers(t *testing.T) {
	checkGroupReplacement(t, localMembersPostgres(t))
}
