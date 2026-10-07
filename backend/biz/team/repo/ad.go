package repo

import (
	"context"
	"entgo.io/ent/dialect/sql"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/team"
	"github.com/63747756/jingjiaagent/backend/db/teamadconfig"
	"github.com/63747756/jingjiaagent/backend/db/teamgroup"
	"github.com/63747756/jingjiaagent/backend/db/teamgroupmember"
	"github.com/63747756/jingjiaagent/backend/db/teammember"
	"github.com/63747756/jingjiaagent/backend/db/teamoidcconfig"
	"github.com/63747756/jingjiaagent/backend/db/user"
	"github.com/63747756/jingjiaagent/backend/db/useridentity"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/adldap"
	"github.com/63747756/jingjiaagent/backend/pkg/entx"
	"github.com/google/uuid"
	"github.com/samber/do"
	"time"
)

type TeamADRepo struct {
	db       *db.Client
	postgres bool
}

func NewTeamADRepo(i *do.Injector) (*TeamADRepo, error) {
	return &TeamADRepo{db: do.MustInvoke[*db.Client](i), postgres: true}, nil
}

func (r *TeamADRepo) DefaultTeamID(ctx context.Context) (uuid.UUID, error) {
	t, err := r.db.Team.Query().Order(team.ByCreatedAt(), team.ByID()).First(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	return t.ID, nil
}
func (r *TeamADRepo) Get(ctx context.Context, teamID uuid.UUID) (*db.TeamADConfig, error) {
	return r.db.TeamADConfig.Query().Where(teamadconfig.TeamIDEQ(teamID)).Only(ctx)
}
func (r *TeamADRepo) Enabled(ctx context.Context) (*db.TeamADConfig, error) {
	return r.db.TeamADConfig.Query().Where(teamadconfig.EnabledEQ(true)).Only(ctx)
}

// The team lock serializes enrollment, configuration and OIDC mode changes.
func (r *TeamADRepo) Save(ctx context.Context, teamID uuid.UUID, req *domain.SaveTeamADConfigReq, ciphertext string, expectedRevision int) (*db.TeamADConfig, error) {
	var result *db.TeamADConfig
	err := entx.WithTx2(ctx, r.db, func(tx *db.Tx) error {
		if err := lockAuthenticationMode(ctx, tx, r.postgres); err != nil {
			return err
		}
		q := tx.Team.Query().Where(team.IDEQ(teamID))
		if r.postgres {
			q.ForUpdate()
		}
		if _, err := q.Only(ctx); err != nil {
			return err
		}
		current, err := tx.TeamADConfig.Query().Where(teamadconfig.TeamIDEQ(teamID)).Only(ctx)
		if err != nil && !db.IsNotFound(err) {
			return err
		}
		if current == nil && expectedRevision != 0 || current != nil && current.Revision != expectedRevision {
			return errcode.ErrADConfigConflict
		}
		if req.Enabled {
			active, err := tx.TeamOIDCConfig.Query().Where(teamoidcconfig.EnabledEQ(true)).Exist(ctx)
			if err != nil {
				return err
			}
			if active {
				return errcode.ErrADModeConflict
			}
		}
		if current == nil {
			result, err = tx.TeamADConfig.Create().SetID(uuid.New()).SetDirectoryID(uuid.New()).SetTeamID(teamID).
				SetEnabled(req.Enabled).SetDisplayName(req.DisplayName).SetURL(req.URL).SetBaseDn(req.BaseDN).SetBindDn(req.BindDN).
				SetBindPasswordCiphertext(ciphertext).SetCaPem(req.CAPEM).SetAllowedGroupDNS(req.AllowedGroupDNs).Save(ctx)
		} else {
			result, err = tx.TeamADConfig.UpdateOneID(current.ID).SetEnabled(req.Enabled).SetDisplayName(req.DisplayName).
				SetURL(req.URL).SetBaseDn(req.BaseDN).SetBindDn(req.BindDN).SetBindPasswordCiphertext(ciphertext).
				SetCaPem(req.CAPEM).SetAllowedGroupDNS(req.AllowedGroupDNs).AddRevision(1).Save(ctx)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Complete performs no LDAP calls: a fully verified profile is committed atomically.
func (r *TeamADRepo) Complete(ctx context.Context, cfg *db.TeamADConfig, p *adldap.Profile) (*db.User, error) {
	if cfg == nil || p == nil || p.GUID == "" || p.Username == "" || p.Department.GUID == "" || p.Department.Path == "" {
		return nil, errcode.ErrADInvalidCredentials
	}
	var account *db.User
	err := entx.WithTx2(ctx, r.db, func(tx *db.Tx) error {
		q := tx.Team.Query().Where(team.IDEQ(cfg.TeamID))
		if r.postgres {
			q.ForUpdate()
		}
		target, err := q.Only(ctx)
		if err != nil {
			return err
		}
		current, err := tx.TeamADConfig.Get(ctx, cfg.ID)
		if err != nil {
			return err
		}
		if !current.Enabled || current.Revision != cfg.Revision || current.DirectoryID != cfg.DirectoryID {
			return errcode.ErrADConfigConflict
		}
		identityID := cfg.DirectoryID.String() + "#" + p.GUID
		identity, err := tx.UserIdentity.Query().Where(useridentity.PlatformEQ(consts.UserPlatformAD), useridentity.IdentityIDEQ(identityID)).Only(entx.SkipSoftDelete(ctx))
		if err != nil && !db.IsNotFound(err) {
			return err
		}
		if identity != nil {
			if !identity.DeletedAt.IsZero() {
				return errcode.ErrADInvalidCredentials
			}
			account, err = tx.User.Get(entx.SkipSoftDelete(ctx), identity.UserID)
			if err != nil {
				return err
			}
			if !account.DeletedAt.IsZero() || account.IsBlocked || account.Status != consts.UserStatusActive || account.Role != consts.UserRoleSubAccount || account.AuthSource != "ad" {
				return errcode.ErrADInvalidCredentials
			}
			exists, err := tx.TeamMember.Query().Where(teammember.TeamIDEQ(cfg.TeamID), teammember.UserIDEQ(account.ID), teammember.RoleEQ(consts.TeamMemberRoleUser)).Exist(ctx)
			if err != nil {
				return err
			}
			if !exists {
				return errcode.ErrADInvalidCredentials
			}
			account, err = tx.User.UpdateOneID(account.ID).SetName(p.DisplayName).SetLoginName(p.Username).SetEmail(p.Email).Save(ctx)
			if err != nil {
				return err
			}
			if err := tx.UserIdentity.UpdateOneID(identity.ID).SetUsername(p.Username).SetEmail(p.Email).Exec(ctx); err != nil {
				return err
			}
		} else {
			count, err := tx.TeamMember.Query().Where(teammember.TeamIDEQ(cfg.TeamID), teammember.RoleEQ(consts.TeamMemberRoleUser), teammember.HasUserWith(user.DeletedAtIsNil())).Count(ctx)
			if err != nil {
				return err
			}
			if count >= target.MemberLimit {
				return errcode.ErrTeamMemberLimitExceeded
			}
			account, err = tx.User.Create().SetID(uuid.New()).SetName(p.DisplayName).SetLoginName(p.Username).SetEmail(p.Email).SetAuthSource("ad").SetRole(consts.UserRoleSubAccount).SetStatus(consts.UserStatusActive).Save(ctx)
			if err != nil {
				return err
			}
			if err := tx.TeamMember.Create().SetID(uuid.New()).SetTeamID(cfg.TeamID).SetUserID(account.ID).SetRole(consts.TeamMemberRoleUser).Exec(ctx); err != nil {
				return err
			}
			if err := tx.UserIdentity.Create().SetID(uuid.New()).SetUserID(account.ID).SetPlatform(consts.UserPlatformAD).SetIdentityID(identityID).SetUsername(p.Username).SetEmail(p.Email).Exec(ctx); err != nil {
				return err
			}
		}
		dept, err := tx.TeamGroup.Query().Where(teamgroup.TeamIDEQ(cfg.TeamID), teamgroup.SourceEQ("ad_ou"), teamgroup.DirectoryIDEQ(cfg.DirectoryID), teamgroup.ExternalIDEQ(p.Department.GUID)).Only(ctx)
		if err != nil && !db.IsNotFound(err) {
			return err
		}
		now := time.Now()
		if dept == nil {
			dept, err = tx.TeamGroup.Create().SetID(uuid.New()).SetTeamID(cfg.TeamID).SetSource("ad_ou").SetDirectoryID(cfg.DirectoryID).SetExternalID(p.Department.GUID).SetName(p.Department.Path).SetOuPath(p.Department.Path).SetExternalDn(p.Department.DN).SetLastSyncedAt(now).Save(ctx)
		} else {
			dept, err = tx.TeamGroup.UpdateOneID(dept.ID).SetName(p.Department.Path).SetOuPath(p.Department.Path).SetExternalDn(p.Department.DN).SetLastSyncedAt(now).Save(ctx)
		}
		if err != nil {
			return err
		}
		defaultGroup, err := ensureDefaultTeamGroupTx(ctx, tx, cfg.TeamID)
		if err != nil {
			return err
		}
		if _, err := tx.TeamGroupMember.Delete().Where(teamgroupmember.UserIDEQ(account.ID), teamgroupmember.SourceEQ("ad_ou"), teamgroupmember.GroupIDNEQ(dept.ID), teamgroupmember.HasGroupWith(teamgroup.TeamIDEQ(cfg.TeamID), teamgroup.DirectoryIDEQ(cfg.DirectoryID), teamgroup.SourceEQ("ad_ou"))).Exec(ctx); err != nil {
			return err
		}
		for _, binding := range []struct {
			id     uuid.UUID
			source string
		}{{defaultGroup.ID, "ad_default"}, {dept.ID, "ad_ou"}} {
			existing, err := tx.TeamGroupMember.Query().Where(teamgroupmember.GroupIDEQ(binding.id), teamgroupmember.UserIDEQ(account.ID)).Only(ctx)
			if err != nil && !db.IsNotFound(err) {
				return err
			}
			if existing == nil {
				err = tx.TeamGroupMember.Create().SetID(uuid.New()).SetGroupID(binding.id).SetUserID(account.ID).SetSource(binding.source).Exec(ctx)
			} else {
				err = tx.TeamGroupMember.UpdateOneID(existing.ID).SetSource(binding.source).Exec(ctx)
			}
			if err != nil {
				return err
			}
		}
		account.Edges.Teams = []*db.Team{target}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return account, nil
}

func (r *TeamADRepo) IsAdmin(ctx context.Context, teamID, userID uuid.UUID) (bool, error) {
	return r.db.TeamMember.Query().Where(teammember.TeamIDEQ(teamID), teammember.UserIDEQ(userID), teammember.RoleEQ(consts.TeamMemberRoleAdmin), teammember.HasUserWith(user.DeletedAtIsNil(), user.IsBlockedEQ(false), user.StatusEQ(consts.UserStatusActive), user.RoleEQ(consts.UserRoleEnterprise))).Exist(ctx)
}
func lockAuthenticationMode(ctx context.Context, tx *db.Tx, postgres bool) error {
	if postgres {
		_, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(1064001)")
		return err
	}
	return nil
}

// LockModeChange is shared by OIDC configuration so both modes cannot be enabled.
func lockADModeChange(ctx context.Context, tx *db.Tx, teamID uuid.UUID, enabled bool, postgres bool) error {
	if err := lockAuthenticationMode(ctx, tx, postgres); err != nil {
		return err
	}
	q := tx.Team.Query().Where(team.IDEQ(teamID))
	if postgres {
		q.Modify(func(s *sql.Selector) { s.ForUpdate() })
	}
	if _, err := q.Only(ctx); err != nil {
		return err
	}
	if enabled {
		active, err := tx.TeamADConfig.Query().Where(teamadconfig.EnabledEQ(true)).Exist(ctx)
		if err != nil {
			return err
		}
		if active {
			return errcode.ErrADModeConflict
		}
	}
	return nil
}
