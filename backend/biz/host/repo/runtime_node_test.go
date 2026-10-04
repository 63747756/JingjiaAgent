package repo

import (
	"context"
	"testing"

	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db/enttest"
	"github.com/chaitin/MonkeyCode/backend/db/teamgrouphost"
	"github.com/chaitin/MonkeyCode/backend/db/teamhost"
	"github.com/chaitin/MonkeyCode/backend/db/teammember"
	"github.com/chaitin/MonkeyCode/backend/pkg/entx"
	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
	"github.com/google/uuid"
)

func TestRuntimeEnrollmentPreservesOwnerAndRevokedGrants(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:runtime-node-enrollment?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	owner, member, outsider, teamID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{owner, member, outsider} {
		client.User.Create().SetID(id).SetName("fixture").SetRole(consts.UserRoleEnterprise).SetStatus(consts.UserStatusActive).SaveX(ctx)
	}
	client.Team.Create().SetID(teamID).SetName("runtime-node-team").SetMemberLimit(10).SaveX(ctx)
	client.TeamMember.Create().SetID(uuid.New()).SetTeamID(teamID).SetUserID(owner).SetRole(consts.TeamMemberRoleAdmin).SaveX(ctx)
	r := &HostRepo{db: client}
	node := config.RuntimeNode{ID: uuid.NewString(), OwnerID: owner.String(), TeamID: teamID.String()}
	info := &taskflow.Host{ID: node.ID, Hostname: "docker-node", Arch: "x86_64", OS: "linux", Cores: 8, Memory: 16 << 30, Disk: 50 << 30, Version: "p7"}
	if err := r.SyncRuntimeHost(ctx, node, info); err != nil {
		t.Fatal(err)
	}
	grant := client.TeamGroupHost.Query().Where(teamgrouphost.HostID(node.ID)).OnlyX(ctx)
	client.TeamGroupMember.Create().SetID(uuid.New()).SetGroupID(grant.GroupID).SetUserID(member).SaveX(ctx)
	for actor, want := range map[string]int{owner.String(): 1, member.String(): 1, outsider.String(): 0} {
		hosts, err := r.ListRuntimeHosts(ctx, actor, []string{node.ID})
		if err != nil || len(hosts) != want {
			t.Fatal("runtime enrollment bypassed original permissions")
		}
	}
	client.Host.UpdateOneID(node.ID).SetRemark("keep remark").SetWeight(7).ExecX(ctx)
	client.TeamGroupHost.DeleteOneID(grant.ID).ExecX(ctx)
	info.Cores = 6
	if err := r.SyncRuntimeHost(ctx, node, info); err != nil {
		t.Fatal(err)
	}
	updated := client.Host.GetX(ctx, node.ID)
	if updated.UserID != owner || updated.Remark != "keep remark" || updated.Weight != 7 || updated.Cores != 6 || updated.Disk != 50<<30 || updated.Version != "p7" {
		t.Fatal("heartbeat overwrote host policy or omitted actual metadata")
	}
	if visible, _ := r.ListRuntimeHosts(ctx, member.String(), []string{node.ID}); len(visible) != 0 {
		t.Fatal("heartbeat restored revoked group grant")
	}
	if client.TeamHost.Query().Where(teamhost.HostID(node.ID)).CountX(ctx) != 1 {
		t.Fatal("heartbeat duplicated team binding")
	}
	wrong := node
	wrong.OwnerID = outsider.String()
	if err := r.SyncRuntimeHost(ctx, wrong, info); err == nil {
		t.Fatal("configuration changed host ownership")
	}
	client.Host.DeleteOneID(node.ID).ExecX(ctx)
	if err := r.SyncRuntimeHost(ctx, node, info); err == nil {
		t.Fatal("heartbeat resurrected deleted host")
	}
	if client.Host.GetX(entx.SkipSoftDelete(ctx), node.ID).DeletedAt.IsZero() {
		t.Fatal("host tombstone erased")
	}
	newNode := node
	newNode.ID = uuid.NewString()
	newNode.OwnerID = member.String()
	copy := *info
	copy.ID = newNode.ID
	if err := r.SyncRuntimeHost(ctx, newNode, &copy); err == nil {
		t.Fatal("ordinary member enrolled team host")
	}
	if exists, _ := client.Host.Query().Where().Exist(ctx); exists {
		t.Fatal("failed team registration committed host")
	}
}

func TestRuntimeInstallerRechecksCurrentTeamAndOwner(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:runtime-install-scope?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })
	owner, admin, member, other, teamID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{owner, admin, member, other} {
		client.User.Create().SetID(id).SetName("fixture").SetRole(consts.UserRoleEnterprise).SetStatus(consts.UserStatusActive).SaveX(ctx)
	}
	client.Team.Create().SetID(teamID).SetName("install-team").SetMemberLimit(10).SaveX(ctx)
	for _, id := range []uuid.UUID{owner, admin} {
		client.TeamMember.Create().SetID(uuid.New()).SetTeamID(teamID).SetUserID(id).SetRole(consts.TeamMemberRoleAdmin).SaveX(ctx)
	}
	client.TeamMember.Create().SetID(uuid.New()).SetTeamID(teamID).SetUserID(member).SetRole(consts.TeamMemberRoleUser).SaveX(ctx)
	r := &HostRepo{db: client}
	node := config.RuntimeNode{ID: uuid.NewString(), OwnerID: owner.String(), TeamID: teamID.String()}
	for _, id := range []uuid.UUID{owner, admin} {
		if err := r.AuthorizeRuntimeInstall(ctx, id.String(), teamID.String(), node); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uuid.UUID{member, other} {
		if err := r.AuthorizeRuntimeInstall(ctx, id.String(), teamID.String(), node); err == nil {
			t.Fatal("ordinary or unrelated user could install node")
		}
	}
	client.TeamMember.Update().Where(teammember.TeamID(teamID), teammember.UserID(admin)).SetRole(consts.TeamMemberRoleUser).ExecX(ctx)
	if err := r.AuthorizeRuntimeInstall(ctx, admin.String(), teamID.String(), node); err == nil {
		t.Fatal("revoked administrator could use issued ticket")
	}
	client.User.UpdateOneID(owner).SetIsBlocked(true).ExecX(ctx)
	if err := r.AuthorizeRuntimeInstall(ctx, owner.String(), teamID.String(), node); err == nil {
		t.Fatal("blocked owner could install node")
	}
	client.User.UpdateOneID(owner).SetIsBlocked(false).ExecX(ctx)
	node.TeamID = ""
	if err := r.AuthorizeRuntimeInstall(ctx, owner.String(), "", node); err != nil {
		t.Fatal(err)
	}
	if err := r.AuthorizeRuntimeInstall(ctx, admin.String(), "", node); err == nil {
		t.Fatal("other administrator could install personal node")
	}
	client.Host.Create().SetID(node.ID).SetUserID(other).ExecX(ctx)
	if err := r.AuthorizeRuntimeInstall(ctx, owner.String(), "", node); err == nil {
		t.Fatal("configuration changed existing ownership")
	}
	client.Host.UpdateOneID(node.ID).SetUserID(owner).ExecX(ctx)
	client.Host.DeleteOneID(node.ID).ExecX(ctx)
	if err := r.AuthorizeRuntimeInstall(ctx, owner.String(), "", node); err == nil {
		t.Fatal("installer revived deleted host")
	}
}
