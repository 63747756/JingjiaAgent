package work

import (
	"context"
	"fmt"
	"slices"

	"github.com/63747756/jingjiaagent/backend/biz/agentresource"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
)

func validateMentionableSkills(p *Profile) error {
	if len(p.MentionableSkillIDs) > 32 {
		return errcode.ErrBadRequest
	}
	ids := make([]string, 0, len(p.MentionableSkillIDs))
	for _, raw := range p.MentionableSkillIDs {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil || !slices.Contains(p.RequiredSkillIDs, id.String()) {
			return skillInputError(fmt.Errorf("只有已绑定的助手内置技能可以允许员工引用"))
		}
		if !slices.Contains(ids, id.String()) {
			ids = append(ids, id.String())
		}
	}
	p.MentionableSkillIDs = ids
	return nil
}

// Visibility is per assistant binding; it never changes installation or ownership.
func canMentionSkill(row *db.AgentSkill, uid uuid.UUID, p Profile) bool {
	if row == nil || row.IsDeleted || !row.Enabled || row.ActiveVersionID == nil {
		return false
	}
	if row.ScopeType == "user" {
		return row.ScopeID == uid.String()
	}
	if row.ScopeType != "global" && !(row.ScopeType == "team" && row.ScopeID == p.TeamID.String()) {
		return false
	}
	id := row.ID.String()
	return slices.Contains(p.RequiredSkillIDs, id) && slices.Contains(p.MentionableSkillIDs, id)
}

func (r *SkillResolver) mentionCandidates(ctx context.Context, uid uuid.UUID, p Profile) ([]*db.AgentSkill, error) {
	scope := agentresource.ScopeFilter{IncludeGlobal: true, UserID: &uid, MemberID: &uid}
	if p.TeamID != uuid.Nil {
		scope.TeamID = &p.TeamID
	}
	rows, err := agentresource.AuthorizedSkills(ctx, r.ent, scope)
	if err != nil {
		return nil, err
	}
	out := []*db.AgentSkill{}
	for _, row := range rows {
		if !canMentionSkill(row, uid, p) {
			continue
		}
		v, err := r.ent.AgentSkillVersion.Get(ctx, *row.ActiveVersionID)
		if db.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if v.ResourceID == row.ID {
			out = append(out, row)
		}
	}
	return out, nil
}
