package repo

import (
	"context"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/teamhost"
	"github.com/63747756/jingjiaagent/backend/db/teammember"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/entx"
	"github.com/google/uuid"
)

// Recheck live account and original team grants on every use of an install ticket.
func (h *HostRepo) AuthorizeRuntimeInstall(ctx context.Context, actor, team string, node config.RuntimeNode) error {
	actorID, err := uuid.Parse(actor)
	if err != nil || actorID == uuid.Nil || node.TeamID != team {
		return errcode.ErrRuntimeInstallScope
	}
	ownerID, err := uuid.Parse(node.OwnerID)
	if err != nil || ownerID == uuid.Nil {
		return errcode.ErrRuntimeInstallScope
	}
	for _, id := range []uuid.UUID{actorID, ownerID} {
		account, err := h.db.User.Get(ctx, id)
		if err != nil || account.IsBlocked || account.Status != consts.UserStatusActive {
			return errcode.ErrRuntimeInstallScope
		}
	}
	if team == "" {
		if actorID != ownerID {
			return errcode.ErrRuntimeInstallScope
		}
	} else {
		teamID, err := uuid.Parse(team)
		if err != nil {
			return errcode.ErrRuntimeInstallScope
		}
		if _, err = h.db.Team.Get(ctx, teamID); err != nil {
			return errcode.ErrRuntimeInstallScope
		}
		for _, id := range []uuid.UUID{actorID, ownerID} {
			ok, err := h.db.TeamMember.Query().Where(teammember.TeamID(teamID), teammember.UserID(id), teammember.RoleEQ(consts.TeamMemberRoleAdmin)).Exist(ctx)
			if err != nil {
				return err
			}
			if !ok {
				return errcode.ErrRuntimeInstallScope
			}
		}
	}
	existing, err := h.db.Host.Get(entx.SkipSoftDelete(ctx), node.ID)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !existing.DeletedAt.IsZero() || existing.UserID != ownerID {
		return errcode.ErrRuntimeInstallScope
	}
	if team != "" {
		id, _ := uuid.Parse(team)
		ok, err := h.db.TeamHost.Query().Where(teamhost.TeamID(id), teamhost.HostID(node.ID)).Exist(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return errcode.ErrRuntimeInstallScope
		}
	}
	return nil
}
