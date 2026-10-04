package repo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/db/agentskill"
	"github.com/chaitin/MonkeyCode/backend/db/agentskillgroupbinding"
	"github.com/chaitin/MonkeyCode/backend/db/agentskillrepo"
	"github.com/chaitin/MonkeyCode/backend/db/agentskillversion"
	"github.com/chaitin/MonkeyCode/backend/db/teamgroup"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/ent/types"
	"github.com/chaitin/MonkeyCode/backend/errcode"
	"github.com/chaitin/MonkeyCode/backend/pkg/entx"
)

// The bare repository exists before any publication. Lock it before looking up
// the resource, including the first upload, so concurrent same-name uploads do
// not create two skills or allocate the same vN. PostgreSQL locks coordinate
// separate server processes, not just requests in this process.
func (r *teamSkillRepo) PublishSkill(ctx context.Context, teamID, userID uuid.UUID, req *domain.TeamSkillPublication, upload func(context.Context, uuid.UUID, uuid.UUID) (string, error)) (*db.AgentSkill, error) {
	var out *db.AgentSkill
	err := entx.WithTx2(ctx, r.client, func(tx *db.Tx) error {
		q := tx.AgentSkillRepo.Query().Where(agentskillrepo.ScopeTypeEQ(agentskillrepo.ScopeTypeTeam),
			agentskillrepo.ScopeIDEQ(teamID.String()), agentskillrepo.SourceTypeEQ(agentskillrepo.SourceTypeBare), agentskillrepo.IsDeletedEQ(false))
		if r.postgres {
			q.ForUpdate()
		}
		bare, err := q.Only(ctx)
		if err != nil {
			return err
		}
		lookup := tx.AgentSkill.Query().Where(agentskill.RepoIDEQ(bare.ID), agentskill.ScopeTypeEQ(agentskill.ScopeTypeTeam),
			agentskill.ScopeIDEQ(teamID.String()), agentskill.IsDeletedEQ(false))
		if req.SkillID != uuid.Nil {
			lookup.Where(agentskill.IDEQ(req.SkillID))
		} else {
			lookup.Where(agentskill.NameEQ(req.Name))
		}
		if r.postgres {
			lookup.ForUpdate()
		}
		skill, err := lookup.Only(ctx)
		if db.IsNotFound(err) && req.SkillID == uuid.Nil {
			skill, err = tx.AgentSkill.Create().SetID(uuid.New()).SetRepoID(bare.ID).SetName(req.Name).
				SetScopeType(agentskill.ScopeTypeTeam).SetScopeID(teamID.String()).SetCreatedBy(userID).
				SetEnabled(true).SetNillableExtensionPackageID(nillableString(req.ExtensionPackageID)).Save(ctx)
		}
		if err != nil {
			return err
		}
		if err := r.replacePublicationGrants(ctx, tx, teamID, skill.ID, req.GroupIDs); err != nil {
			return err
		}
		versions, err := tx.AgentSkillVersion.Query().Where(agentskillversion.ResourceIDEQ(skill.ID)).All(ctx)
		if err != nil {
			return err
		}
		max := 0
		for _, ver := range versions {
			if n, err := strconv.Atoi(strings.TrimPrefix(ver.Version, "v")); err == nil && n > max {
				max = n
			}
		}
		versionID := uuid.New()
		s3Key, err := upload(ctx, skill.ID, versionID)
		if err != nil {
			return err // the resource, grants and version all roll back
		}
		description := skill.Description
		if req.Description != nil {
			description = *req.Description
		}
		meta := req.Meta
		ver, err := tx.AgentSkillVersion.Create().SetID(versionID).SetResourceID(skill.ID).
			SetVersion(fmt.Sprintf("v%d", max+1)).SetS3Key(s3Key).SetParsedMeta(types.SkillParsedMeta{
			Description: description, Tags: meta.Tags, Categories: meta.Categories,
			SourceType: meta.SourceType, SourceLabel: meta.SourceLabel,
		}).Save(ctx)
		if err != nil {
			return err
		}
		u := tx.AgentSkill.UpdateOneID(skill.ID).SetDescription(description).SetActiveVersionID(ver.ID).SetUpdatedAt(time.Now())
		if req.IsForceDelivery != nil {
			u.SetIsForceDelivery(*req.IsForceDelivery)
		}
		out, err = u.Save(ctx)
		return err
	})
	return out, err
}

func (r *teamSkillRepo) replacePublicationGrants(ctx context.Context, tx *db.Tx, teamID, skillID uuid.UUID, groupIDs []uuid.UUID) error {
	if groupIDs == nil {
		return nil
	}
	if len(groupIDs) > 0 {
		q := tx.TeamGroup.Query().Where(teamgroup.IDIn(groupIDs...), teamgroup.TeamIDEQ(teamID), teamgroup.DeletedAtIsNil())
		if r.postgres {
			q.ForShare()
		}
		groups, err := q.All(ctx)
		if err != nil {
			return err
		}
		if len(groups) != len(groupIDs) {
			return errcode.ErrBadRequest.Wrap(fmt.Errorf("skill groups do not belong to the team"))
		}
	}
	if _, err := tx.AgentSkillGroupBinding.Delete().Where(agentskillgroupbinding.SkillIDEQ(skillID)).Exec(ctx); err != nil {
		return err
	}
	for _, id := range groupIDs {
		if _, err := tx.AgentSkillGroupBinding.Create().SetID(uuid.New()).SetSkillID(skillID).SetGroupID(id).Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *teamSkillRepo) UpdateSkillMetadata(ctx context.Context, teamID uuid.UUID, req *domain.UpdateTeamSkillReq) (*db.AgentSkill, error) {
	var out *db.AgentSkill
	err := entx.WithTx2(ctx, r.client, func(tx *db.Tx) error {
		q := tx.AgentSkill.Query().Where(agentskill.IDEQ(req.SkillID), agentskill.ScopeTypeEQ(agentskill.ScopeTypeTeam),
			agentskill.ScopeIDEQ(teamID.String()), agentskill.IsDeletedEQ(false))
		if r.postgres {
			q.ForUpdate()
		}
		skill, err := q.Only(ctx)
		if err != nil {
			return err
		}
		if err := r.replacePublicationGrants(ctx, tx, teamID, skill.ID, req.GroupIDs); err != nil {
			return err
		}
		u := tx.AgentSkill.UpdateOneID(skill.ID).SetUpdatedAt(time.Now())
		if strings.TrimSpace(req.Name) != "" {
			u.SetName(req.Name)
		}
		if req.Description != nil {
			u.SetDescription(*req.Description)
		}
		if req.IsForceDelivery != nil {
			u.SetIsForceDelivery(*req.IsForceDelivery)
		}
		out, err = u.Save(ctx)
		if err != nil {
			return err
		}
		if skill.ActiveVersionID != nil && (req.Tags != nil || req.Description != nil) {
			ver, err := tx.AgentSkillVersion.Get(ctx, *skill.ActiveVersionID)
			if err != nil {
				return err
			}
			meta := ver.ParsedMeta
			if req.Tags != nil {
				meta.Tags = req.Tags
			}
			if req.Description != nil {
				meta.Description = *req.Description
			}
			_, err = tx.AgentSkillVersion.UpdateOneID(ver.ID).SetParsedMeta(meta).Save(ctx)
			return err
		}
		return nil
	})
	return out, err
}
