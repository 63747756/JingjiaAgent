package repo

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/samber/do"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/team"
	"github.com/63747756/jingjiaagent/backend/db/teamgroup"
	"github.com/63747756/jingjiaagent/backend/db/teammember"
	"github.com/63747756/jingjiaagent/backend/db/user"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/entx"
)

// LocalMemberStore supplies the missing standalone implementation. Enterprise
// administrators and console subaccounts retain the existing separate roles.
type LocalMemberStore struct {
	db       *db.Client
	postgres bool
}

type LocalMemberInput struct {
	Email, Name, PasswordHash string
}

func NewLocalMemberStore(i *do.Injector) (*LocalMemberStore, error) {
	return &LocalMemberStore{db: do.MustInvoke[*db.Client](i), postgres: true}, nil
}

// Create commits the whole batch, group membership and quota decision together.
// OIDC may reuse an existing member of this team, but never another team's login.
// Interactive duplicate requests fail without returning or replacing passwords.
func (r *LocalMemberStore) Create(ctx context.Context, teamID, actorID, groupID uuid.UUID, role consts.UserRole, inputs []LocalMemberInput, oidc bool) ([]*db.User, error) {
	if teamID == uuid.Nil || len(inputs) == 0 || (role != consts.UserRoleSubAccount && role != consts.UserRoleEnterprise) || (oidc && role != consts.UserRoleSubAccount) {
		return nil, errcode.ErrForbidden
	}
	seen := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		if input.Email == "" || input.Email != strings.ToLower(strings.TrimSpace(input.Email)) || seen[input.Email] {
			return nil, errcode.ErrUserAlreadyExists
		}
		seen[input.Email] = true
	}
	var created []*db.User
	err := entx.WithTx2(ctx, r.db, func(tx *db.Tx) error {
		if r.postgres {
			// Role/email has no unique index in the baseline. Lock names in a
			// stable order before the team row, also across different teams.
			keys := make([]string, 0, len(inputs))
			for _, input := range inputs {
				keys = append(keys, "jingjiaagent:member:"+string(role)+":"+input.Email)
			}
			sort.Strings(keys)
			for _, key := range keys {
				hash := sha256.Sum256([]byte(key))
				if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(binary.BigEndian.Uint64(hash[:8]))); err != nil {
					return err
				}
			}
		}
		query := tx.Team.Query().Where(team.IDEQ(teamID))
		if r.postgres {
			query.ForUpdate()
		}
		target, err := query.Only(ctx)
		if err != nil {
			if db.IsNotFound(err) {
				return errcode.ErrNotFound
			}
			return err
		}
		if !oidc {
			allowed, err := tx.TeamMember.Query().Where(
				teammember.TeamIDEQ(teamID), teammember.UserIDEQ(actorID), teammember.RoleEQ(consts.TeamMemberRoleAdmin),
				teammember.HasUserWith(user.DeletedAtIsNil(), user.IsBlockedEQ(false), user.StatusEQ(consts.UserStatusActive)),
			).Exist(ctx)
			if err != nil {
				return err
			}
			if !allowed {
				return errcode.ErrForbidden
			}
		}
		var group *db.TeamGroup
		if role == consts.UserRoleSubAccount {
			if groupID == uuid.Nil {
				group, err = ensureDefaultTeamGroupTx(ctx, tx, teamID)
			} else {
				group, err = tx.TeamGroup.Query().Where(teamgroup.IDEQ(groupID), teamgroup.TeamIDEQ(teamID)).Only(ctx)
			}
			if err != nil {
				if db.IsNotFound(err) {
					return errcode.ErrNotFound
				}
				return err
			}
		}
		if group != nil && group.Source == "ad_ou" {
			return errcode.ErrADManaged
		}
		pending := make([]LocalMemberInput, 0, len(inputs))
		for _, input := range inputs {
			accountQuery := tx.User.Query().Where(user.EmailEqualFold(input.Email))
			if role == consts.UserRoleSubAccount {
				// The console login accepts every non-enterprise role. Do not
				// introduce an ambiguous login alongside an individual/admin.
				accountQuery.Where(user.RoleNEQ(consts.UserRoleEnterprise))
			} else {
				accountQuery.Where(user.RoleEQ(role))
			}
			existing, err := accountQuery.All(entx.SkipSoftDelete(ctx))
			if err != nil {
				return err
			}
			if len(existing) > 0 {
				if len(existing) != 1 {
					return errcode.ErrUserAlreadyExists
				}
				account := existing[0]
				if !account.DeletedAt.IsZero() {
					return errcode.ErrTeamUserDeleted
				}
				if account.IsBlocked || account.Status != consts.UserStatusActive {
					return errcode.ErrUserBlocked
				}
				if !oidc {
					return errcode.ErrUserAlreadyExists
				}
				if account.AuthSource == "ad" {
					return errcode.ErrADLocalPasswordDenied
				}
				if account.Role != consts.UserRoleSubAccount {
					return errcode.ErrUserAlreadyExists
				}
				_, err := tx.TeamMember.Query().Where(teammember.TeamIDEQ(teamID), teammember.UserIDEQ(account.ID), teammember.RoleEQ(consts.TeamMemberRoleUser)).Only(ctx)
				if err != nil {
					if db.IsNotFound(err) {
						return errcode.ErrUserAlreadyExists
					}
					return err
				}
				account.Edges.Teams = []*db.Team{target}
				created = append(created, account)
				continue
			}
			pending = append(pending, input)
		}
		if role == consts.UserRoleSubAccount {
			count, err := tx.TeamMember.Query().Where(teammember.TeamIDEQ(teamID), teammember.RoleEQ(consts.TeamMemberRoleUser), teammember.HasUserWith(user.DeletedAtIsNil())).Count(ctx)
			if err != nil {
				return err
			}
			if count+len(pending) > target.MemberLimit {
				return errcode.ErrTeamMemberLimitExceeded
			}
		}
		for _, input := range pending {
			name := strings.TrimSpace(input.Name)
			if name == "" {
				name = strings.SplitN(input.Email, "@", 2)[0]
			}
			account, err := tx.User.Create().SetID(uuid.New()).SetEmail(input.Email).SetName(name).
				SetPassword(input.PasswordHash).SetRole(role).SetStatus(consts.UserStatusActive).Save(ctx)
			if err != nil {
				return err
			}
			memberRole := consts.TeamMemberRoleUser
			if role == consts.UserRoleEnterprise {
				memberRole = consts.TeamMemberRoleAdmin
			}
			if err := tx.TeamMember.Create().SetID(uuid.New()).SetTeamID(teamID).SetUserID(account.ID).SetRole(memberRole).Exec(ctx); err != nil {
				return err
			}
			if group != nil {
				if err := tx.TeamGroupMember.Create().SetID(uuid.New()).SetGroupID(group.ID).SetUserID(account.ID).Exec(ctx); err != nil {
					return err
				}
			}
			account.Edges.Teams = []*db.Team{target}
			created = append(created, account)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}
