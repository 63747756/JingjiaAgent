package work

import (
	"context"
	"errors"
	"testing"

	"github.com/63747756/jingjiaagent/backend/biz/agentresource"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/agentskill"
	"github.com/63747756/jingjiaagent/backend/db/agentskillrepo"
	"github.com/63747756/jingjiaagent/backend/db/enttest"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

func skillFixture(t *testing.T) (*Service, *db.AgentSkill, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	uid, team := uuid.New(), uuid.New()
	c := enttest.Open(t, "sqlite3", "file:work-skill-"+uuid.NewString()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { c.Close() })
	repo := c.AgentSkillRepo.Create().SetName("personal").SetSourceType(agentskillrepo.SourceTypeBare).SetScopeType(agentskillrepo.ScopeTypeUser).SetScopeID(uid.String()).SetCreatedBy(uid).SaveX(ctx)
	skill := c.AgentSkill.Create().SetRepoID(repo.ID).SetName("fixture").SetScopeType(agentskill.ScopeTypeUser).SetScopeID(uid.String()).SetPublicationState("draft").SetCreatedBy(uid).SaveX(ctx)
	data, _ := zipMarkdown("first version")
	objects := &memorySkills{map[string][]byte{"old.zip": data}}
	v := c.AgentSkillVersion.Create().SetResourceID(skill.ID).SetVersion("v1").SetS3Key("old.zip").SaveX(ctx)
	skill = c.AgentSkill.UpdateOneID(skill.ID).SetActiveVersionID(v.ID).SaveX(ctx)
	return &Service{ent: c, objects: objects, skillRepo: agentresource.NewRepo(c)}, skill, uid, team
}
func TestWorkSkillPinsStayOnOriginalVersion(t *testing.T) {
	s, skill, uid, team := skillFixture(t)
	ctx := context.Background()
	pins, refs, err := s.pinSkills(ctx, uid, team, []string{skill.ID.String()}, nil)
	if err != nil || len(pins) != 1 {
		t.Fatalf("pin: %v %v", pins, err)
	}
	v := s.ent.AgentSkillVersion.Create().SetResourceID(skill.ID).SetVersion("v2").SetS3Key("new.zip").SaveX(ctx)
	s.ent.AgentSkill.UpdateOneID(skill.ID).SetActiveVersionID(v.ID).SaveX(ctx)
	if pins[0].VersionID != *skill.ActiveVersionID || pins[0].VersionID == v.ID || pins[0].S3Key != "old.zip" || refs[0].SHA256 != pins[0].SHA256 {
		t.Fatalf("pin moved after edit: %v", pins)
	}
	if _, _, err = s.pinSkills(ctx, uid, team, []string{skill.ID.String()}, nil); !errors.Is(err, errcode.ErrBadRequest) {
		t.Fatalf("missing new package admitted: %v", err)
	}
}
func TestWorkRequiredSkillsFailClosed(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		s, skill, uid, team := skillFixture(t)
		s.ent.AgentSkill.UpdateOneID(skill.ID).SetEnabled(false).SaveX(context.Background())
		if _, _, err := s.pinSkills(context.Background(), uid, team, nil, []string{skill.ID.String()}); !errors.Is(err, errcode.ErrForbidden) {
			t.Fatalf("disabled mandatory accepted: %v", err)
		}
	})
	t.Run("foreign", func(t *testing.T) {
		s, skill, _, team := skillFixture(t)
		if _, _, err := s.pinSkills(context.Background(), uuid.New(), team, nil, []string{skill.ID.String()}); !errors.Is(err, errcode.ErrForbidden) {
			t.Fatalf("foreign mandatory accepted: %v", err)
		}
	})
	t.Run("hash mismatch", func(t *testing.T) {
		s, skill, uid, team := skillFixture(t)
		s.ent.AgentSkillVersion.UpdateOneID(*skill.ActiveVersionID).SetContentHash("bad-digest").SaveX(context.Background())
		if _, _, err := s.pinSkills(context.Background(), uid, team, nil, []string{skill.ID.String()}); !errors.Is(err, errcode.ErrBadRequest) {
			t.Fatalf("corrupt mandatory accepted: %v", err)
		}
	})
	t.Run("forced version absent", func(t *testing.T) {
		s, skill, uid, team := skillFixture(t)
		s.ent.AgentSkill.UpdateOneID(skill.ID).SetIsForceDelivery(true).ClearActiveVersionID().SaveX(context.Background())
		if _, _, err := s.pinSkills(context.Background(), uid, team, nil, nil); !errors.Is(err, errcode.ErrForbidden) {
			t.Fatalf("broken forced skill silently omitted: %v", err)
		}
	})
}
