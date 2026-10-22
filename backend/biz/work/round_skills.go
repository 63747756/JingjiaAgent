package work

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/63747756/jingjiaagent/backend/biz/agentresource"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

// SkillResolver reads current policy without depending on the execution service.
type SkillResolver struct {
	ent     *db.Client
	objects agentresource.ObjectStore
	store   *Repository
}

func (r *SkillResolver) personalCandidates(ctx context.Context, uid uuid.UUID) ([]*db.AgentSkill, error) {
	return r.mentionCandidates(ctx, uid, Profile{})
}

func (r *SkillResolver) resolveProfile(ctx context.Context, uid uuid.UUID, p Profile, mentionID *uuid.UUID) (*taskflow.RoundSkillSnapshot, *taskflow.SkillMention, error) {
	team, required := p.TeamID, p.RequiredSkillIDs
	scope := agentresource.ScopeFilter{IncludeGlobal: true, UserID: &uid, MemberID: &uid}
	if team != uuid.Nil {
		scope.TeamID = &team
	}
	rows, err := agentresource.AuthorizedSkills(ctx, r.ent, scope)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]*db.AgentSkill{}
	selected := []string{}
	sources := map[uuid.UUID]string{}
	for _, row := range rows {
		byID[row.ID.String()] = row
		if !row.Enabled {
			continue
		}
		if row.ScopeType == "user" {
			selected = append(selected, row.ID.String())
			sources[row.ID] = "personal"
		} else if row.IsForceDelivery {
			sources[row.ID] = "system"
		}
	}
	for _, id := range required {
		row := byID[id]
		if row == nil {
			parsed, parseErr := uuid.Parse(id)
			if parseErr != nil {
				return nil, nil, skillInputError(fmt.Errorf("助手内置技能标识无效，请联系管理员"))
			}
			existing, getErr := r.ent.AgentSkill.Get(ctx, parsed)
			if db.IsNotFound(getErr) || (getErr == nil && (existing.IsDeleted || !existing.Enabled)) {
				continue
			}
			if getErr != nil {
				return nil, nil, getErr
			}
			return nil, nil, errcode.ErrForbidden.Wrap(fmt.Errorf("助手内置技能未授权，请联系管理员"))
		}
		if row.ScopeType == "user" {
			return nil, nil, skillInputError(fmt.Errorf("助手只能绑定系统技能，请联系管理员"))
		}
		if row.Enabled {
			selected = append(selected, id)
			sources[row.ID] = "assistant"
		}
	}
	if mentionID != nil {
		row := byID[mentionID.String()]
		if !canMentionSkill(row, uid, p) {
			return nil, nil, skillInputError(fmt.Errorf("引用技能已停用、删除或不再允许引用，请移除引用；个人技能可在我的技能中重新启用"))
		}
	}
	slices.Sort(selected)
	selected = slices.Compact(selected)
	pins, _, err := r.pinSkills(ctx, uid, team, selected, nil)
	if err != nil {
		return nil, nil, err
	}
	snapshot := &taskflow.RoundSkillSnapshot{Pins: []taskflow.RoundSkillPin{}}
	var mention *taskflow.SkillMention
	for _, p := range pins {
		source := sources[p.SkillID]
		if source == "" {
			source = "system"
		}
		snapshot.Pins = append(snapshot.Pins, taskflow.RoundSkillPin{SkillID: p.SkillID, VersionID: p.VersionID, Name: p.Name, Version: p.Version, SHA256: p.SHA256, S3Key: p.S3Key, Source: source})
		if mentionID != nil && p.SkillID == *mentionID {
			mention = &taskflow.SkillMention{ID: p.SkillID, Name: p.Name, VersionID: p.VersionID, Version: p.Version}
		}
	}
	if mentionID != nil && mention == nil {
		return nil, nil, skillInputError(fmt.Errorf("引用技能没有可用版本，请移除引用后重试"))
	}
	return snapshot, mention, nil
}

// Read the task's bound assistant, never the caller's current/default assistant.
func (r *SkillResolver) taskSkillProfile(ctx context.Context, owner, taskID uuid.UUID) (Profile, error) {
	var savedOwner uuid.UUID
	var team sql.NullString
	var required, mentionable []byte
	err := r.store.db.QueryRowContext(ctx, `SELECT t.user_id,w.team_id::text,COALESCE(p.required_skill_ids,'[]'::jsonb),COALESCE(p.mentionable_skill_ids,'[]'::jsonb)
 FROM tasks t LEFT JOIN work_tasks w ON w.task_id=t.id
 LEFT JOIN work_execution_profiles p ON p.id=w.profile_id AND p.team_id=w.team_id
 WHERE t.id=$1 AND t.deleted_at IS NULL`, taskID).Scan(&savedOwner, &team, &required, &mentionable)
	if err != nil {
		return Profile{}, err
	}
	if savedOwner != owner {
		return Profile{}, errcode.ErrForbidden
	}
	p := Profile{}
	if team.Valid {
		p.TeamID, err = uuid.Parse(team.String)
		if err != nil {
			return p, err
		}
	}
	if err = json.Unmarshal(required, &p.RequiredSkillIDs); err != nil {
		return p, err
	}
	err = json.Unmarshal(mentionable, &p.MentionableSkillIDs)
	return p, err
}

func (r *SkillResolver) ResolveRoundSkills(ctx context.Context, owner, taskID uuid.UUID, mentionID *uuid.UUID) (*taskflow.RoundSkillSnapshot, *taskflow.SkillMention, error) {
	p, err := r.taskSkillProfile(ctx, owner, taskID)
	if err != nil {
		return nil, nil, err
	}
	return r.resolveProfile(ctx, owner, p, mentionID)
}

func snapshotPins(s *taskflow.RoundSkillSnapshot) []SkillPin {
	pins := []SkillPin{}
	for _, p := range s.Pins {
		pins = append(pins, SkillPin{SkillID: p.SkillID, VersionID: p.VersionID, Name: p.Name, Version: p.Version, SHA256: p.SHA256, S3Key: p.S3Key})
	}
	return pins
}
