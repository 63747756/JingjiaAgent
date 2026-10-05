package repo

import (
	"context"
	"errors"

	teamrepo "github.com/63747756/jingjiaagent/backend/biz/team/repo"
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/host"
	"github.com/63747756/jingjiaagent/backend/db/teamhost"
	"github.com/63747756/jingjiaagent/backend/db/teammember"
	"github.com/63747756/jingjiaagent/backend/pkg/entx"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

// Administrator configuration may enroll a new host. Heartbeats only update
// metadata: they never change ownership, restore group grants or resurrect a
// host deleted through the original product workflow.
func (h *HostRepo) SyncRuntimeHost(ctx context.Context, node config.RuntimeNode, info *taskflow.Host) error {
	if info == nil || info.ID != node.ID {
		return errors.New("runtime host metadata does not match configured node")
	}
	return entx.WithTx2(ctx, h.db, func(tx *db.Tx) error {
		existing, err := tx.Host.Query().Where(host.ID(node.ID)).Only(entx.SkipSoftDelete(ctx))
		if err != nil && !db.IsNotFound(err) {
			return err
		}
		owner := uuid.Nil
		if existing != nil {
			if !existing.DeletedAt.IsZero() {
				return errors.New("runtime host was deleted")
			}
			owner = existing.UserID
			if node.OwnerID != "" && owner.String() != node.OwnerID {
				return errors.New("runtime host owner cannot change through configuration")
			}
		} else {
			owner, err = uuid.Parse(node.OwnerID)
			if err != nil || owner == uuid.Nil {
				return errors.New("new runtime host requires an existing owner_id")
			}
		}
		account, err := tx.User.Get(ctx, owner)
		if err != nil || account.IsBlocked || account.Status != consts.UserStatusActive {
			return errors.New("runtime host owner is unavailable")
		}
		var teamID uuid.UUID
		if node.TeamID != "" {
			teamID, err = uuid.Parse(node.TeamID)
			if err != nil || teamID == uuid.Nil {
				return errors.New("invalid runtime host team")
			}
			if _, err = tx.Team.Get(ctx, teamID); err != nil {
				return errors.New("runtime host team is unavailable")
			}
			allowed, err := tx.TeamMember.Query().Where(teammember.TeamID(teamID), teammember.UserID(owner), teammember.RoleEQ(consts.TeamMemberRoleAdmin)).Exist(ctx)
			if err != nil || !allowed {
				return errors.New("runtime host registration requires an active team administrator")
			}
			if existing != nil {
				allowed, err = tx.TeamHost.Query().Where(teamhost.TeamID(teamID), teamhost.HostID(node.ID)).Exist(ctx)
				if err != nil || !allowed {
					return errors.New("existing runtime host team binding cannot change")
				}
			}
		}
		if existing == nil {
			if err = tx.Host.Create().SetID(node.ID).SetUserID(owner).Exec(ctx); err != nil {
				return err
			}
			if teamID != uuid.Nil {
				if err = tx.TeamHost.Create().SetID(uuid.New()).SetTeamID(teamID).SetHostID(node.ID).Exec(ctx); err != nil {
					return err
				}
				if err = teamrepo.AddDefaultGroupHost(ctx, tx, teamID, node.ID); err != nil {
					return err
				}
			}
		}
		updated, err := tx.Host.Update().Where(host.ID(node.ID), host.UserID(owner), host.DeletedAtIsNil()).
			SetHostname(info.Hostname).SetArch(info.Arch).SetOs(info.OS).SetCores(int(info.Cores)).
			SetMemory(int64(info.Memory)).SetDisk(int64(info.Disk)).SetVersion(info.Version).Save(ctx)
		if err != nil {
			return err
		}
		if updated != 1 {
			return errors.New("runtime host was removed during synchronization")
		}
		return nil
	})
}

func (h *HostRepo) ListRuntimeHosts(ctx context.Context, actor string, ids []string) (map[string]*taskflow.Host, error) {
	uid, err := uuid.Parse(actor)
	if err != nil || uid == uuid.Nil {
		return nil, errors.New("invalid runtime host user")
	}
	out := map[string]*taskflow.Host{}
	if len(ids) == 0 {
		return out, nil
	}
	// Reuse the original user/group/public-host predicate; being present in the
	// server configuration does not make a host visible to every logged-in user.
	hosts, err := h.db.Host.Query().Where(host.IDIn(ids...), hostWithUserPredicate(uid)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range hosts {
		out[item.ID] = &taskflow.Host{ID: item.ID, UserID: item.UserID.String(), Hostname: item.Hostname, Arch: item.Arch, OS: item.Os, Cores: int32(item.Cores), Memory: uint64(item.Memory), Disk: uint64(item.Disk), Version: item.Version}
	}
	return out, nil
}
