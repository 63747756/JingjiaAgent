package work

import (
	"context"
	"github.com/63747756/jingjiaagent/backend/biz/agentresource"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
	"slices"
)

// Legacy callers cannot override the centralized enabled-skill policy.
func (s *Service) defaultSkillIDs(ctx context.Context, uid uuid.UUID, p Profile, _ *db.Project, _ []string) ([]string, error) {
	ids := append([]string{}, p.RequiredSkillIDs...)
	all, err := agentresource.AuthorizedSkills(ctx, s.ent, agentresource.ScopeFilter{UserID: &uid})
	if err != nil {
		return nil, err
	}
	for _, row := range all {
		if row.Enabled && row.ScopeType == "user" {
			ids = append(ids, row.ID.String())
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

type SelectionPreviewRequest struct {
	TeamID         uuid.UUID       `json:"team_id" query:"team_id"`
	ProjectID      *uuid.UUID      `json:"project_id,omitempty"`
	ProfileID      uuid.UUID       `json:"profile_id" query:"profile_id"`
	TaskID         *uuid.UUID      `json:"task_id,omitempty" query:"task_id"`
	SkillSelection *SkillSelection `json:"skill_selection,omitempty"`
}
type SelectionItem struct {
	SkillView
	Origins  []string `json:"origins,omitempty"`
	Selected bool     `json:"-"`
	Forced   bool     `json:"-"`
}
type SelectionPreview struct {
	Items  []SelectionItem `json:"items"`
	IDs    []string        `json:"-"`
	Count  int             `json:"-"`
	Issues []string        `json:"issues"`
}

func (s *Service) PreviewSkills(ctx context.Context, u *domain.User, req SelectionPreviewRequest) (SelectionPreview, error) {
	out := SelectionPreview{Items: []SelectionItem{}, Issues: []string{}}
	r := &SkillResolver{ent: s.ent, store: s.store}
	p := Profile{}
	var err error
	if req.TaskID != nil {
		if *req.TaskID == uuid.Nil || req.ProfileID != uuid.Nil {
			return out, errcode.ErrBadRequest
		}
		if err = s.guard.CheckWorkTaskAccess(ctx, u.ID, *req.TaskID, false); err != nil {
			return out, err
		}
		p, err = r.taskSkillProfile(ctx, u.ID, *req.TaskID)
	} else if req.ProfileID != uuid.Nil {
		create := CreateRequest{ProfileID: &req.ProfileID}
		if req.TeamID != uuid.Nil {
			create.TeamID = &req.TeamID
		}
		p, err = s.selectProfile(ctx, u.ID, create)
	}
	if err != nil {
		return out, err
	}
	rows, err := r.mentionCandidates(ctx, u.ID, p)
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		view, err := s.skill(ctx, u, row.ID, false)
		if err != nil {
			return out, err
		}
		origin := "personal"
		if row.ScopeType != "user" {
			origin = "assistant"
		}
		out.Items = append(out.Items, SelectionItem{SkillView: view, Origins: []string{origin}})
	}
	return out, nil
}
