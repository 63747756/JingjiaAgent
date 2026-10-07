package repo

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"entgo.io/ent"
	"github.com/google/uuid"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/teamgroup"
	"github.com/63747756/jingjiaagent/backend/db/teamgroupmember"
	"github.com/63747756/jingjiaagent/backend/db/teamgroupmodel"
	"github.com/63747756/jingjiaagent/backend/db/teammember"
	"github.com/63747756/jingjiaagent/backend/db/user"
	"github.com/63747756/jingjiaagent/backend/db/useridentity"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/adldap"
)

// These tests begin after directory verification. LDAP, TLS and password checks
// are covered by the client tests; this fixture exercises the real transaction.
func adRepoFixture(t *testing.T, client *db.Client, limit int, postgres bool) (*TeamADRepo, *db.TeamADConfig, uuid.UUID) {
	t.Helper()
	teamID, adminID := localMemberFixture(t, client, limit)
	r := &TeamADRepo{db: client, postgres: postgres}
	cfg, err := r.Save(context.Background(), teamID, &domain.SaveTeamADConfigReq{
		Enabled: true, DisplayName: "测试域", URL: "ldaps://directory.example.invalid:636",
		BaseDN: "DC=example,DC=invalid", BindDN: "CN=reader,DC=example,DC=invalid",
		AllowedGroupDNs: []string{"CN=employees,DC=example,DC=invalid"},
	}, "opaque-test-ciphertext", 0)
	if err != nil {
		t.Fatal(err)
	}
	return r, cfg, adminID
}

func adGUID(label string) string {
	value := uuid.NewSHA1(uuid.NameSpaceOID, []byte(label))
	return base64.RawURLEncoding.EncodeToString(value[:])
}

func adProfile(account, department, path string) *adldap.Profile {
	return &adldap.Profile{
		GUID: adGUID("user:" + account), Username: account, DisplayName: "员工 " + account,
		Email:      account + "@example.invalid",
		Department: adldap.Department{GUID: adGUID("ou:" + department), DN: "OU=" + department + ",OU=公司,DC=example,DC=invalid", Path: path},
	}
}

func adDepartment(t *testing.T, client *db.Client, cfg *db.TeamADConfig, guid string) *db.TeamGroup {
	t.Helper()
	return client.TeamGroup.Query().Where(teamgroup.TeamIDEQ(cfg.TeamID), teamgroup.SourceEQ("ad_ou"),
		teamgroup.DirectoryIDEQ(cfg.DirectoryID), teamgroup.ExternalIDEQ(guid)).OnlyX(context.Background())
}

func adAssertBinding(t *testing.T, client *db.Client, groupID, userID uuid.UUID, source string) {
	t.Helper()
	relation := client.TeamGroupMember.Query().Where(teamgroupmember.GroupIDEQ(groupID), teamgroupmember.UserIDEQ(userID)).OnlyX(context.Background())
	if relation.Source != source {
		t.Fatalf("membership source = %q, want %q", relation.Source, source)
	}
}

func TestADIdentityRenameAndOUMovePreserveAccountAndResources(t *testing.T) {
	ctx := context.Background()
	client := newTeamRepoTestDB(t)
	r, cfg, adminID := adRepoFixture(t, client, 10, false)
	profile := adProfile("alice", "软件部", "公司／研发中心／软件部")
	// Neither a matching email nor a matching display name establishes identity.
	local := client.User.Create().SetID(uuid.New()).SetName(profile.DisplayName).SetEmail(profile.Email).
		SetRole(consts.UserRoleSubAccount).SetStatus(consts.UserStatusActive).SetPassword("local-password-hash").SaveX(ctx)
	client.TeamMember.Create().SetID(uuid.New()).SetTeamID(cfg.TeamID).SetUserID(local.ID).SetRole(consts.TeamMemberRoleUser).SaveX(ctx)
	manual := client.TeamGroup.Create().SetID(uuid.New()).SetTeamID(cfg.TeamID).SetName(profile.Department.Path).SaveX(ctx)
	account, err := r.Complete(ctx, cfg, profile)
	if err != nil {
		t.Fatal(err)
	}
	if account.ID == local.ID || account.AuthSource != "ad" || account.Password != "" || account.Role != consts.UserRoleSubAccount {
		t.Fatalf("AD enrollment reused or promoted a local user: %#v", account)
	}
	dept := adDepartment(t, client, cfg, profile.Department.GUID)
	if dept.ID == manual.ID || dept.Name != manual.Name {
		t.Fatal("same-name manual and AD groups were merged")
	}
	model := client.Model.Create().SetID(uuid.New()).SetUserID(adminID).SetProvider("test").
		SetAPIKey("fixture-only").SetBaseURL("https://model.example.invalid/v1").SetModel("fixture").SaveX(ctx)
	client.TeamGroupModel.Create().SetID(uuid.New()).SetGroupID(dept.ID).SetModelID(model.ID).SaveX(ctx)
	profile.Username = "alice.renamed"
	profile.DisplayName = "改名后的员工"
	profile.Email = ""
	profile.Department.DN = "OU=应用软件部,OU=工程中心,DC=example,DC=invalid"
	profile.Department.Path = "工程中心／应用软件部"
	updated, err := r.Complete(ctx, cfg, profile)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != account.ID || updated.LoginName != profile.Username || updated.Email != "" || updated.Name != profile.DisplayName {
		t.Fatalf("GUID identity was not preserved across rename: %#v", updated)
	}
	newDept := adDepartment(t, client, cfg, profile.Department.GUID)
	if newDept.ID != dept.ID || newDept.Name != profile.Department.Path || newDept.ExternalDn != profile.Department.DN || newDept.LastSyncedAt == nil {
		t.Fatal("OU rename/move replaced its stable group or failed to update its path")
	}
	if !client.TeamGroupModel.Query().Where(teamgroupmodel.GroupIDEQ(dept.ID), teamgroupmodel.ModelIDEQ(model.ID)).ExistX(ctx) {
		t.Fatal("OU rename removed resource authorization")
	}
	if client.TeamGroup.GetX(ctx, manual.ID).Name != manual.Name || client.User.GetX(ctx, local.ID).Password != "local-password-hash" {
		t.Fatal("AD synchronization modified a manual group or local account")
	}
	if got := client.UserIdentity.Query().Where(useridentity.PlatformEQ(consts.UserPlatformAD)).CountX(ctx); got != 1 {
		t.Fatalf("identity count = %d, want 1", got)
	}
	if got := client.TeamGroupMember.Query().Where(teamgroupmember.UserIDEQ(account.ID)).CountX(ctx); got != 2 {
		t.Fatalf("repeated login membership count = %d, want 2", got)
	}
}

func TestADTransferChangesOnlyCurrentDirectoryMembership(t *testing.T) {
	ctx := context.Background()
	client := newTeamRepoTestDB(t)
	r, cfg, _ := adRepoFixture(t, client, 10, false)
	aliceProfile := adProfile("alice", "software", "公司／研发／软件部")
	bobProfile := adProfile("bob", "software", "公司／研发／软件部")
	alice, err := r.Complete(ctx, cfg, aliceProfile)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := r.Complete(ctx, cfg, bobProfile)
	if err != nil {
		t.Fatal(err)
	}
	oldDept := adDepartment(t, client, cfg, aliceProfile.Department.GUID)
	manual := client.TeamGroup.Create().SetID(uuid.New()).SetTeamID(cfg.TeamID).SetName("专家组").SaveX(ctx)
	client.TeamGroupMember.Create().SetID(uuid.New()).SetGroupID(manual.ID).SetUserID(alice.ID).SaveX(ctx)
	otherDirectory := client.TeamGroup.Create().SetID(uuid.New()).SetTeamID(cfg.TeamID).SetName("其他目录分组").
		SetSource("ad_ou").SetDirectoryID(uuid.New()).SetExternalID(adGUID("foreign-ou")).SaveX(ctx)
	client.TeamGroupMember.Create().SetID(uuid.New()).SetGroupID(otherDirectory.ID).SetUserID(alice.ID).SetSource("ad_ou").SaveX(ctx)
	aliceProfile.Department = adProfile("alice", "testing", "公司／质量／软件部").Department
	if _, err := r.Complete(ctx, cfg, aliceProfile); err != nil {
		t.Fatal(err)
	}
	newDept := adDepartment(t, client, cfg, aliceProfile.Department.GUID)
	if newDept.ID == oldDept.ID {
		t.Fatal("different OU identities with equal leaf names were merged")
	}
	if client.TeamGroupMember.Query().Where(teamgroupmember.GroupIDEQ(oldDept.ID), teamgroupmember.UserIDEQ(alice.ID)).ExistX(ctx) {
		t.Fatal("transferred employee retained old automatic department relation")
	}
	adAssertBinding(t, client, oldDept.ID, bob.ID, "ad_ou")
	adAssertBinding(t, client, newDept.ID, alice.ID, "ad_ou")
	adAssertBinding(t, client, manual.ID, alice.ID, "manual")
	adAssertBinding(t, client, otherDirectory.ID, alice.ID, "ad_ou")
	defaultGroup := client.TeamGroup.Query().Where(teamgroup.TeamIDEQ(cfg.TeamID), teamgroup.NameEQ(defaultTeamGroupName), teamgroup.SourceEQ("manual")).OnlyX(ctx)
	adAssertBinding(t, client, defaultGroup.ID, alice.ID, "ad_default")
	adAssertBinding(t, client, defaultGroup.ID, bob.ID, "ad_default")
	if !client.TeamGroup.Query().Where(teamgroup.IDEQ(oldDept.ID)).ExistX(ctx) {
		t.Fatal("old department group was deleted")
	}
}

func TestADUnassignedAndEmptyEmailAreValid(t *testing.T) {
	ctx := context.Background()
	client := newTeamRepoTestDB(t)
	r, cfg, _ := adRepoFixture(t, client, 10, false)
	for _, name := range []string{"alice", "bob"} {
		profile := adProfile(name, "unused", "unused")
		profile.Email = ""
		profile.Department = adldap.Department{GUID: "unassigned", Path: "未分配部门"}
		account, err := r.Complete(ctx, cfg, profile)
		if err != nil || account.Email != "" {
			t.Fatalf("empty-email enrollment: %v", err)
		}
	}
	group := adDepartment(t, client, cfg, "unassigned")
	if group.ExternalDn != "" || group.OuPath != "未分配部门" || client.TeamGroupMember.Query().Where(teamgroupmember.GroupIDEQ(group.ID)).CountX(ctx) != 2 {
		t.Fatal("unassigned group was not shared within one directory")
	}
}

func TestADDefaultMembershipSurvivesDefaultGroupRename(t *testing.T) {
	ctx := context.Background()
	client := newTeamRepoTestDB(t)
	r, cfg, _ := adRepoFixture(t, client, 10, false)
	profile := adProfile("alice", "default-name", defaultTeamGroupName)
	account, err := r.Complete(ctx, cfg, profile)
	if err != nil {
		t.Fatal(err)
	}
	// An OU with this name must not be mistaken for the project's default group.
	dept := adDepartment(t, client, cfg, profile.Department.GUID)
	defaultGroup := client.TeamGroup.Query().Where(teamgroup.TeamIDEQ(cfg.TeamID), teamgroup.SourceEQ("manual"), teamgroup.NameEQ(defaultTeamGroupName)).OnlyX(ctx)
	if dept.ID == defaultGroup.ID {
		t.Fatal("department claimed the default group")
	}
	groups := &TeamGroupUserRepo{db: client}
	if _, err := groups.Update(ctx, &domain.UpdateTeamGroupReq{GroupID: defaultGroup.ID, Name: "全体员工"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Complete(ctx, cfg, profile); err != nil {
		t.Fatal(err)
	}
	adAssertBinding(t, client, defaultGroup.ID, account.ID, "ad_default")
	if client.TeamGroup.Query().Where(teamgroup.TeamIDEQ(cfg.TeamID), teamgroup.SourceEQ("manual")).CountX(ctx) != 1 || client.TeamGroupMember.Query().Where(teamgroupmember.UserIDEQ(account.ID)).CountX(ctx) != 2 {
		t.Fatal("renaming default group created another default group or membership")
	}
}

func TestADQuotaAndTransactionFailureDoNotPartiallyEnroll(t *testing.T) {
	t.Run("quota", func(t *testing.T) {
		ctx := context.Background()
		client := newTeamRepoTestDB(t)
		r, cfg, _ := adRepoFixture(t, client, 1, false)
		profile := adProfile("alice", "software", "公司／软件部")
		first, err := r.Complete(ctx, cfg, profile)
		if err != nil {
			t.Fatal(err)
		}
		if repeated, err := r.Complete(ctx, cfg, profile); err != nil || repeated.ID != first.ID {
			t.Fatalf("existing employee rejected at quota: %v", err)
		}
		if _, err := r.Complete(ctx, cfg, adProfile("bob", "new-ou", "公司／新部门")); !errors.Is(err, errcode.ErrTeamMemberLimitExceeded) {
			t.Fatalf("quota result = %v", err)
		}
		if client.User.Query().Where(user.AuthSourceEQ("ad")).CountX(ctx) != 1 || client.UserIdentity.Query().Where(useridentity.PlatformEQ(consts.UserPlatformAD)).CountX(ctx) != 1 || client.TeamGroup.Query().Where(teamgroup.SourceEQ("ad_ou")).CountX(ctx) != 1 {
			t.Fatal("quota rejection partially created identity, user or department")
		}
	})
	t.Run("late membership failure", func(t *testing.T) {
		ctx := context.Background()
		client := newTeamRepoTestDB(t)
		r, cfg, _ := adRepoFixture(t, client, 10, false)
		injected := errors.New("injected department membership failure")
		client.TeamGroupMember.Use(func(next ent.Mutator) ent.Mutator {
			return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
				if mutation, ok := m.(*db.TeamGroupMemberMutation); ok {
					if source, exists := mutation.Source(); exists && source == "ad_ou" {
						return nil, injected
					}
				}
				return next.Mutate(ctx, m)
			})
		})
		if _, err := r.Complete(ctx, cfg, adProfile("alice", "software", "公司／软件部")); !errors.Is(err, injected) {
			t.Fatalf("injected failure result = %v", err)
		}
		if client.User.Query().Where(user.AuthSourceEQ("ad")).ExistX(ctx) || client.UserIdentity.Query().Where(useridentity.PlatformEQ(consts.UserPlatformAD)).ExistX(ctx) || client.TeamGroup.Query().ExistX(ctx) || client.TeamGroupMember.Query().ExistX(ctx) {
			t.Fatal("transaction failure left an account, identity, group or membership")
		}
	})
	t.Run("failed transfer keeps committed membership", func(t *testing.T) {
		ctx := context.Background()
		client := newTeamRepoTestDB(t)
		r, cfg, _ := adRepoFixture(t, client, 10, false)
		profile := adProfile("alice", "software", "公司／软件部")
		account, err := r.Complete(ctx, cfg, profile)
		if err != nil {
			t.Fatal(err)
		}
		previous := adDepartment(t, client, cfg, profile.Department.GUID)
		injected := errors.New("injected transfer failure")
		client.TeamGroupMember.Use(func(next ent.Mutator) ent.Mutator {
			return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
				if mutation, ok := m.(*db.TeamGroupMemberMutation); ok && mutation.Op().Is(ent.OpCreate) {
					if source, exists := mutation.Source(); exists && source == "ad_ou" {
						return nil, injected
					}
				}
				return next.Mutate(ctx, m)
			})
		})
		profile.DisplayName = "不应提交的名称"
		profile.Department = adProfile("alice", "testing", "公司／测试部").Department
		if _, err := r.Complete(ctx, cfg, profile); !errors.Is(err, injected) {
			t.Fatalf("failed transfer result = %v", err)
		}
		adAssertBinding(t, client, previous.ID, account.ID, "ad_ou")
		if client.User.GetX(ctx, account.ID).Name != account.Name || client.TeamGroup.Query().Where(teamgroup.ExternalIDEQ(profile.Department.GUID)).ExistX(ctx) {
			t.Fatal("failed transfer changed profile or created a new department")
		}
	})
}

func TestADDisabledStaleAndTombstonedAccountsFailClosed(t *testing.T) {
	for _, state := range []string{"blocked", "inactive", "deleted user", "deleted identity", "wrong role", "missing team", "config disabled", "stale config"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			client := newTeamRepoTestDB(t)
			r, cfg, _ := adRepoFixture(t, client, 10, false)
			profile := adProfile("alice", "software", "公司／软件部")
			account, err := r.Complete(ctx, cfg, profile)
			if err != nil {
				t.Fatal(err)
			}
			switch state {
			case "blocked":
				client.User.UpdateOneID(account.ID).SetIsBlocked(true).ExecX(ctx)
			case "inactive":
				client.User.UpdateOneID(account.ID).SetStatus("inactive").ExecX(ctx)
			case "deleted user":
				client.User.DeleteOneID(account.ID).ExecX(ctx)
			case "deleted identity":
				identity := client.UserIdentity.Query().Where(useridentity.UserIDEQ(account.ID)).OnlyX(ctx)
				client.UserIdentity.DeleteOneID(identity.ID).ExecX(ctx)
			case "wrong role":
				client.User.UpdateOneID(account.ID).SetRole(consts.UserRoleEnterprise).ExecX(ctx)
			case "missing team":
				client.TeamMember.Delete().Where(teammember.UserIDEQ(account.ID)).ExecX(ctx)
			case "config disabled":
				client.TeamADConfig.UpdateOneID(cfg.ID).SetEnabled(false).ExecX(ctx)
			case "stale config":
				client.TeamADConfig.UpdateOneID(cfg.ID).AddRevision(1).ExecX(ctx)
			}
			profile.Username = "unexpected-update"
			profile.Department = adProfile("alice", "new", "公司／新部门").Department
			if _, err := r.Complete(ctx, cfg, profile); err == nil {
				t.Fatal("invalid account/configuration accepted")
			}
			if client.TeamGroup.Query().Where(teamgroup.ExternalIDEQ(profile.Department.GUID)).ExistX(ctx) {
				t.Fatal("rejected login changed departments")
			}
			if client.User.Query().Where(user.LoginNameEQ(profile.Username)).ExistX(ctx) {
				t.Fatal("rejected login updated account profile")
			}
		})
	}
}

func TestADManagedGroupsAndPasswordCannotBeChangedManually(t *testing.T) {
	ctx := context.Background()
	client := newTeamRepoTestDB(t)
	r, cfg, adminID := adRepoFixture(t, client, 10, false)
	profile := adProfile("alice", "software", "公司／软件部")
	account, err := r.Complete(ctx, cfg, profile)
	if err != nil {
		t.Fatal(err)
	}
	dept := adDepartment(t, client, cfg, profile.Department.GUID)
	defaultGroup := client.TeamGroup.Query().Where(teamgroup.NameEQ(defaultTeamGroupName), teamgroup.TeamIDEQ(cfg.TeamID), teamgroup.SourceEQ("manual")).OnlyX(ctx)
	groups := &TeamGroupUserRepo{db: client, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	operations := map[string]func() error{
		"rename department": func() error {
			_, err := groups.Update(ctx, &domain.UpdateTeamGroupReq{GroupID: dept.ID, Name: "篡改"})
			return err
		},
		"delete department":               func() error { return groups.Delete(ctx, cfg.TeamID, dept.ID) },
		"replace department members":      func() error { _, err := groups.ModifyGroupUsers(ctx, dept.ID, []uuid.UUID{}); return err },
		"remove department member":        func() error { return groups.DeleteGroupUser(ctx, dept.ID, account.ID) },
		"remove default automatic member": func() error { return groups.DeleteGroupUser(ctx, defaultGroup.ID, account.ID) },
		"delete automatic default group":  func() error { return groups.Delete(ctx, cfg.TeamID, defaultGroup.ID) },
		"reset AD password":               func() error { return groups.ResetPassword(ctx, account.ID, "local-password") },
		"set AD password":                 func() error { return groups.ChangePassword(ctx, account.ID, "", "local-password") },
	}
	for name, operation := range operations {
		err := operation()
		if !errors.Is(err, errcode.ErrADManaged) && !errors.Is(err, errcode.ErrADLocalPasswordDenied) {
			t.Errorf("%s result = %v", name, err)
		}
	}
	// Replacing manual selections must retain the automatic membership.
	client.TeamGroupMember.Create().SetID(uuid.New()).SetGroupID(defaultGroup.ID).SetUserID(adminID).SaveX(ctx)
	if _, err := groups.ModifyGroupUsers(ctx, defaultGroup.ID, []uuid.UUID{}); err != nil {
		t.Fatal(err)
	}
	adAssertBinding(t, client, defaultGroup.ID, account.ID, "ad_default")
	if client.TeamGroupMember.Query().Where(teamgroupmember.GroupIDEQ(defaultGroup.ID), teamgroupmember.UserIDEQ(adminID)).ExistX(ctx) {
		t.Fatal("removed manual selection remained")
	}
	if client.User.GetX(ctx, account.ID).Password != "" {
		t.Fatal("AD password was set by a local password endpoint")
	}
	store := &LocalMemberStore{db: client}
	if _, err := store.Create(ctx, cfg.TeamID, adminID, dept.ID, consts.UserRoleSubAccount, []LocalMemberInput{{Email: "injected@example.invalid", PasswordHash: "fake"}}, false); !errors.Is(err, errcode.ErrADManaged) {
		t.Fatalf("member creation bypassed managed department guard: %v", err)
	}
}

func TestADPostgresConcurrentEnrollmentAndQuota(t *testing.T) {
	for _, scenario := range []string{"same identity", "different employees at quota"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			client := localMembersPostgres(t)
			r, cfg, _ := adRepoFixture(t, client, 3, true)
			const attempts = 12
			start := make(chan struct{})
			results := make(chan error, attempts)
			ids := make(chan uuid.UUID, attempts)
			var workers sync.WaitGroup
			for i := 0; i < attempts; i++ {
				workers.Add(1)
				go func(i int) {
					defer workers.Done()
					name := "alice"
					if scenario == "different employees at quota" {
						name = uuid.NewSHA1(uuid.NameSpaceOID, []byte{byte(i)}).String()
					}
					<-start
					account, err := r.Complete(ctx, cfg, adProfile(name, "software", "公司／软件部"))
					results <- err
					if err == nil {
						ids <- account.ID
					}
				}(i)
			}
			close(start)
			workers.Wait()
			close(results)
			close(ids)
			successes, distinct := 0, map[uuid.UUID]bool{}
			for err := range results {
				if err == nil {
					successes++
				} else if scenario != "different employees at quota" || !errors.Is(err, errcode.ErrTeamMemberLimitExceeded) {
					t.Errorf("concurrent enrollment: %v", err)
				}
			}
			for id := range ids {
				distinct[id] = true
			}
			wantSuccesses, wantAccounts := attempts, 1
			if scenario == "different employees at quota" {
				wantSuccesses, wantAccounts = 3, 3
			}
			if successes != wantSuccesses || len(distinct) != wantAccounts || client.User.Query().Where(user.AuthSourceEQ("ad")).CountX(ctx) != wantAccounts || client.UserIdentity.Query().Where(useridentity.PlatformEQ(consts.UserPlatformAD)).CountX(ctx) != wantAccounts {
				t.Fatalf("successes/accounts = %d/%d, want %d/%d", successes, len(distinct), wantSuccesses, wantAccounts)
			}
			if client.TeamGroup.Query().Where(teamgroup.SourceEQ("ad_ou")).CountX(ctx) != 1 || client.TeamGroupMember.Query().CountX(ctx) != wantAccounts*2 {
				t.Fatal("concurrent enrollment duplicated departments or relations")
			}
		})
	}
}

func TestADPostgresConcurrentConfigurationAndOIDCConflict(t *testing.T) {
	ctx := context.Background()
	client := localMembersPostgres(t)
	r, cfg, _ := adRepoFixture(t, client, 10, true)
	// Disable first; each contender must lock the same team before enabling.
	draft := &domain.SaveTeamADConfigReq{Enabled: false, DisplayName: "测试域", URL: cfg.URL, BaseDN: cfg.BaseDn, BindDN: cfg.BindDn, AllowedGroupDNs: cfg.AllowedGroupDNS}
	cfg, err := r.Save(ctx, cfg.TeamID, draft, cfg.BindPasswordCiphertext, cfg.Revision)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		request := *draft
		request.Enabled = true
		_, err := r.Save(ctx, cfg.TeamID, &request, cfg.BindPasswordCiphertext, cfg.Revision)
		results <- err
	}()
	go func() {
		<-start
		_, err := (&TeamOIDCRepo{db: client, postgres: true}).UpsertConfig(ctx, cfg.TeamID, &domain.SaveTeamOIDCConfigReq{
			Enabled: true, DisplayName: "OIDC", Issuer: "https://oidc.example.invalid", ClientID: "test",
		})
		results <- err
	}()
	close(start)
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("exactly one mode should enable: %v; %v", first, second)
	}
	if first != nil && !errors.Is(first, errcode.ErrADModeConflict) || second != nil && !errors.Is(second, errcode.ErrADModeConflict) {
		t.Fatalf("mode conflict must be explicit: %v; %v", first, second)
	}
	current := client.TeamADConfig.GetX(ctx, cfg.ID)
	oidcConfig, err := (&TeamOIDCRepo{db: client, postgres: true}).GetConfig(ctx, cfg.TeamID)
	if err != nil && !db.IsNotFound(err) {
		t.Fatal(err)
	}
	if current.Enabled && oidcConfig != nil && oidcConfig.Enabled {
		t.Fatal("both AD and OIDC enabled")
	}
}

func TestADPostgresConcurrentConfigurationUsesOptimisticRevision(t *testing.T) {
	ctx := context.Background()
	client := localMembersPostgres(t)
	r, cfg, _ := adRepoFixture(t, client, 10, true)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, displayName := range []string{"配置 A", "配置 B"} {
		go func(name string) {
			<-start
			_, err := r.Save(ctx, cfg.TeamID, &domain.SaveTeamADConfigReq{
				Enabled: true, DisplayName: name, URL: cfg.URL, BaseDN: cfg.BaseDn,
				BindDN: cfg.BindDn, AllowedGroupDNs: cfg.AllowedGroupDNS,
			}, cfg.BindPasswordCiphertext, cfg.Revision)
			results <- err
		}(displayName)
	}
	close(start)
	first, second := <-results, <-results
	if (first == nil) == (second == nil) || first != nil && !errors.Is(first, errcode.ErrADConfigConflict) || second != nil && !errors.Is(second, errcode.ErrADConfigConflict) {
		t.Fatalf("stale configuration should conflict: %v; %v", first, second)
	}
	current := client.TeamADConfig.GetX(ctx, cfg.ID)
	if current.Revision != cfg.Revision+1 || current.DirectoryID != cfg.DirectoryID {
		t.Fatal("concurrent save changed the directory identity or applied both revisions")
	}
}

func TestADPostgresMigrationAllowsSharedMailWithoutWeakeningLocalIdentity(t *testing.T) {
	ctx := context.Background()
	client := localMembersPostgres(t)
	// Ent's test schema does not include the old SQL-only email index. Restore
	// the relevant pre-AD shape before running the actual appended migration.
	baseline := `
DROP TABLE team_ad_configs;
ALTER TABLE users DROP COLUMN auth_source, DROP COLUMN login_name;
ALTER TABLE team_groups DROP COLUMN source, DROP COLUMN directory_id,
 DROP COLUMN external_id, DROP COLUMN external_dn, DROP COLUMN ou_path, DROP COLUMN last_synced_at;
ALTER TABLE team_group_members DROP COLUMN source;
CREATE UNIQUE INDEX unique_idx_users_email_role ON users(lower(email),role)
 WHERE deleted_at IS NULL AND email IS NOT NULL AND email <> '';
`
	if _, err := client.ExecContext(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../../migration/000033_ad_login.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	r, cfg, _ := adRepoFixture(t, client, 10, true)
	sharedMail := "shared@example.invalid"
	local := client.User.Create().SetID(uuid.New()).SetName("Local employee").SetEmail(sharedMail).
		SetRole(consts.UserRoleSubAccount).SetStatus(consts.UserStatusActive).SetPassword("local-hash").SaveX(ctx)
	client.TeamMember.Create().SetID(uuid.New()).SetTeamID(cfg.TeamID).SetUserID(local.ID).SetRole(consts.TeamMemberRoleUser).SaveX(ctx)
	seen := map[uuid.UUID]bool{local.ID: true}
	for _, name := range []string{"alice", "bob"} {
		profile := adProfile(name, "software", strings.Repeat("组织层级／", 70)+"软件部")
		profile.Email = sharedMail
		account, err := r.Complete(ctx, cfg, profile)
		if err != nil {
			t.Fatalf("distinct AD GUID with shared mail failed: %v", err)
		}
		if seen[account.ID] {
			t.Fatal("mail was used to merge independent account identities")
		}
		seen[account.ID] = true
		if adDepartment(t, client, cfg, profile.Department.GUID).Name != profile.Department.Path {
			t.Fatal("full OU display path was truncated")
		}
	}
	if _, err := client.User.Create().SetID(uuid.New()).SetName("Duplicate local employee").SetEmail("SHARED@example.invalid").
		SetRole(consts.UserRoleSubAccount).SetStatus(consts.UserStatusActive).Save(ctx); !db.IsConstraintError(err) {
		t.Fatalf("local case-insensitive email uniqueness was weakened: %v", err)
	}
	if client.User.GetX(ctx, local.ID).Password != "local-hash" || len(seen) != 3 {
		t.Fatal("migration or AD enrollment modified the local account")
	}
}
