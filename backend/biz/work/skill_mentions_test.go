package work

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/63747756/jingjiaagent/backend/db/agentskill"
	"github.com/63747756/jingjiaagent/backend/db/agentskillrepo"
	"github.com/google/uuid"
)

func TestAssistantMentionPermissionDoesNotChangeLoadedSkills(t *testing.T) {
	s, personal, uid, team := skillFixture(t)
	ctx := context.Background()
	r := &SkillResolver{ent: s.ent, objects: s.objects}
	repo := s.ent.AgentSkillRepo.Create().SetName("mention-system").SetSourceType(agentskillrepo.SourceTypeBare).SetScopeType(agentskillrepo.ScopeTypeGlobal).SetScopeID("global").SetCreatedBy(uid).SaveX(ctx)
	system := s.ent.AgentSkill.Create().SetRepoID(repo.ID).SetName("mention-system").SetScopeType(agentskill.ScopeTypeGlobal).SetCreatedBy(uid).SaveX(ctx)
	v := s.ent.AgentSkillVersion.Create().SetResourceID(system.ID).SetVersion("v1").SetS3Key("old.zip").SaveX(ctx)
	s.ent.AgentSkill.UpdateOneID(system.ID).SetActiveVersionID(v.ID).SaveX(ctx)
	p := Profile{TeamID: team, RequiredSkillIDs: []string{system.ID.String()}}
	assertChoices := func(wantSystem bool) {
		t.Helper()
		rows, err := r.mentionCandidates(ctx, uid, p)
		if err != nil {
			t.Fatal(err)
		}
		ids := []uuid.UUID{}
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		if !slices.Contains(ids, personal.ID) || slices.Contains(ids, system.ID) != wantSystem {
			t.Fatalf("incorrect candidates: %v", ids)
		}
	}
	assertChoices(false)
	if _, _, err := r.resolveProfile(ctx, uid, p, &system.ID); err == nil {
		t.Fatal("hidden system skill was referenceable")
	}
	p.MentionableSkillIDs = []string{system.ID.String()}
	assertChoices(true)
	first, mention, err := r.resolveProfile(ctx, uid, p, &system.ID)
	if err != nil || mention == nil || mention.VersionID != v.ID || len(first.Pins) != 2 {
		t.Fatalf("allowed reference: %v", err)
	}
	p.MentionableSkillIDs = nil
	assertChoices(false)
	second, _, err := r.resolveProfile(ctx, uid, p, nil)
	if err != nil || len(second.Pins) != 2 || first.Pins[0] != second.Pins[0] || first.Pins[1] != second.Pins[1] {
		t.Fatalf("visibility changed loading or frozen snapshot: %v", err)
	}
	if _, _, err := r.resolveProfile(ctx, uid, p, &system.ID); err == nil {
		t.Fatal("revoked reference accepted on next round")
	}
	p.RequiredSkillIDs = nil
	p.MentionableSkillIDs = []string{system.ID.String()}
	assertChoices(false)
	if _, _, err := r.resolveProfile(ctx, uid, p, &system.ID); err == nil {
		t.Fatal("unbound skill reference accepted")
	}
	p.RequiredSkillIDs = []string{system.ID.String()}
	s.ent.AgentSkill.UpdateOneID(system.ID).SetEnabled(false).SaveX(ctx)
	assertChoices(false)
	if _, _, err := r.resolveProfile(ctx, uid, p, &system.ID); err == nil {
		t.Fatal("disabled system skill reference accepted")
	}
}

func TestMentionableBindingsValidation(t *testing.T) {
	id := uuid.NewString()
	p := Profile{RequiredSkillIDs: []string{id}}
	if err := validateMentionableSkills(&p); err != nil || p.MentionableSkillIDs == nil {
		t.Fatal("default must persist as an empty array")
	}
	p.MentionableSkillIDs = []string{id, id}
	if err := validateMentionableSkills(&p); err != nil || len(p.MentionableSkillIDs) != 1 {
		t.Fatal("duplicate references not normalized")
	}
	for _, value := range []string{uuid.NewString(), uuid.Nil.String(), "invalid"} {
		p.MentionableSkillIDs = []string{value}
		if err := validateMentionableSkills(&p); err == nil {
			t.Fatal("invalid or unbound reference accepted")
		}
	}
}

func TestTaskMentionPolicyUsesBoundAssistantAndCurrentVisibility(t *testing.T) {
	dsn := os.Getenv("WORK_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("WORK_POSTGRES_TEST_DSN not set")
	}
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TEMP TABLE tasks(id uuid,user_id uuid,deleted_at timestamptz);
CREATE TEMP TABLE work_tasks(task_id uuid,team_id uuid,profile_id uuid);
CREATE TEMP TABLE work_execution_profiles(id uuid,team_id uuid,required_skill_ids jsonb);`)
	migration, err := os.ReadFile(filepath.Join("..", "..", "migration", "000045_assistant_skill_mentions.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	exec(string(migration))
	uid, team, task, oldTask, assistant, other, skill := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO tasks VALUES($1,$2,NULL),($3,$2,NULL)`, task, uid, oldTask)
	exec(`INSERT INTO work_execution_profiles(id,team_id,required_skill_ids) VALUES($1,$2,jsonb_build_array($3::text)),($4,$2,'[]')`, assistant, team, skill.String(), other)
	exec(`INSERT INTO work_tasks VALUES($1,$2,$3)`, task, team, assistant)
	r := &SkillResolver{store: &Repository{db: conn}}
	p, err := r.taskSkillProfile(context.Background(), uid, task)
	if err != nil || len(p.MentionableSkillIDs) != 0 || !slices.Contains(p.RequiredSkillIDs, skill.String()) {
		t.Fatalf("migration or task binding: %v", err)
	}
	exec(`UPDATE work_execution_profiles SET mentionable_skill_ids=jsonb_build_array($2::text) WHERE id=$1`, other, skill.String())
	p, err = r.taskSkillProfile(context.Background(), uid, task)
	if err != nil || len(p.MentionableSkillIDs) != 0 {
		t.Fatal("another assistant affected existing task")
	}
	exec(`UPDATE work_execution_profiles SET mentionable_skill_ids=jsonb_build_array($2::text) WHERE id=$1`, assistant, skill.String())
	p, err = r.taskSkillProfile(context.Background(), uid, task)
	if err != nil || !slices.Contains(p.MentionableSkillIDs, skill.String()) {
		t.Fatal("current assistant setting not loaded")
	}
	if _, err := r.taskSkillProfile(context.Background(), uuid.New(), task); err == nil {
		t.Fatal("foreign owner accepted")
	}
	p, err = r.taskSkillProfile(context.Background(), uid, oldTask)
	if err != nil || len(p.MentionableSkillIDs) != 0 || len(p.RequiredSkillIDs) != 0 {
		t.Fatal("legacy task gained an assistant")
	}
}
