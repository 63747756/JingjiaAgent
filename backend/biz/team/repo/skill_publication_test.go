package repo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"entgo.io/ent"
	"github.com/google/uuid"

	"github.com/chaitin/MonkeyCode/backend/biz/agentresource"
	"github.com/chaitin/MonkeyCode/backend/db"
	"github.com/chaitin/MonkeyCode/backend/db/agentskill"
	"github.com/chaitin/MonkeyCode/backend/db/agentskillversion"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/errcode"
	"github.com/chaitin/MonkeyCode/backend/pkg/entx"
)

func skillPublicationBareRepo(t *testing.T, client *db.Client, teamID, adminID uuid.UUID) {
	t.Helper()
	if err := entx.WithTx2(context.Background(), client, func(tx *db.Tx) error {
		_, _, err := agentresource.EnsureTeamBareReposTx(context.Background(), tx, teamID, adminID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func checkSkillPublication(t *testing.T, client *db.Client, postgres bool) {
	t.Helper()
	ctx := context.Background()
	teamID, adminID := localMemberFixture(t, client, 5)
	otherTeam, _ := localMemberFixture(t, client, 5)
	skillPublicationBareRepo(t, client, teamID, adminID)
	skillPublicationBareRepo(t, client, otherTeam, adminID)
	group := client.TeamGroup.Create().SetID(uuid.New()).SetTeamID(teamID).SetName("restricted skill").SaveX(ctx)
	foreign := client.TeamGroup.Create().SetID(uuid.New()).SetTeamID(otherTeam).SetName("foreign").SaveX(ctx)
	r := &teamSkillRepo{client: client, postgres: postgres}
	objects := map[string]string{}
	upload := func(body string) func(context.Context, uuid.UUID, uuid.UUID) (string, error) {
		return func(_ context.Context, skillID, objectID uuid.UUID) (string, error) {
			key := fmt.Sprintf("skills/%s/%s.zip", skillID, objectID)
			if _, exists := objects[key]; exists {
				t.Fatal("immutable object overwritten")
			}
			objects[key] = body
			return key, nil
		}
	}
	description, force := "Chinese version one", true
	initial := &domain.TeamSkillPublication{Name: "version-fixture", Description: &description, IsForceDelivery: &force,
		GroupIDs: []uuid.UUID{group.ID}, Meta: domain.SkillVersionMeta{Tags: []string{"中文"}}}
	skill, err := r.PublishSkill(ctx, teamID, adminID, initial, upload("body-v1"))
	if err != nil {
		t.Fatal(err)
	}
	first := client.AgentSkillVersion.GetX(ctx, *skill.ActiveVersionID)
	if first.Version != "v1" || objects[first.S3Key] != "body-v1" {
		t.Fatal("first publication differs")
	}
	// Content-only updates must keep scope/description/force delivery; a new
	// body must not overwrite bytes addressed by the previous active version.
	skill, err = r.PublishSkill(ctx, teamID, adminID, &domain.TeamSkillPublication{SkillID: skill.ID}, upload("body-v2"))
	if err != nil {
		t.Fatal(err)
	}
	second := client.AgentSkillVersion.GetX(ctx, *skill.ActiveVersionID)
	if second.Version != "v2" || second.S3Key == first.S3Key || objects[first.S3Key] != "body-v1" || skill.Description != description || !skill.IsForceDelivery {
		t.Fatal("content update mutated previous version or metadata")
	}
	grants, err := r.LoadGroups(ctx, skill.ID)
	if err != nil || len(grants) != 1 || grants[0].ID != group.ID.String() {
		t.Fatal("omitted grants were cleared")
	}
	badDescription := "must not commit"
	failUpload := errors.New("object store unavailable")
	_, err = r.PublishSkill(ctx, teamID, adminID, &domain.TeamSkillPublication{SkillID: skill.ID, Description: &badDescription, GroupIDs: []uuid.UUID{}},
		func(context.Context, uuid.UUID, uuid.UUID) (string, error) { return "", failUpload })
	if !errors.Is(err, failUpload) {
		t.Fatalf("upload failure: %v", err)
	}
	after := client.AgentSkill.GetX(ctx, skill.ID)
	grants, _ = r.LoadGroups(ctx, skill.ID)
	if after.ActiveVersionID == nil || *after.ActiveVersionID != second.ID || after.Description != description || len(grants) != 1 || client.AgentSkillVersion.Query().Where(agentskillversion.ResourceIDEQ(skill.ID)).CountX(ctx) != 2 {
		t.Fatal("failed publication partially changed active state/grants")
	}
	_, err = r.PublishSkill(ctx, teamID, adminID, &domain.TeamSkillPublication{Name: "failed-first-upload"},
		func(context.Context, uuid.UUID, uuid.UUID) (string, error) { return "", failUpload })
	if !errors.Is(err, failUpload) || client.AgentSkill.Query().Where(agentskill.NameEQ("failed-first-upload")).ExistX(ctx) {
		t.Fatal("failed first upload left an orphan business resource")
	}
	// Fail database activation after a successful object upload. An orphaned
	// unique object may remain, but committed bytes and active state must not.
	failActivation := true
	client.AgentSkill.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if skillMutation, ok := m.(*db.AgentSkillMutation); ok && failActivation {
				if _, set := skillMutation.ActiveVersionID(); set {
					return nil, errors.New("activation failed")
				}
			}
			return next.Mutate(ctx, m)
		})
	})
	_, err = r.PublishSkill(ctx, teamID, adminID, &domain.TeamSkillPublication{SkillID: skill.ID, Description: &badDescription, GroupIDs: []uuid.UUID{}}, upload("uncommitted-body"))
	failActivation = false
	after = client.AgentSkill.GetX(ctx, skill.ID)
	grants, _ = r.LoadGroups(ctx, skill.ID)
	if err == nil || after.ActiveVersionID == nil || *after.ActiveVersionID != second.ID || len(grants) != 1 || objects[first.S3Key] != "body-v1" || objects[second.S3Key] != "body-v2" || client.AgentSkillVersion.Query().Where(agentskillversion.ResourceIDEQ(skill.ID)).CountX(ctx) != 2 {
		t.Fatal("activation failure changed committed version/grants/objects")
	}
	called := false
	_, err = r.PublishSkill(ctx, teamID, adminID, &domain.TeamSkillPublication{SkillID: skill.ID, GroupIDs: []uuid.UUID{foreign.ID}},
		func(context.Context, uuid.UUID, uuid.UUID) (string, error) { called = true; return "bad.zip", nil })
	if !errors.Is(err, errcode.ErrBadRequest) || called {
		t.Fatalf("foreign grant reached upload: %v", err)
	}
	_, err = r.UpdateSkillMetadata(ctx, teamID, &domain.UpdateTeamSkillReq{SkillID: skill.ID, Description: &badDescription, Tags: []string{"changed"}, GroupIDs: []uuid.UUID{foreign.ID}})
	if !errors.Is(err, errcode.ErrBadRequest) || client.AgentSkill.GetX(ctx, skill.ID).Description != description {
		t.Fatal("invalid metadata grants partially committed")
	}
	if _, err = r.PublishSkill(ctx, otherTeam, adminID, &domain.TeamSkillPublication{SkillID: skill.ID}, upload("foreign")); err == nil {
		t.Fatal("foreign resource mutated")
	}
	_, err = r.UpdateSkillMetadata(ctx, teamID, &domain.UpdateTeamSkillReq{SkillID: skill.ID, GroupIDs: []uuid.UUID{}})
	grants, _ = r.LoadGroups(ctx, skill.ID)
	if err != nil || len(grants) != 0 {
		t.Fatal("explicit empty grants did not clear")
	}
}

func TestSkillPublicationAtomicVersionsAndGrants(t *testing.T) {
	checkSkillPublication(t, newTeamRepoTestDB(t), false)
}
func TestSkillPublicationPostgresAtomicVersionsAndGrants(t *testing.T) {
	checkSkillPublication(t, localMembersPostgres(t), true)
}

func TestSkillPublicationPostgresConcurrentFirstUpload(t *testing.T) {
	client := localMembersPostgres(t)
	ctx := context.Background()
	teamID, adminID := localMemberFixture(t, client, 5)
	skillPublicationBareRepo(t, client, teamID, adminID)
	r := &teamSkillRepo{client: client, postgres: true}
	start := make(chan struct{})
	results := make(chan error, 4)
	objects := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			description := fmt.Sprintf("concurrent body %d", i)
			_, err := r.PublishSkill(ctx, teamID, adminID, &domain.TeamSkillPublication{Name: "same-name-first-upload", Description: &description},
				func(_ context.Context, skillID, objectID uuid.UUID) (string, error) {
					key := fmt.Sprintf("%s/%s.zip", skillID, objectID)
					mu.Lock()
					defer mu.Unlock()
					if _, exists := objects[key]; exists {
						return "", errors.New("object overwritten")
					}
					objects[key] = description
					return key, nil
				})
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	skills := client.AgentSkill.Query().Where(agentskill.NameEQ("same-name-first-upload")).AllX(ctx)
	if len(skills) != 1 {
		t.Fatalf("created %d same-name skills", len(skills))
	}
	versions := client.AgentSkillVersion.Query().Where(agentskillversion.ResourceIDEQ(skills[0].ID)).AllX(ctx)
	seen := map[string]bool{}
	for _, v := range versions {
		if seen[v.Version] || objects[v.S3Key] != v.ParsedMeta.Description {
			t.Fatal("duplicate version or mismatched immutable body")
		}
		seen[v.Version] = true
	}
	active := client.AgentSkillVersion.GetX(ctx, *skills[0].ActiveVersionID)
	if len(versions) != 4 || !seen["v1"] || !seen["v2"] || !seen["v3"] || !seen["v4"] || active.Version != "v4" || active.ParsedMeta.Description != skills[0].Description {
		t.Fatal("concurrent publication did not converge to the latest committed version")
	}
}
