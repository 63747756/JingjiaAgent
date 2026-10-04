package repo

import (
	"context"
	stdsql "database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/chaitin/MonkeyCode/backend/consts"
	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/db/teamgroupmember"
	"github.com/chaitin/MonkeyCode/backend/db/teammember"
	"github.com/chaitin/MonkeyCode/backend/db/user"
	"github.com/chaitin/MonkeyCode/backend/errcode"
)

func localMemberFixture(t *testing.T, client *db.Client, limit int) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	teamID, adminID := uuid.New(), uuid.New()
	client.Team.Create().SetID(teamID).SetName("members fixture").SetMemberLimit(limit).SaveX(ctx)
	client.User.Create().SetID(adminID).SetName("administrator").SetEmail(uuid.NewString() + "@example.invalid").
		SetRole(consts.UserRoleEnterprise).SetStatus(consts.UserStatusActive).SetPassword("existing-admin-hash").SaveX(ctx)
	client.TeamMember.Create().SetID(uuid.New()).SetTeamID(teamID).SetUserID(adminID).SetRole(consts.TeamMemberRoleAdmin).SaveX(ctx)
	return teamID, adminID
}

func TestLocalMembersAtomicScopeAndQuota(t *testing.T) {
	ctx := context.Background()
	client := newTeamRepoTestDB(t)
	r := &LocalMemberStore{db: client}
	teamID, adminID := localMemberFixture(t, client, 2)
	otherTeamID, otherAdminID := localMemberFixture(t, client, 5)
	otherGroup := client.TeamGroup.Create().SetID(uuid.New()).SetTeamID(otherTeamID).SetName("private group").SaveX(ctx)
	create := func(actorID, groupID uuid.UUID, emails ...string) ([]*db.User, error) {
		inputs := make([]LocalMemberInput, 0, len(emails))
		for _, email := range emails {
			inputs = append(inputs, LocalMemberInput{Email: email, PasswordHash: "initial-hash"})
		}
		return r.Create(ctx, teamID, actorID, groupID, consts.UserRoleSubAccount, inputs, false)
	}
	if _, err := create(otherAdminID, uuid.Nil, "unauthorized@example.invalid"); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatalf("foreign admin: %v", err)
	}
	if _, err := create(adminID, otherGroup.ID, "wrong-group@example.invalid"); !errors.Is(err, errcode.ErrNotFound) {
		t.Fatalf("foreign group: %v", err)
	}
	if count := client.User.Query().Where(user.RoleEQ(consts.UserRoleSubAccount)).CountX(ctx); count != 0 {
		t.Fatal("denied batch created users")
	}
	accounts, err := create(adminID, uuid.Nil, "one@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	account := accounts[0]
	if account.Role != consts.UserRoleSubAccount || account.Edges.Teams[0].ID != teamID {
		t.Fatal("account role/team changed")
	}
	if count := client.TeamGroupMember.Query().Where(teamgroupmember.UserIDEQ(account.ID)).CountX(ctx); count != 1 {
		t.Fatal("default group not assigned exactly once")
	}
	if _, err := create(account.ID, uuid.Nil, "ordinary-user@example.invalid"); !errors.Is(err, errcode.ErrForbidden) {
		t.Fatalf("ordinary member created user: %v", err)
	}
	if _, err := create(adminID, uuid.Nil, "two@example.invalid", "three@example.invalid"); !errors.Is(err, errcode.ErrTeamMemberLimitExceeded) {
		t.Fatalf("quota: %v", err)
	}
	if count := client.User.Query().Where(user.RoleEQ(consts.UserRoleSubAccount)).CountX(ctx); count != 1 {
		t.Fatal("quota rejection partially committed")
	}
	if _, err := create(adminID, uuid.Nil, "one@example.invalid"); !errors.Is(err, errcode.ErrUserAlreadyExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if value := client.User.GetX(ctx, account.ID).Password; value != "initial-hash" {
		t.Fatal("duplicate replaced password")
	}
	if _, err := r.Create(ctx, otherTeamID, otherAdminID, uuid.Nil, consts.UserRoleSubAccount, []LocalMemberInput{{Email: account.Email, PasswordHash: "takeover"}}, false); !errors.Is(err, errcode.ErrUserAlreadyExists) {
		t.Fatalf("cross team account reused: %v", err)
	}
	if exists := client.TeamMember.Query().Where(teammember.TeamIDEQ(otherTeamID), teammember.UserIDEQ(account.ID)).ExistX(ctx); exists {
		t.Fatal("cross team account was attached")
	}
	if _, err := create(adminID, uuid.Nil, "new@example.invalid", account.Email); !errors.Is(err, errcode.ErrUserAlreadyExists) {
		t.Fatalf("mixed duplicate batch: %v", err)
	}
	if client.User.Query().Where(user.EmailEQ("new@example.invalid")).ExistX(ctx) {
		t.Fatal("mixed batch partially committed")
	}
	client.User.DeleteOneID(account.ID).ExecX(ctx)
	if _, err := create(adminID, uuid.Nil, account.Email); !errors.Is(err, errcode.ErrTeamUserDeleted) {
		t.Fatalf("deleted account resurrected: %v", err)
	}
}

func TestLocalMembersRolesAndOIDCReuse(t *testing.T) {
	ctx := context.Background()
	client := newTeamRepoTestDB(t)
	r := &LocalMemberStore{db: client}
	teamID, adminID := localMemberFixture(t, client, 1)
	admin := client.User.GetX(ctx, adminID)
	accounts, err := r.Create(ctx, teamID, adminID, uuid.Nil, consts.UserRoleSubAccount, []LocalMemberInput{{Email: admin.Email, PasswordHash: "console-hash"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if accounts[0].ID == adminID || client.User.GetX(ctx, adminID).Password != "existing-admin-hash" {
		t.Fatal("console user changed enterprise login")
	}
	reused, err := r.Create(ctx, teamID, uuid.Nil, uuid.Nil, consts.UserRoleSubAccount, []LocalMemberInput{{Email: admin.Email}}, true)
	if err != nil || reused[0].ID != accounts[0].ID || reused[0].Password != "console-hash" {
		t.Fatalf("OIDC reuse/reset: %v", err)
	}
	client.User.UpdateOneID(accounts[0].ID).SetIsBlocked(true).ExecX(ctx)
	if _, err := r.Create(ctx, teamID, uuid.Nil, uuid.Nil, consts.UserRoleSubAccount, []LocalMemberInput{{Email: admin.Email}}, true); !errors.Is(err, errcode.ErrUserBlocked) {
		t.Fatalf("blocked OIDC member accepted: %v", err)
	}
	client.User.UpdateOneID(accounts[0].ID).SetIsBlocked(false).ExecX(ctx)
	if _, err := r.Create(ctx, teamID, uuid.Nil, uuid.Nil, consts.UserRoleSubAccount, []LocalMemberInput{{Email: "new-oidc@example.invalid"}}, true); !errors.Is(err, errcode.ErrTeamMemberLimitExceeded) {
		t.Fatalf("OIDC bypassed quota: %v", err)
	}
	otherTeam, _ := localMemberFixture(t, client, 5)
	if _, err := r.Create(ctx, otherTeam, uuid.Nil, uuid.Nil, consts.UserRoleSubAccount, []LocalMemberInput{{Email: admin.Email}}, true); !errors.Is(err, errcode.ErrUserAlreadyExists) {
		t.Fatalf("OIDC reused another team's account: %v", err)
	}
	admins, err := r.Create(ctx, teamID, adminID, uuid.Nil, consts.UserRoleEnterprise, []LocalMemberInput{{Email: "admin-two@example.invalid", Name: "second administrator", PasswordHash: "admin-two-hash"}}, false)
	if err != nil || admins[0].Role != consts.UserRoleEnterprise {
		t.Fatalf("admin creation: %v", err)
	}
	if !client.TeamMember.Query().Where(teammember.UserIDEQ(admins[0].ID), teammember.RoleEQ(consts.TeamMemberRoleAdmin)).ExistX(ctx) {
		t.Fatal("administrator role missing")
	}
}

func localMembersPostgres(t *testing.T) *db.Client {
	t.Helper()
	dsn := os.Getenv("RUNTIME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL RUNTIME_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := stdsql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "members_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); _ = admin.Close() })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	driver, err := entsql.Open(dialect.Postgres, u.String())
	if err != nil {
		t.Fatal(err)
	}
	client := db.NewClient(db.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	return client
}

func TestLocalMembersPostgresConcurrentQuotaAndEmail(t *testing.T) {
	ctx := context.Background()
	client := localMembersPostgres(t)
	r := &LocalMemberStore{db: client, postgres: true}
	teamID, adminID := localMemberFixture(t, client, 1)
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, email := range []string{"concurrent-one@example.invalid", "concurrent-two@example.invalid"} {
		wg.Add(1)
		go func(email string) {
			defer wg.Done()
			<-start
			_, err := r.Create(ctx, teamID, adminID, uuid.Nil, consts.UserRoleSubAccount, []LocalMemberInput{{Email: email, PasswordHash: "hash"}}, false)
			results <- err
		}(email)
	}
	close(start)
	wg.Wait()
	close(results)
	success, quota := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, errcode.ErrTeamMemberLimitExceeded) {
			quota++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || quota != 1 {
		t.Fatalf("quota successes=%d denials=%d", success, quota)
	}
	if count := client.TeamMember.Query().Where(teammember.TeamIDEQ(teamID), teammember.RoleEQ(consts.TeamMemberRoleUser)).CountX(ctx); count != 1 {
		t.Fatal("concurrent submissions exceeded quota")
	}
	teamA, adminA := localMemberFixture(t, client, 2)
	teamB, adminB := localMemberFixture(t, client, 2)
	start = make(chan struct{})
	results = make(chan error, 2)
	for _, pair := range [][2]uuid.UUID{{teamA, adminA}, {teamB, adminB}} {
		wg.Add(1)
		go func(pair [2]uuid.UUID) {
			defer wg.Done()
			<-start
			_, err := r.Create(ctx, pair[0], pair[1], uuid.Nil, consts.UserRoleSubAccount, []LocalMemberInput{{Email: "same-email@example.invalid", PasswordHash: "hash"}}, false)
			results <- err
		}(pair)
	}
	close(start)
	wg.Wait()
	close(results)
	success, duplicates := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, errcode.ErrUserAlreadyExists) {
			duplicates++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || duplicates != 1 || client.User.Query().Where(user.EmailEQ("same-email@example.invalid"), user.RoleEQ(consts.UserRoleSubAccount)).CountX(ctx) != 1 {
		t.Fatal("cross-team concurrent email created duplicate logins")
	}
}
