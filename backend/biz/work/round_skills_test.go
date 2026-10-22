package work

import (
	"context"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/agentskill"
	"github.com/63747756/jingjiaagent/backend/db/agentskillrepo"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
	"testing"
)

func TestRoundSkillPolicyAndMentionFollowCurrentPersonalStatus(t *testing.T) {
	s, personal, uid, team := skillFixture(t)
	ctx := context.Background()
	r := &SkillResolver{ent: s.ent, objects: s.objects}
	repo := s.ent.AgentSkillRepo.Create().SetName("system").SetSourceType(agentskillrepo.SourceTypeBare).SetScopeType(agentskillrepo.ScopeTypeGlobal).SetScopeID("global").SetCreatedBy(uid).SaveX(ctx)
	system := s.ent.AgentSkill.Create().SetRepoID(repo.ID).SetName("assistant-system").SetScopeType(agentskill.ScopeTypeGlobal).SetCreatedBy(uid).SaveX(ctx)
	v := s.ent.AgentSkillVersion.Create().SetResourceID(system.ID).SetVersion("v1").SetS3Key("old.zip").SaveX(ctx)
	s.ent.AgentSkill.UpdateOneID(system.ID).SetActiveVersionID(v.ID).SaveX(ctx)
	required := []string{system.ID.String()}
	snapshot, mention, err := r.resolveProfile(ctx, uid, Profile{TeamID: team, RequiredSkillIDs: required}, &personal.ID)
	if err != nil || len(snapshot.Pins) != 2 || mention == nil || mention.ID != personal.ID {
		t.Fatalf("policy: %#v %#v %v", snapshot, mention, err)
	}
	if taskflow.SkillMentionPrompt("", mention) == "" {
		t.Fatal("skill-only message has no execution prompt")
	}
	choices, err := r.personalCandidates(ctx, uid)
	if err != nil || len(choices) != 1 || choices[0].ID != personal.ID {
		t.Fatalf("internal skill exposed: %#v %v", choices, err)
	}
	v2 := s.ent.AgentSkillVersion.Create().SetResourceID(personal.ID).SetVersion("v2").SetS3Key("old.zip").SaveX(ctx)
	s.ent.AgentSkill.UpdateOneID(personal.ID).SetActiveVersionID(v2.ID).SaveX(ctx)
	next, nextMention, err := r.resolveProfile(ctx, uid, Profile{TeamID: team, RequiredSkillIDs: required}, &personal.ID)
	if err != nil || nextMention.VersionID != v2.ID || mention.VersionID == nextMention.VersionID {
		t.Fatalf("next round did not update version: %v", err)
	}
	_ = next
	s.ent.AgentSkill.UpdateOneID(personal.ID).SetEnabled(false).SaveX(ctx)
	next, _, err = r.resolveProfile(ctx, uid, Profile{TeamID: team, RequiredSkillIDs: required}, nil)
	if err != nil || len(next.Pins) != 1 || next.Pins[0].SkillID != system.ID || len(snapshot.Pins) != 2 {
		t.Fatalf("toggle changed old snapshot: %v", err)
	}
	if _, _, err = r.resolveProfile(ctx, uid, Profile{TeamID: team, RequiredSkillIDs: required}, &personal.ID); err == nil {
		t.Fatal("mention enabled a disabled skill")
	}
	if _, _, err = r.resolveProfile(ctx, uuid.New(), Profile{TeamID: team, RequiredSkillIDs: required}, &personal.ID); err == nil {
		t.Fatal("foreign mention accepted")
	}
	s.ent.AgentSkill.UpdateOneID(personal.ID).SetEnabled(true).SetIsDeleted(true).SaveX(ctx)
	next, _, err = r.resolveProfile(ctx, uid, Profile{TeamID: team, RequiredSkillIDs: required}, nil)
	if err != nil || len(next.Pins) != 1 {
		t.Fatalf("deleted skill blocked next round: %v", err)
	}
}

func TestProjectAndLegacyOverridesDoNotChangeSkillPolicy(t *testing.T) {
	s, personal, uid, team := skillFixture(t)
	p := Profile{TeamID: team, RequiredSkillIDs: []string{uuid.NewString()}}
	a, err := s.defaultSkillIDs(context.Background(), uid, p, nil, []string{uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.defaultSkillIDs(context.Background(), uid, p, &db.Project{DefaultSkillIds: []string{uuid.NewString()}}, nil)
	if err != nil || len(a) != 2 || len(b) != 2 {
		t.Fatal("project skills affected baseline")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("project changed assistant or personal skills")
		}
	}
	if a[0] != personal.ID.String() && a[1] != personal.ID.String() {
		t.Fatal("enabled personal skill missing")
	}
}
